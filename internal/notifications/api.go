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

// NotificationCreated is the stream event that says a new inbox item exists.
// It carries no text: the app refetches the list.
type NotificationCreated struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
}
type NotificationList struct {
	Items       []Notification `json:"items"`
	MailEnabled bool           `json:"mail_enabled"`
}
type Preferences struct {
	DeploymentEmail bool `json:"deployment_email"`
	AgentEmail      bool `json:"agent_email"`
	MailEnabled     bool `json:"mail_enabled"`
}

// PreferenceInput changes the fields it carries. A nil field keeps its
// value, so sending one switch never turns the other off.
type PreferenceInput struct {
	DeploymentEmail *bool `json:"deployment_email,omitempty"`
	AgentEmail      *bool `json:"agent_email,omitempty"`
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

// emailPreference reports whether the person gets email for a notice of
// this kind: the agent's notices have a switch of their own.
func (s *Service) emailPreference(ctx context.Context, p security.Principal, kind string) (bool, error) {
	column := "deployment_email"
	if strings.HasPrefix(kind, "agent.") {
		column = "agent_email"
	}
	var enabled bool
	err := s.pg.QueryRow(ctx, `SELECT coalesce((SELECT `+column+` FROM loomplane_notification_preferences WHERE org_id=$1 AND user_id=$2),true)`, p.OrgID, p.UserID).Scan(&enabled)
	return enabled, err
}
func (s *Service) Preferences(ctx context.Context, p security.Principal) (Preferences, error) {
	if err := s.member(ctx, p); err != nil {
		return Preferences{}, err
	}
	out := Preferences{MailEnabled: s.MailEnabled()}
	err := s.pg.QueryRow(ctx, `SELECT coalesce((SELECT deployment_email FROM loomplane_notification_preferences WHERE org_id=$1 AND user_id=$2),true),coalesce((SELECT agent_email FROM loomplane_notification_preferences WHERE org_id=$1 AND user_id=$2),true)`, p.OrgID, p.UserID).Scan(&out.DeploymentEmail, &out.AgentEmail)
	return out, err
}
func (s *Service) SetPreferences(ctx context.Context, p security.Principal, in PreferenceInput) (Preferences, error) {
	if p.ReadOnly {
		return Preferences{}, security.ErrForbidden
	}
	if err := s.member(ctx, p); err != nil {
		return Preferences{}, err
	}
	out := Preferences{MailEnabled: s.MailEnabled()}
	err := s.pg.QueryRow(ctx, `INSERT INTO loomplane_notification_preferences(org_id,user_id,deployment_email,agent_email)VALUES($1,$2,coalesce($3::boolean,true),coalesce($4::boolean,true)) ON CONFLICT(org_id,user_id)DO UPDATE SET deployment_email=coalesce($3::boolean,loomplane_notification_preferences.deployment_email),agent_email=coalesce($4::boolean,loomplane_notification_preferences.agent_email),updated_at=clock_timestamp() RETURNING deployment_email,agent_email`, p.OrgID, p.UserID, in.DeploymentEmail, in.AgentEmail).Scan(&out.DeploymentEmail, &out.AgentEmail)
	return out, err
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
		if !orgNotice(n) && !s.projectExists(ctx, p.OrgID, n.Metadata["project_id"]) {
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
	if n.AppID != s.cfg.Identity.AppID || n.UserID != p.UserID || n.Metadata["org_id"] != p.OrgID || (!orgNotice(n) && !s.projectExists(ctx, p.OrgID, n.Metadata["project_id"])) {
		return ErrNotFound
	}
	if err := s.requireReader(ctx, p, n); err != nil {
		return err
	}
	if err := s.mailStore.MarkRead(ctx, parsed); err != nil {
		return err
	}
	s.withdrawRead(ctx, p.UserID, n.Metadata)
	return nil
}

// domainNotice is a renewal or registration notice, which belongs to the
// organization rather than a project.
func domainNotice(n *inbox.Notification) bool {
	return n.Metadata["kind"] == kindRenewal || n.Metadata["kind"] == kindRegistration
}

// genericNotice is a notice queued with Enqueue. Its inbox item names a
// subject, which no other kind's does.
func genericNotice(n *inbox.Notification) bool { return n.Metadata["subject_id"] != "" }

// orgNotice is a notice that belongs to the organization rather than a
// project: a connection notice, a domain notice or a workspace-wide one
// queued with Enqueue.
func orgNotice(n *inbox.Notification) bool {
	return n.Metadata["kind"] == kindConnection || domainNotice(n) || (genericNotice(n) && n.Metadata["project_id"] == "")
}

// requireReader rechecks the grant the notification was sent under. A
// connection notice went to its connection's maker and the organization's
// owners and admins, so it needs no further grant. A model's budget
// notice needs ai:manage.
func (s *Service) requireReader(ctx context.Context, p security.Principal, n *inbox.Notification) error {
	if n.Metadata["kind"] == kindConnection {
		return nil
	}
	if domainNotice(n) {
		return s.require(ctx, p, security.OrganizationCloudManage, p.OrgID)
	}
	if n.Metadata["kind"] == kindModelBudget {
		// A model's budget notice went to admins only.
		return s.require(ctx, p, security.AIManage, "")
	}
	if genericNotice(n) {
		// Project read when it has a project, then the permission it
		// was sent under.
		if project := n.Metadata["project_id"]; project != "" {
			if err := s.require(ctx, p, security.ProjectRead, project); err != nil {
				return err
			}
		}
		need, err := parseNeed(n.Metadata["need"])
		if err != nil {
			return err
		}
		if need == nil {
			return s.member(ctx, p)
		}
		return s.require(ctx, p, *need, "")
	}
	return s.require(ctx, p, security.ProjectRead, n.Metadata["project_id"])
}

// webhookEventTypes is every event a webhook endpoint hears. Endpoints made
// before connection.sign_in_needed existed get it from the
// cloud_sign_in_webhooks migration, connection.keyless_refused from the
// keyless one, and the model budget events from ai_model_budget_webhooks.
func webhookEventTypes() []string {
	return []string{"deployment.succeeded", "deployment.failed", connectionSignInNeeded, connectionKeylessRefused,
		"model.budget_" + ModelBudgetProjected, "model.budget_" + ModelBudgetCrossed}
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
	ep := &endpoint.Endpoint{ID: relayid.NewEndpointID(), TenantID: p.OrgID, ScopeAppID: s.cfg.Identity.AppID, ScopeOrgID: p.OrgID, URL: in.URL, Description: in.Description, Secret: signature.GenerateSecret(), EventTypes: webhookEventTypes(), RateLimit: 5, Enabled: true}
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
// acceptance identity. It retries only this organization's bounded failed set,
// and a push older than its TTL is skipped, not sent late.
func (s *Service) RetryExhausted(ctx context.Context, p security.Principal) error {
	if err := s.require(ctx, p, security.MemberManage, ""); err != nil {
		return err
	}
	_, err := s.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET attempts=0,next_attempt_at=clock_timestamp(),email_state=CASE WHEN email_state='failed' THEN 'pending' ELSE email_state END,push_state=CASE WHEN push_state<>'failed' THEN push_state WHEN created_at>clock_timestamp()-(CASE WHEN kind LIKE 'plan.%' THEN $2::int WHEN kind='deployment' OR kind LIKE 'release.%' THEN $3::int ELSE COALESCE(NULLIF((payload->'push'->>'ttl_seconds')::int,0),$4::int) END * interval '1 second') THEN 'pending' ELSE 'skipped' END,last_error='' WHERE id IN(SELECT id FROM loomplane_notification_outbox WHERE org_id=$1 AND completed_at IS NULL AND attempts>=10 AND (lease_until IS NULL OR lease_until<clock_timestamp()) ORDER BY created_at LIMIT 100)`, p.OrgID, planPushTTL, failedDeploymentTTL, workspaceTTL)
	if err == nil {
		s.recordAudit(ctx, p, "notification.retry", p.OrgID)
	}
	return err
}
