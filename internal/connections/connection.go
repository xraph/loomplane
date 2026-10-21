// Package connections keeps each organization's cloud connections: a
// Google Cloud service account or a Vercel access token that Loomplane
// deploys with. The credential itself lives in Vault, encrypted and
// write-only: the API never returns it, the audit trail never records
// it, and only a deployment reads it back.
package connections

import (
	"context"
	"errors"
	"time"
)

// Clouds a connection can be for. They're the clouds of the provider
// catalog (internal/providers), so a target's cloud names the connection
// it deploys with.
const (
	GCP          = "gcp"
	Vercel       = "vercel"
	AWS          = "aws"
	Render       = "render"
	DigitalOcean = "digitalocean"
	Server       = "server"
	Kubernetes   = "kubernetes"
	Registry     = "registry"
)

// Statuses. A connection is checked when it's made and whenever someone
// asks: ready when everything a deployment needs is allowed, incomplete
// when the credential works but lacks permissions, failed when the cloud
// refused it.
const (
	StatusReady      = "ready"
	StatusIncomplete = "incomplete"
	StatusFailed     = "failed"
)

// Errors.
var (
	ErrChanged  = errors.New("cloud connection changed during the operation")
	ErrNotFound = errors.New("cloud connection not found")
	ErrInvalid  = errors.New("invalid cloud connection")
	ErrExists   = errors.New("this organization already has a connection with that name")
	ErrInUse    = errors.New("this connection is used by a deployment or an active plan")
	// ErrNotReady is a deployment asking for a connection that isn't
	// ready.
	ErrNotReady = errors.New("the cloud connection isn't ready")
)

// Connection is one cloud account an organization deploys to.
type Connection struct {
	// Generation changes with credentials or verified placement configuration, not health checks.
	Generation int64  `json:"generation"`
	ID         string `json:"id" forge:"id"`
	OrgID      string `json:"org_id"`
	Cloud      string `json:"cloud"`
	Name       string `json:"name"`
	// Account is what the credential acts as: a Google Cloud project, or
	// a Vercel team or username.
	Account string `json:"account"`
	// AccountIdentity is derived from checked scope, never accepted from input
	// or persisted separately. Clients use it to compare reviewed plans.
	AccountIdentity string `json:"account_identity,omitempty"`
	// Details are facts about the credential that are safe to show, such
	// as a service account's email. Never the credential.
	Details map[string]string `json:"details"`
	Status  string            `json:"status"`
	// Missing lists the permissions an incomplete connection lacks.
	Missing []string `json:"missing"`
	// Message says why a connection failed its last check.
	Message   string     `json:"message"`
	CheckedAt *time.Time `json:"checked_at"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func (c *Connection) normalize() {
	if c.Generation == 0 {
		c.Generation = 1
	}
	c.AccountIdentity = AccountIdentity(c.Cloud, c.Account, c.Details)
	c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
	if c.CheckedAt != nil {
		t := c.CheckedAt.UTC()
		c.CheckedAt = &t
	}
	if c.Details == nil {
		c.Details = map[string]string{}
	}
	if c.Missing == nil {
		c.Missing = []string{}
	}
}

// RegistryPullReady permits an existing pinned image when only push is missing.
func RegistryPullReady(c *Connection) bool {
	if c == nil || c.Cloud != Registry || c.Message == replacementInProgressMessage {
		return false
	}
	if c.Status == StatusReady {
		return true
	}
	if c.Status != StatusIncomplete || c.Details["api"] != "ready" || c.Details["pull"] != "ready" || len(c.Missing) == 0 {
		return false
	}
	for _, missing := range c.Missing {
		if missing != "registry:push" {
			return false
		}
	}
	return true
}

// Store persists connections, scoped to an organization: another
// organization's read as not found.
type Store interface {
	// CreateConnection inserts c. A taken name is ErrExists.
	CreateConnection(ctx context.Context, c *Connection) error
	GetConnection(ctx context.Context, orgID, id string) (*Connection, error)
	ListConnections(ctx context.Context, orgID string) ([]*Connection, error)
	// UpdateConnection writes c's account, details and check result.
	UpdateConnection(ctx context.Context, c *Connection) error
	DeleteConnection(ctx context.Context, orgID, id string) error
}

// Secrets keeps credentials. *VaultSecrets is the real one.
type Secrets interface {
	Put(ctx context.Context, orgID, connectionID string, credential []byte) error
	Reveal(ctx context.Context, orgID, connectionID string) ([]byte, error)
	Delete(ctx context.Context, orgID, connectionID string) error
}

// Check is what checking a credential found.
type Check struct {
	Account string
	Details map[string]string
	// Missing are permissions the credential lacks.
	Missing []string
}

// Checker validates and checks credentials for one cloud.
type Checker interface {
	// Parse checks a credential's shape without calling the cloud, and
	// returns what's safe to show about it. settings are the cloud's own
	// options, such as a Vercel team.
	Parse(credential []byte, settings map[string]string) (Check, error)
	// Check asks the cloud what the credential may do. A credential the
	// cloud refuses is an error wrapping ErrRejected-like errors; the
	// service records it as failed.
	Check(ctx context.Context, credential []byte, settings map[string]string) (Check, error)
}
