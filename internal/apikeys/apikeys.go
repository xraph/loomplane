// Package apikeys is Loomplane's API keys, for the CLI, CI and automation.
// Keysmith keeps them: a key is shown once, when it's made, and only its
// hash is stored.
//
// A key belongs to one organization and acts as the member who made it,
// with that member's role, so it can never do more than they can. It can
// be limited to reading. When its maker leaves the organization, or loses
// a role, the key loses the same rights at once, because every request is
// checked against their role then. Keys are Loomplane credentials; the
// cloud credentials Loomplane deploys with are connections, kept in Vault.
package apikeys

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/xraph/keysmith"
	"github.com/xraph/keysmith/id"
	"github.com/xraph/keysmith/key"

	"github.com/xraph/loomplane/internal/security"
)

// Prefix starts every Loomplane API key: lp_live_ and 64 hex characters.
const Prefix = "lp"

// Access is what a key may do.
type Access string

const (
	// Read keys may only read: list, get, watch.
	Read Access = "read"
	// Write keys may do whatever their maker's role allows.
	Write Access = "write"
)

// Limits.
const (
	MaxNameLength = 80
	// MaxLifetime is the longest a key may live. Every key expires; a key
	// that never did would outlive the reason it was made.
	MaxLifetime = 366 * 24 * time.Hour
	// DefaultLifetime is how long a key lives when you don't say.
	DefaultLifetime = 90 * 24 * time.Hour
)

// Errors.
var (
	ErrInvalid  = errors.New("invalid API key request")
	ErrNotFound = errors.New("API key not found")
	// ErrNotWithKey refuses managing keys with a key: a key that could make
	// keys would outlive its own revocation.
	ErrNotWithKey = errors.New("API keys can only be managed from a signed-in session")
	// ErrRejected is a key that isn't one, or is revoked or expired.
	ErrRejected = errors.New("that API key isn't valid")
)

// APIKey is an API key as anyone can see it: never the key itself.
type APIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Access     Access     `json:"access"`
	Hint       string     `json:"hint"`
	CreatedBy  string     `json:"created_by"`
	State      string     `json:"state"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// CreatedKey is a new key, with the secret, which is never shown again.
type CreatedKey struct {
	Key    APIKey `json:"key"`
	Secret string `json:"secret"`
}

// NewKey is a key to make.
type NewKey struct {
	Name   string `json:"name"`
	Access Access `json:"access"`
	// ExpiresInDays defaults to 90, and can be at most 366.
	ExpiresInDays int `json:"expires_in_days,omitempty"`
}

// Authorizer is the part of security.Authorizer this package uses.
type Authorizer interface {
	Allowed(ctx context.Context, p security.Principal, perm security.Permission) (bool, error)
}

// Auditor records key changes. It may be nil.
type Auditor interface {
	Record(ctx context.Context, action string, p security.Principal, k APIKey)
}

// Service makes, lists, revokes and checks keys.
type Service struct {
	engine *keysmith.Engine
	appID  string
	authz  Authorizer
	audit  Auditor
	now    func() time.Time
}

// New returns a Service over a Keysmith engine. appID is the authsome app
// Loomplane runs as; keys are kept under it, and under each organization
// as Keysmith's tenant.
func New(engine *keysmith.Engine, appID string, authz Authorizer, audit Auditor) *Service {
	return &Service{engine: engine, appID: appID, authz: authz, audit: audit, now: time.Now}
}

func (s *Service) tenant(ctx context.Context, orgID string) context.Context {
	return keysmith.WithTenant(ctx, s.appID, orgID)
}

// Create makes a key for the caller in their organization.
func (s *Service) Create(ctx context.Context, p security.Principal, in NewKey) (*CreatedKey, error) {
	if p.KeyID != "" {
		return nil, ErrNotWithKey
	}
	// A key acts as the person who made it. Loomplane automation is no
	// person, and nothing may act as it through a key.
	if p.IsServiceAccount() {
		return nil, security.ErrForbidden
	}
	name := strings.TrimSpace(in.Name)
	switch {
	case name == "":
		return nil, fmt.Errorf("%w: give the key a name, such as the machine or pipeline it's for", ErrInvalid)
	case len(name) > MaxNameLength:
		return nil, fmt.Errorf("%w: the name is longer than %d characters", ErrInvalid, MaxNameLength)
	case in.Access != Read && in.Access != Write:
		return nil, fmt.Errorf("%w: access is read or write", ErrInvalid)
	case in.ExpiresInDays < 0 || time.Duration(in.ExpiresInDays)*24*time.Hour > MaxLifetime:
		return nil, fmt.Errorf("%w: a key lives at most %d days", ErrInvalid, int(MaxLifetime.Hours()/24))
	}
	lifetime := DefaultLifetime
	if in.ExpiresInDays > 0 {
		lifetime = time.Duration(in.ExpiresInDays) * 24 * time.Hour
	}
	expires := s.now().Add(lifetime).UTC()
	res, err := s.engine.CreateKey(s.tenant(ctx, p.OrgID), &keysmith.CreateKeyInput{
		Name: name, Prefix: Prefix, Environment: key.EnvLive, TenantID: p.OrgID,
		CreatedBy: p.UserID, ExpiresAt: &expires,
		Metadata: map[string]any{"access": string(in.Access)},
	})
	if err != nil {
		return nil, fmt.Errorf("create the key: %w", err)
	}
	out := &CreatedKey{Key: view(res.Key), Secret: res.RawKey}
	if s.audit != nil {
		s.audit.Record(ctx, "api_key.created", p, out.Key)
	}
	return out, nil
}

// List returns the caller's keys in their organization; an owner or admin
// sees everyone's.
func (s *Service) List(ctx context.Context, p security.Principal) ([]APIKey, error) {
	filter := &key.ListFilter{TenantID: p.OrgID}
	admin, err := s.admin(ctx, p)
	if err != nil {
		return nil, err
	}
	if !admin {
		filter.CreatedBy = p.UserID
	}
	keys, err := s.engine.ListKeys(s.tenant(ctx, p.OrgID), filter)
	if err != nil {
		return nil, fmt.Errorf("list keys: %w", err)
	}
	out := make([]APIKey, 0, len(keys))
	for _, k := range keys {
		if k.AppID == s.appID && k.TenantID == p.OrgID {
			out = append(out, view(k))
		}
	}
	slices.SortFunc(out, func(a, b APIKey) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out, nil
}

// Revoke revokes one of the caller's keys, or anyone's for an owner or
// admin. It works at once.
func (s *Service) Revoke(ctx context.Context, p security.Principal, keyID string) error {
	if p.KeyID != "" {
		return ErrNotWithKey
	}
	kid, err := id.ParseKeyID(keyID)
	if err != nil {
		return ErrNotFound
	}
	tctx := s.tenant(ctx, p.OrgID)
	k, err := s.engine.GetKey(tctx, kid)
	if err != nil || k.TenantID != p.OrgID || k.AppID != s.appID {
		return ErrNotFound
	}
	if k.CreatedBy != p.UserID {
		admin, aerr := s.admin(ctx, p)
		if aerr != nil {
			return aerr
		}
		if !admin {
			return ErrNotFound
		}
	}
	if err := s.engine.RevokeKey(tctx, kid, "revoked by "+p.UserID); err != nil {
		return fmt.Errorf("revoke the key: %w", err)
	}
	if s.audit != nil {
		v := view(k)
		v.State = string(key.StateRevoked)
		s.audit.Record(ctx, "api_key.revoked", p, v)
	}
	return nil
}

// Authenticate checks a key and returns who it acts as.
func (s *Service) Authenticate(ctx context.Context, raw string) (security.Principal, error) {
	if !IsKey(raw) {
		return security.Principal{}, ErrRejected
	}
	res, err := s.engine.ValidateKey(ctx, raw)
	if err != nil {
		return security.Principal{}, ErrRejected
	}
	k := res.Key
	if k.AppID != s.appID || k.TenantID == "" || k.CreatedBy == "" {
		return security.Principal{}, ErrRejected
	}
	if _, system := security.IsSystemSubject(k.CreatedBy); system {
		return security.Principal{}, ErrRejected
	}
	return security.Principal{
		UserID: k.CreatedBy, OrgID: k.TenantID, KeyID: k.ID.String(),
		ReadOnly: access(k) != Write,
	}, nil
}

// IsKey reports whether s looks like a Loomplane API key, so a request
// carrying one is authenticated as a key and never as a session.
func IsKey(s string) bool { return strings.HasPrefix(s, Prefix+"_") }

func (s *Service) admin(ctx context.Context, p security.Principal) (bool, error) {
	if s.authz == nil {
		return false, nil
	}
	return s.authz.Allowed(ctx, p, security.OrganizationAdminister)
}

// access is a key's access. Anything but write reads: a key whose
// metadata was lost can still do the least.
func access(k *key.Key) Access {
	if a, ok := k.Metadata["access"].(string); ok && a == string(Write) {
		return Write
	}
	return Read
}

func view(k *key.Key) APIKey {
	state := string(k.State)
	if k.RevokedAt != nil {
		state = string(key.StateRevoked)
	}
	return APIKey{
		ID: k.ID.String(), Name: k.Name, Access: access(k), Hint: k.Hint, CreatedBy: k.CreatedBy, State: state,
		CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt, RevokedAt: k.RevokedAt,
	}
}
