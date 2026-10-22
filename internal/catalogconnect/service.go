package catalogconnect

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/security"
)

// Store keeps endpoint rows. *PgStore is the real one.
type Store interface {
	// Create inserts an endpoint, or returns ErrNameTaken.
	Create(ctx context.Context, e *Endpoint) error
	Get(ctx context.Context, orgID, id string) (*Endpoint, error)
	List(ctx context.Context, orgID string) ([]*Endpoint, error)
	// Delete removes an endpoint, or returns ErrInUse while a binding's
	// composition connects a dependency to it, and ErrNotFound when
	// there's no such endpoint.
	Delete(ctx context.Context, orgID, id string) error
}

// Secrets keeps DSNs. deployments.VaultResourceSecrets is the real one.
type Secrets interface {
	Put(ctx context.Context, key string, value []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

// Authorizer decides whether a principal may act.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// Service is the endpoint API.
type Service struct {
	store   Store
	secrets Secrets
	authz   Authorizer
	now     func() time.Time
}

// NewService returns a Service.
func NewService(store Store, secrets Secrets, authz Authorizer) *Service {
	return &Service{store: store, secrets: secrets, authz: authz, now: time.Now}
}

// maxName bounds an endpoint's name.
const maxName = 100

// Create registers an endpoint. The DSN goes to Vault; the row and the
// returned Endpoint hold only the parts that aren't secret. It needs
// SecretManage.
func (s *Service) Create(ctx context.Context, p security.Principal, in CreateInput) (*Endpoint, error) {
	if err := s.authz.Require(ctx, p, security.SecretManage, ""); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > maxName {
		return nil, fmt.Errorf("%w: an endpoint needs a name of at most %d characters", ErrInvalid, maxName)
	}
	if in.Engine != EnginePostgres && in.Engine != EngineRedis {
		return nil, fmt.Errorf("%w: Loomplane connects postgres and redis endpoints, not %q", ErrInvalid, in.Engine)
	}
	e := &Endpoint{OrgID: p.OrgID, Name: name, Engine: in.Engine, CreatedBy: p.UserID, CreatedAt: s.now().UTC(), Revision: 1}
	if err := parseDSN(e, in.DSN); err != nil {
		return nil, err
	}
	id, err := typeid.Generate("endpoint")
	if err != nil {
		return nil, err
	}
	e.ID = id.String()
	// The DSN is stored first, so no row is ever without one.
	key := DSNKey(e.OrgID, e.ID)
	if err := s.secrets.Put(ctx, key, []byte(strings.TrimSpace(in.DSN))); err != nil {
		return nil, err
	}
	if err := s.store.Create(ctx, e); err != nil {
		if derr := s.secrets.Delete(ctx, key); derr != nil {
			return nil, errors.Join(err, derr)
		}
		return nil, err
	}
	return e, nil
}

// AuthorizeCheck checks p may check a DSN before storing it. That needs
// SecretManage, as storing one does: the check sends the DSN's
// credentials to the endpoint.
func (s *Service) AuthorizeCheck(ctx context.Context, p security.Principal) error {
	return s.authz.Require(ctx, p, security.SecretManage, "")
}

// List returns the organization's endpoints. It needs InfrastructureRead.
func (s *Service) List(ctx context.Context, p security.Principal) ([]*Endpoint, error) {
	if err := s.authz.Require(ctx, p, security.InfrastructureRead, ""); err != nil {
		return nil, err
	}
	return s.store.List(ctx, p.OrgID)
}

// Delete removes an endpoint and its DSN. It needs SecretManage, and
// returns ErrInUse while a binding connects to the endpoint; the DSN stays
// in Vault then. Nothing on the endpoint itself changes.
func (s *Service) Delete(ctx context.Context, p security.Principal, id string) error {
	if err := s.authz.Require(ctx, p, security.SecretManage, ""); err != nil {
		return err
	}
	if err := s.store.Delete(ctx, p.OrgID, id); err != nil {
		return err
	}
	if err := s.secrets.Delete(ctx, DSNKey(p.OrgID, id)); err != nil {
		return fmt.Errorf("the endpoint is gone, but its DSN is still in Vault: %w", err)
	}
	return nil
}

// Get returns an endpoint, without its DSN, for Loomplane's own use, such
// as a plan checking the endpoints it connects. It checks no principal.
func (s *Service) Get(ctx context.Context, orgID, id string) (*Endpoint, error) {
	return s.store.Get(ctx, orgID, id)
}

// Resolve returns an endpoint and its DSN for Loomplane's own use, such as
// a deployment connecting a service to it. It checks no principal: callers
// have already authorized the work the DSN is for.
func (s *Service) Resolve(ctx context.Context, orgID, id string) (*Endpoint, string, error) {
	e, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return nil, "", err
	}
	dsn, err := s.secrets.Get(ctx, DSNKey(orgID, id))
	if err != nil {
		return nil, "", fmt.Errorf("read the DSN of endpoint %s: %w", e.Name, err)
	}
	return e, string(dsn), nil
}
