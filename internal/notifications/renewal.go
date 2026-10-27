package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/xraph/relay/catalog"
)

const (
	kindDeployment   = "deployment"
	kindRenewal      = "domain.renewal"
	kindRegistration = "domain.registration"
)

// registrationStatuses are the final registration statuses that notify.
var registrationStatuses = []string{"succeeded", "failed", "action_required", "blocked", "price_changed", "unconfirmed"}

// RenewalNotice says a registered domain renews soon. An empty Currency or
// RenewalCost means the registrar reported no price.
type RenewalNotice struct {
	OrgID, RegistrationID, Domain string
	RenewsOn                      time.Time
	Currency, RenewalCost         string
}

// RegistrationResultNotice says a registration ended. Message is the
// registration's message; for action_required and blocked it is the
// Cloudflare dashboard link.
type RegistrationResultNotice struct {
	OrgID, RegistrationID, Domain, Status, Message string
}

type renewalPayload struct {
	RegistrationID string `json:"registration_id"`
	Domain         string `json:"domain"`
	RenewsOn       string `json:"renews_on"`
	Currency       string `json:"currency,omitempty"`
	RenewalCost    string `json:"renewal_cost,omitempty"`
	URL            string `json:"url,omitempty"`
}

// registrationPayload keeps the message for the inbox and email. The
// webhook gets registrationWire, without it.
type registrationPayload struct {
	RegistrationID string `json:"registration_id"`
	Domain         string `json:"domain"`
	Status         string `json:"status"`
	Message        string `json:"message,omitempty"`
}

type registrationWire struct {
	RegistrationID string `json:"registration_id"`
	Domain         string `json:"domain"`
	Status         string `json:"status"`
	URL            string `json:"url"`
}

func renewalKey(n RenewalNotice) string {
	return "renewal:" + n.RegistrationID + ":" + n.RenewsOn.UTC().Format("2006-01-02")
}

func registrationKey(n RegistrationResultNotice) string {
	return "registration:" + n.RegistrationID + ":" + n.Status
}

// EnqueueRenewalNotice queues the notice for every member of the
// organization. Whether each one may see it is checked at delivery. Sending
// the same notice again adds nothing.
func (s *Service) EnqueueRenewalNotice(ctx context.Context, n RenewalNotice) error {
	payload, err := json.Marshal(renewalPayload{RegistrationID: n.RegistrationID, Domain: n.Domain, RenewsOn: n.RenewsOn.UTC().Format("2006-01-02"), Currency: n.Currency, RenewalCost: n.RenewalCost})
	if err != nil {
		return err
	}
	return s.enqueueMembers(ctx, n.OrgID, renewalKey(n), kindRenewal, payload)
}

// EnqueueRegistrationResult queues a registration's outcome like a renewal
// notice, once per status.
func (s *Service) EnqueueRegistrationResult(ctx context.Context, n RegistrationResultNotice) error {
	payload, err := json.Marshal(registrationPayload{RegistrationID: n.RegistrationID, Domain: n.Domain, Status: n.Status, Message: n.Message})
	if err != nil {
		return err
	}
	return s.enqueueMembers(ctx, n.OrgID, registrationKey(n), kindRegistration, payload)
}

func (s *Service) enqueueMembers(ctx context.Context, org, key, kind string, payload []byte) error {
	_, err := s.pg.Exec(ctx, `INSERT INTO loomplane_notification_outbox(id,org_id,project_id,user_id,deployment_id,attempt,payload,kind)
 SELECT DISTINCT $1::text||':'||m.user_id,m.org_id,'',m.user_id,$1::text||':'||m.user_id,0,$3::jsonb,$4::text FROM authsome_members m JOIN authsome_users u ON u.id=m.user_id
 WHERE m.org_id=$2 AND u.app_id=$5 AND u.deleted_at IS NULL AND NOT u.banned ON CONFLICT DO NOTHING`, key, org, string(payload), kind, s.cfg.Identity.AppID)
	return err
}

func (s *Service) registerEventTypes(ctx context.Context) error {
	definitions := make([]catalog.WebhookDefinition, 0, 4+len(registrationStatuses))
	for _, status := range []string{"succeeded", "failed"} {
		definitions = append(definitions, catalog.WebhookDefinition{Name: "deployment." + status, Description: "A deployment attempt completed", Group: "deployment", Version: "2026-10-18"})
	}
	definitions = append(definitions,
		catalog.WebhookDefinition{Name: connectionSignInNeeded, Description: "A cloud connection needs signing in again", Group: "connection", Version: "2026-10-25"},
		catalog.WebhookDefinition{Name: "domain.renewal_upcoming", Description: "A registered domain renews soon", Group: "domain", Version: "2026-11-05"})
	for _, status := range registrationStatuses {
		definitions = append(definitions, catalog.WebhookDefinition{Name: "domain.registration_" + status, Description: "A domain registration ended", Group: "domain", Version: "2026-11-05"})
	}
	for _, definition := range definitions {
		if _, err := s.relay.RegisterEventType(ctx, definition); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) domainsURL(registration string) string {
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/domains?registration=" + url.QueryEscape(registration)
}

func renewalCopy(domain string, renewsOn time.Time, currency, cost string) (title, body, subject string) {
	date := renewsOn.UTC().Format("Jan 2, 2006")
	title = domain + " renews on " + date
	if currency != "" && cost != "" {
		body = "Cloudflare renews " + domain + " on " + date + " for " + currency + " " + cost + ". Turn auto-renew off before then if you don't want to keep it."
	} else {
		body = "Cloudflare renews " + domain + " on " + date + ". Loomplane doesn't know the renewal price. Turn auto-renew off before then if you don't want to keep it."
	}
	return title, body, "Loomplane: " + title
}

// registrationCopy matches the registration's line on the Domains page.
func registrationCopy(domain, status, message string) (title, body string) {
	if status == "succeeded" {
		return domain + " is registered", domain + " is registered in your Cloudflare account, and Loomplane manages its DNS."
	}
	title = domain + " wasn't registered"
	switch status {
	case "price_changed":
		body = "The price changed before purchase. Nothing was charged. Register it again to see the new price."
	case "failed":
		body = "Cloudflare declined the registration: " + message
	case "action_required", "blocked":
		body = "Finish this in your Cloudflare dashboard. " + message
	default:
		body = message
	}
	return title, body
}

// domainWire is what a domain notice's webhook carries, fixed on first
// delivery like a deployment's.
func (s *Service) domainWire(item *outboxItem) ([]byte, error) {
	switch item.kind {
	case kindRenewal:
		var p renewalPayload
		if err := json.Unmarshal(item.payload, &p); err != nil {
			return nil, err
		}
		p.URL = s.domainsURL(p.RegistrationID) + "&action=auto-renew-off"
		return json.Marshal(p)
	case kindRegistration:
		var p registrationPayload
		if err := json.Unmarshal(item.payload, &p); err != nil {
			return nil, err
		}
		return json.Marshal(registrationWire{RegistrationID: p.RegistrationID, Domain: p.Domain, Status: p.Status, URL: s.domainsURL(p.RegistrationID)})
	}
	return nil, fmt.Errorf("unknown notification kind %q", item.kind)
}

func domainMessage(item *outboxItem) (*outboxMessage, error) {
	meta := map[string]string{"org_id": item.org, "project_id": "", "kind": item.kind, "outbox_id": item.id}
	if item.kind == kindRenewal {
		var p renewalPayload
		if err := json.Unmarshal(item.wire, &p); err != nil {
			return nil, err
		}
		renews, err := time.Parse("2006-01-02", p.RenewsOn)
		if err != nil {
			return nil, err
		}
		title, body, subject := renewalCopy(p.Domain, renews, p.Currency, p.RenewalCost)
		meta["status"], meta["registration_id"] = "renewal_upcoming", p.RegistrationID
		return &outboxMessage{sourceKey: renewalKey(RenewalNotice{RegistrationID: p.RegistrationID, RenewsOn: renews}), event: "domain.renewal_upcoming", inboxType: kindRenewal,
			title: title, body: body, actionURL: p.URL, metadata: meta, subject: subject, emailBody: body + "\n\nTurn auto-renew off: " + p.URL, alwaysEmail: true}, nil
	}
	if item.kind != kindRegistration {
		return nil, fmt.Errorf("unknown notification kind %q", item.kind)
	}
	var wire registrationWire
	if err := json.Unmarshal(item.wire, &wire); err != nil {
		return nil, err
	}
	var p registrationPayload
	if err := json.Unmarshal(item.payload, &p); err != nil {
		return nil, err
	}
	title, body := registrationCopy(wire.Domain, wire.Status, p.Message)
	meta["status"], meta["registration_id"] = wire.Status, wire.RegistrationID
	return &outboxMessage{sourceKey: registrationKey(RegistrationResultNotice{RegistrationID: wire.RegistrationID, Status: wire.Status}), event: "domain.registration_" + wire.Status, inboxType: kindRegistration,
		title: title, body: body, actionURL: wire.URL, metadata: meta, subject: "Loomplane: " + title, emailBody: body + "\n\nReview the registration: " + wire.URL, alwaysEmail: true}, nil
}
