package cloudinfra

import (
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// Registration is one domain bought through the organization's own
// registrar account.
type Registration struct {
	ID                 string               `json:"id" forge:"id"`
	OrgID              string               `json:"org_id"`
	ConnectionID       string               `json:"connection_id"`
	Provider           string               `json:"provider"`
	AccountID          string               `json:"account_id"`
	Name               string               `json:"name"`
	AutoRenew          bool                 `json:"auto_renew"`
	PrivacyMode        string               `json:"privacy_mode"`
	CustomRegistrant   bool                 `json:"custom_registrant"`
	ProjectIDs         []string             `json:"project_ids"`
	Quote              provider.DomainPrice `json:"quote"`
	Status             string               `json:"status"`
	Message            string               `json:"message"`
	Review             string               `json:"review"`
	ReviewExpiresAt    time.Time            `json:"review_expires_at"`
	Attempt            int                  `json:"attempt"`
	Attempted          bool                 `json:"attempted"`
	FirstUnconfirmedAt time.Time            `json:"first_unconfirmed_at"`
	ZoneID             string               `json:"zone_id"`
	DomainExpiresAt    time.Time            `json:"domain_expires_at"`
	RefreshedAt        time.Time            `json:"refreshed_at"`
	NoticeSentFor      time.Time            `json:"notice_sent_for"`
	Sandbox            bool                 `json:"sandbox"`
	Revision           int64                `json:"revision"`
	CreatedAt          time.Time            `json:"created_at"`
	UpdatedAt          time.Time            `json:"updated_at"`
	Principal          security.Principal   `json:"-"`
}
