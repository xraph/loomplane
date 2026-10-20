package notifications

import (
	"context"
	"fmt"
	"maps"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xraph/authsome/bridge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/herald"
	emaildriver "github.com/xraph/herald/driver/email"
	"github.com/xraph/herald/message"
	"github.com/xraph/herald/provider"
	heraldstore "github.com/xraph/herald/store"
	heraldpg "github.com/xraph/herald/store/postgres"
	"github.com/xraph/relay"
	"github.com/xraph/relay/catalog"
	relaypg "github.com/xraph/relay/store/postgres"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/security"
)

// Authorizer is shared with ordinary project requests. Membership is checked
// afresh before delayed notifications, so removed users receive no new mail.
type Authorizer interface {
	Require(context.Context, security.Principal, security.Permission, string) error
}

type Service struct {
	cfg        config.Config
	pg         *pgdriver.PgDB
	herald     *herald.Herald
	mailStore  heraldstore.Store
	relay      *relay.Relay
	relayStore *encryptedRelayStore
	authz      Authorizer
	audit      func(context.Context, security.Principal, string, string)
	statusMu   sync.RWMutex
	workerErr  error
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

func New(cfg config.Config, db *grove.DB) (*Service, error) {
	mailKey, err := security.DeriveKey(cfg.SecretKey, "loomplane/herald credentials v1")
	if err != nil {
		return nil, err
	}
	hookKey, err := security.DeriveKey(cfg.SecretKey, "loomplane/relay signing secrets v1")
	if err != nil {
		return nil, err
	}
	ms := privateHeraldStore{Store: heraldpg.New(db)}
	h, err := herald.New(herald.WithStore(ms), herald.WithCredentialKey("lp1", mailKey), herald.WithDriver(&emaildriver.SMTPDriver{}))
	if err != nil {
		return nil, err
	}
	rs, err := newRelayStore(relaypg.New(db), hookKey)
	if err != nil {
		return nil, err
	}
	r, err := relay.New(relay.WithStore(rs), relay.WithHTTPClient(webhookClient()), relay.WithConcurrency(2), relay.WithBatchSize(10), relay.WithPollInterval(time.Second))
	if err != nil {
		return nil, err
	}
	return &Service{cfg: cfg, pg: pgdriver.Unwrap(db), herald: h, mailStore: ms, relay: r, relayStore: rs}, nil
}
func (s *Service) SetAuthorizer(a Authorizer) { s.authz = a }
func (s *Service) MailEnabled() bool          { return s.cfg.Notifications.MailEnabled() }

// Prepare is called after central migrations and before Authsome starts. It
// configures exactly one application-owned SMTP provider and seeds templates.
func (s *Service) Prepare(ctx context.Context) error {
	if err := s.herald.SeedDefaultTemplates(ctx, s.cfg.Identity.AppID); err != nil {
		return err
	}
	if !s.MailEnabled() {
		return nil
	}
	n := s.cfg.Notifications
	creds := map[string]string{"username": n.SMTPUsername, "password": n.SMTPPassword}
	settings := map[string]string{"host": n.SMTPHost, "port": strconv.Itoa(n.SMTPPort), "use_tls": strconv.FormatBool(n.SMTPTLS), "from": n.From}
	list, err := s.mailStore.ListAllProviders(ctx, s.cfg.Identity.AppID)
	if err != nil {
		return err
	}
	for _, p := range list {
		if p.Name == "loomplane-smtp" {
			enabled := true
			_, err = s.herald.UpdateProvider(ctx, s.cfg.Identity.AppID, p.ID, herald.ProviderUpdate{SetCredentials: creds, SetSettings: settings, Enabled: &enabled})
			return err
		}
	}
	return s.herald.CreateProvider(ctx, &provider.Provider{AppID: s.cfg.Identity.AppID, Name: "loomplane-smtp", Channel: "email", Driver: "smtp", Credentials: creds, Settings: settings, Enabled: true})
}
func (s *Service) Run(ctx context.Context) error {
	for _, status := range []string{"succeeded", "failed"} {
		if _, err := s.relay.RegisterEventType(ctx, catalog.WebhookDefinition{Name: "deployment." + status, Description: "A deployment attempt completed", Group: "deployment", Version: "2026-10-18"}); err != nil {
			return err
		}
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.relay.Start(ctx)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			err := s.drain(ctx)
			s.statusMu.Lock()
			s.workerErr = err
			s.statusMu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}
func (s *Service) Shutdown(ctx context.Context) error {
	if s.cancel != nil {
		s.cancel()
		s.wg.Wait()
	}
	s.relay.Stop(ctx)
	return nil
}
func (s *Service) Health(ctx context.Context) error {
	if err := s.mailStore.Ping(ctx); err != nil {
		return fmt.Errorf("notification storage unavailable")
	}
	s.statusMu.RLock()
	err := s.workerErr
	s.statusMu.RUnlock()
	if err != nil {
		return fmt.Errorf("notification outbox processing failed")
	}
	var failed int
	if err = s.pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_notification_outbox WHERE completed_at IS NULL AND attempts>=10`).Scan(&failed); err != nil {
		return fmt.Errorf("notification outbox status unavailable")
	}
	if failed > 0 {
		return fmt.Errorf("%d notification deliveries exhausted retries", failed)
	}
	return nil
}
func (s *Service) SetAudit(fn func(context.Context, security.Principal, string, string)) {
	s.audit = fn
}
func (s *Service) recordAudit(ctx context.Context, p security.Principal, action, id string) {
	if s.audit != nil {
		s.audit(ctx, p, action, id)
	}
}
func (s *Service) AuthBridge() bridge.Herald { return authBridge{s: s} }

type authBridge struct{ s *Service }

func (b authBridge) Send(ctx context.Context, req *bridge.HeraldSendRequest) error {
	if !b.s.MailEnabled() {
		return ErrMailDisabled
	}
	if req == nil || req.Channel != "email" || !strings.HasPrefix(req.Template, "auth.") || (req.AppID != "" && req.AppID != b.s.cfg.Identity.AppID) {
		return ErrInvalid
	}
	for _, to := range req.To {
		address, err := mail.ParseAddress(to)
		if err != nil || address.Address != to || strings.ContainsAny(to, "\r\n") {
			return ErrInvalid
		}
	}
	data := maps.Clone(req.Data)
	if data == nil {
		data = map[string]any{}
	}
	if data["app_name"] == nil {
		data["app_name"] = "Loomplane"
	}
	if data["user_name"] == nil {
		data["user_name"] = "there"
	}
	// Account security is independent of the deployment email preference.
	res, err := b.s.herald.Send(ctx, &herald.SendRequest{AppID: b.s.cfg.Identity.AppID, OrgID: req.OrgID, Channel: "email", Template: req.Template, Locale: req.Locale, To: req.To, Data: data})
	return deliveryError(res, err)
}
func (b authBridge) Notify(ctx context.Context, req *bridge.HeraldNotifyRequest) error {
	if req == nil {
		return ErrInvalid
	}
	for _, channel := range req.Channels {
		if channel != "email" {
			return ErrInvalid
		}
	}
	return b.Send(ctx, &bridge.HeraldSendRequest{AppID: req.AppID, OrgID: req.OrgID, Channel: "email", Template: req.Template, Locale: req.Locale, To: req.To, Data: req.Data})
}
func deliveryError(result *herald.SendResult, err error) error {
	if err != nil || result == nil || (result.Status != message.StatusSent && result.Status != message.StatusDelivered) {
		return ErrDeliveryFailed
	}
	return nil
}
func (s *Service) SendInvitation(ctx context.Context, orgID, email, link, orgName, role string) error {
	base, err := url.Parse(s.cfg.PublicURL)
	if err != nil {
		return ErrInvalid
	}
	dest, err := url.Parse(link)
	if err != nil || dest.Scheme != base.Scheme || dest.Host != base.Host || dest.User != nil {
		return ErrInvalid
	}
	return s.AuthBridge().Send(ctx, &bridge.HeraldSendRequest{OrgID: orgID, Channel: "email", Template: "auth.invitation", To: []string{email}, Data: map[string]any{"inviter_name": "Your teammate", "org_name": orgName, "app_name": "Loomplane", "accept_url": link, "role": role}})
}
func (s *Service) member(ctx context.Context, p security.Principal) error {
	if p.OrgID == "" || p.UserID == "" {
		return security.ErrUnauthenticated
	}
	var exists bool
	err := s.pg.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authsome_members m JOIN authsome_users u ON u.id=m.user_id WHERE m.org_id=$1 AND m.user_id=$2 AND u.app_id=$3 AND u.deleted_at IS NULL AND NOT u.banned)`, p.OrgID, p.UserID, s.cfg.Identity.AppID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return security.ErrNotMember
	}
	return nil
}
func (s *Service) require(ctx context.Context, p security.Principal, permission security.Permission, resource string) error {
	if err := s.member(ctx, p); err != nil {
		return err
	}
	if s.authz == nil {
		return fmt.Errorf("notifications authorization is unavailable")
	}
	return s.authz.Require(ctx, p, permission, resource)
}
func (s *Service) projectExists(ctx context.Context, org, id string) bool {
	var exists bool
	err := s.pg.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_projects WHERE org_id=$1 AND id=$2)`, org, id).Scan(&exists)
	return err == nil && exists
}

// RetryDelay bounds mail acceptance retries; Relay owns HTTP delivery retries.
func retryDelay(attempt int) time.Duration {
	if attempt < 0 {
		return 30 * time.Second
	}
	if attempt > 7 {
		return time.Hour
	}
	d := 30 * time.Second * time.Duration(1<<attempt)
	if d > time.Hour {
		return time.Hour
	}
	return d
}

var _ bridge.Herald = authBridge{}
