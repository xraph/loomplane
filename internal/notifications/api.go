package notifications

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/xraph/herald/id"
	"github.com/xraph/herald/inbox"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/endpoint"
	relayid "github.com/xraph/relay/id"
	"github.com/xraph/relay/signature"

	"github.com/xraph/loomplane/internal/security"
)

type Notification struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	ActionURL   string    `json:"action_url"`
	Read        bool      `json:"read"`
	CreatedAt   time.Time `json:"created_at"`
	ProjectID   string    `json:"project_id"`
	Status      string    `json:"status"`
	EmailStatus string    `json:"email_status"`
}
type NotificationList struct {
	Items       []Notification `json:"items"`
	MailEnabled bool           `json:"mail_enabled"`
}
type Preferences struct {
	DeploymentEmail bool `json:"deployment_email"`
	MailEnabled     bool `json:"mail_enabled"`
}
type PreferenceInput struct {
	DeploymentEmail bool `json:"deployment_email"`
}
type WebhookEndpoint struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Description string    `json:"description"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
}
type WebhookEndpointInput struct {
	URL         string `json:"url"`
	Description string `json:"description"`
}
type WebhookEndpointList struct {
	Items []WebhookEndpoint `json:"items"`
}
type WebhookEndpointSecret struct {
	Endpoint WebhookEndpoint `json:"endpoint"`
	Secret   string          `json:"secret"`
}
type Delivery struct {
	ID          string     `json:"id"`
	EventType   string     `json:"event_type"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	CreatedAt   time.Time  `json:"created_at"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	NextRetryAt *time.Time `json:"next_retry_at,omitempty"`
	StatusCode  int        `json:"status_code"`
}
type DeliveryList struct {
	Items []Delivery `json:"items"`
}
type OK struct {
	OK bool `json:"ok"`
}

func (s *Service) emailPreference(ctx context.Context, p security.Principal) (bool, error) {
	var enabled bool
	err := s.pg.QueryRow(ctx, `SELECT coalesce((SELECT deployment_email FROM loomplane_notification_preferences WHERE org_id=$1 AND user_id=$2),true)`, p.OrgID, p.UserID).Scan(&enabled)
	return enabled, err
}
func (s *Service) Preferences(ctx context.Context, p security.Principal) (Preferences, error) {
	if err := s.member(ctx, p); err != nil {
		return Preferences{}, err
	}
	enabled, err := s.emailPreference(ctx, p)
	return Preferences{DeploymentEmail: enabled, MailEnabled: s.MailEnabled()}, err
}
func (s *Service) SetPreferences(ctx context.Context, p security.Principal, in PreferenceInput) (Preferences, error) {
	if p.ReadOnly {
		return Preferences{}, security.ErrForbidden
	}
	if err := s.member(ctx, p); err != nil {
		return Preferences{}, err
	}
	_, err := s.pg.Exec(ctx, `INSERT INTO loomplane_notification_preferences(org_id,user_id,deployment_email)VALUES($1,$2,$3) ON CONFLICT(org_id,user_id)DO UPDATE SET deployment_email=excluded.deployment_email,updated_at=clock_timestamp()`, p.OrgID, p.UserID, in.DeploymentEmail)
	return Preferences{DeploymentEmail: in.DeploymentEmail, MailEnabled: s.MailEnabled()}, err
}
func (s *Service) List(ctx context.Context, p security.Principal) (NotificationList, error) {
	out := NotificationList{Items: []Notification{}, MailEnabled: s.MailEnabled()}
	if err := s.member(ctx, p); err != nil {
		return out, err
	}
	// The inbox is user-owned in Herald. Join its deployment outbox to scope
	// history before paging, then recheck current project grants before exposure.
	rows, err := s.pg.Query(ctx, `SELECT i.id FROM herald_inbox i JOIN loomplane_notification_outbox o ON o.id=i.metadata->>'outbox_id' WHERE i.app_id=$1 AND i.user_id=$2 AND o.org_id=$3 ORDER BY i.created_at DESC,i.id DESC LIMIT 200`, s.cfg.Identity.AppID, p.UserID, p.OrgID)
	if err != nil {
		return out, err
	}
	ids := []string{}
	for rows.Next() {
		var item string
		if err = rows.Scan(&item); err != nil {
			_ = rows.Close()
			return out, err
		}
		ids = append(ids, item)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return out, err
	}
	for _, value := range ids {
		parsed, err := id.ParseInboxID(value)
		if err != nil {
			return out, err
		}
		n, err := s.mailStore.GetNotification(ctx, parsed)
		if err != nil {
			return out, err
		}
		if !domainNotice(n) && !s.projectExists(ctx, p.OrgID, n.Metadata["project_id"]) {
			continue
		}
		if err = s.requireReader(ctx, p, n); errors.Is(err, security.ErrForbidden) {
			continue
		} else if err != nil {
			return out, err
		}
		var emailState string
		if err := s.pg.QueryRow(ctx, `SELECT email_state FROM loomplane_notification_outbox WHERE id=$1 AND org_id=$2`, n.Metadata["outbox_id"], p.OrgID).Scan(&emailState); err != nil {
			return out, err
		}
		out.Items = append(out.Items, Notification{ID: n.ID.String(), Title: n.Title, Body: n.Body, ActionURL: n.ActionURL, Read: n.Read, CreatedAt: n.CreatedAt, ProjectID: n.Metadata["project_id"], Status: n.Metadata["status"], EmailStatus: emailState})
		if len(out.Items) == 50 {
			break
		}
	}
	return out, nil
}
func (s *Service) MarkRead(ctx context.Context, p security.Principal, value string) error {
	if p.ReadOnly {
		return security.ErrForbidden
	}
	if err := s.member(ctx, p); err != nil {
		return err
	}
	parsed, err := id.ParseInboxID(value)
	if err != nil {
		return ErrNotFound
	}
	n, err := s.mailStore.GetNotification(ctx, parsed)
	if err != nil {
		return ErrNotFound
	}
	if n.AppID != s.cfg.Identity.AppID || n.UserID != p.UserID || n.Metadata["org_id"] != p.OrgID || (!domainNotice(n) && !s.projectExists(ctx, p.OrgID, n.Metadata["project_id"])) {
		return ErrNotFound
	}
	if err := s.requireReader(ctx, p, n); err != nil {
		return err
	}
	return s.mailStore.MarkRead(ctx, parsed)
}

// domainNotice is a renewal or registration notice, which belongs to the
// organization rather than a project.
func domainNotice(n *inbox.Notification) bool {
	return n.Metadata["kind"] == kindRenewal || n.Metadata["kind"] == kindRegistration
}

// requireReader rechecks the grant the notification was sent under.
func (s *Service) requireReader(ctx context.Context, p security.Principal, n *inbox.Notification) error {
	if domainNotice(n) {
		return s.require(ctx, p, security.OrganizationCloudManage, p.OrgID)
	}
	return s.require(ctx, p, security.ProjectRead, n.Metadata["project_id"])
}
func publicEndpoint(ep *endpoint.Endpoint) WebhookEndpoint {
	return WebhookEndpoint{ID: ep.ID.String(), URL: ep.URL, Description: ep.Description, Enabled: ep.Enabled, CreatedAt: ep.CreatedAt}
}
func (s *Service) ListEndpoints(ctx context.Context, p security.Principal) (WebhookEndpointList, error) {
	out := WebhookEndpointList{Items: []WebhookEndpoint{}}
	if err := s.require(ctx, p, security.MemberManage, ""); err != nil {
		return out, err
	}
	list, err := s.relay.Endpoints().List(ctx, p.OrgID, endpoint.ListOpts{Limit: 100})
	if err != nil {
		return out, err
	}
	for _, ep := range list {
		out.Items = append(out.Items, publicEndpoint(ep))
	}
	return out, nil
}
func (s *Service) CreateEndpoint(ctx context.Context, p security.Principal, in WebhookEndpointInput) (WebhookEndpointSecret, error) {
	if err := s.require(ctx, p, security.MemberManage, ""); err != nil {
		return WebhookEndpointSecret{}, err
	}
	if err := validateWebhookURL(in.URL); err != nil {
		return WebhookEndpointSecret{}, err
	}
	if len(in.Description) > 500 || strings.ContainsAny(in.Description, "\r\n") {
		return WebhookEndpointSecret{}, ErrInvalid
	}
	u, err := url.Parse(in.URL)
	if err != nil {
		return WebhookEndpointSecret{}, ErrInvalid
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname())
	if err != nil || len(ips) == 0 {
		return WebhookEndpointSecret{}, ErrInvalid
	}
	for _, ip := range ips {
		if !publicAddress(ip) {
			return WebhookEndpointSecret{}, ErrInvalid
		}
	}
	var count int
	if err := s.pg.QueryRow(ctx, `SELECT count(*) FROM relay_endpoints WHERE tenant_id=$1`, p.OrgID).Scan(&count); err != nil {
		return WebhookEndpointSecret{}, err
	}
	if count >= 100 {
		return WebhookEndpointSecret{}, ErrInvalid
	}
	ep := &endpoint.Endpoint{ID: relayid.NewEndpointID(), TenantID: p.OrgID, ScopeAppID: s.cfg.Identity.AppID, ScopeOrgID: p.OrgID, URL: in.URL, Description: in.Description, Secret: signature.GenerateSecret(), EventTypes: []string{"deployment.succeeded", "deployment.failed"}, RateLimit: 5, Enabled: true}
	ep.CreatedAt = time.Now().UTC()
	ep.UpdatedAt = ep.CreatedAt
	if err := s.relayStore.CreateEndpoint(ctx, ep); err != nil {
		return WebhookEndpointSecret{}, err
	}
	s.recordAudit(ctx, p, "notification.webhook.create", ep.ID.String())
	return WebhookEndpointSecret{Endpoint: publicEndpoint(ep), Secret: ep.Secret}, nil
}
func (s *Service) endpoint(ctx context.Context, p security.Principal, value string) (*endpoint.Endpoint, error) {
	if err := s.require(ctx, p, security.MemberManage, ""); err != nil {
		return nil, err
	}
	parsed, err := relayid.ParseEndpointID(value)
	if err != nil {
		return nil, ErrNotFound
	}
	ep, err := s.relay.Endpoints().Get(ctx, parsed)
	if err != nil || ep.TenantID != p.OrgID {
		return nil, ErrNotFound
	}
	return ep, nil
}
func (s *Service) RotateEndpoint(ctx context.Context, p security.Principal, value string) (WebhookEndpointSecret, error) {
	ep, err := s.endpoint(ctx, p, value)
	if err != nil {
		return WebhookEndpointSecret{}, err
	}
	secret, err := s.relay.Endpoints().RotateSecret(ctx, ep.ID)
	if err != nil {
		return WebhookEndpointSecret{}, err
	}
	s.recordAudit(ctx, p, "notification.webhook.rotate", value)
	return WebhookEndpointSecret{Endpoint: publicEndpoint(ep), Secret: secret}, nil
}
func (s *Service) DeleteEndpoint(ctx context.Context, p security.Principal, value string) error {
	ep, err := s.endpoint(ctx, p, value)
	if err != nil {
		return err
	}
	if err := s.relay.Endpoints().Delete(ctx, ep.ID); err != nil {
		return err
	}
	s.recordAudit(ctx, p, "notification.webhook.delete", value)
	return nil
}
func (s *Service) Deliveries(ctx context.Context, p security.Principal, value string) (DeliveryList, error) {
	out := DeliveryList{Items: []Delivery{}}
	ep, err := s.endpoint(ctx, p, value)
	if err != nil {
		return out, err
	}
	items, err := s.relayStore.ListByEndpoint(ctx, ep.ID, delivery.ListOpts{Limit: 50})
	if err != nil {
		return out, err
	}
	for _, d := range items {
		if d.TenantID != p.OrgID {
			continue
		}
		item := Delivery{ID: d.ID.String(), EventType: d.EventType, Status: string(d.State), Attempts: d.AttemptCount, CreatedAt: d.CreatedAt, StatusCode: d.LastStatusCode}
		if d.State == delivery.StateDelivered {
			item.DeliveredAt = d.CompletedAt
		}
		if d.State == delivery.StatePending {
			next := d.NextAttemptAt
			item.NextRetryAt = &next
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// RetryExhausted preserves per-channel completion flags and stable Relay
// acceptance identity. It retries only this organization's bounded failed set.
func (s *Service) RetryExhausted(ctx context.Context, p security.Principal) error {
	if err := s.require(ctx, p, security.MemberManage, ""); err != nil {
		return err
	}
	_, err := s.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET attempts=0,next_attempt_at=clock_timestamp(),email_state=CASE WHEN email_state='failed' THEN 'pending' ELSE email_state END,last_error='' WHERE id IN(SELECT id FROM loomplane_notification_outbox WHERE org_id=$1 AND completed_at IS NULL AND attempts>=10 AND (lease_until IS NULL OR lease_until<clock_timestamp()) ORDER BY created_at LIMIT 100)`, p.OrgID)
	if err == nil {
		s.recordAudit(ctx, p, "notification.retry", p.OrgID)
	}
	return err
}
