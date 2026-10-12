package connections

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

const maxName = 60

// ErrUnavailable is a check that couldn't reach the cloud. Nothing
// changes; try again.
var ErrUnavailable = errors.New("the cloud couldn't be reached to check the credential; try again")

// Authorizer decides whether a principal may act.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// Auditor records connection changes, never a credential. Recording must
// not fail the change.
type Auditor interface {
	Record(ctx context.Context, action string, p security.Principal, c *Connection)
}

// Service is the connection API.
type Service struct {
	store    Store
	secrets  Secrets
	checkers map[string]Checker
	authz    Authorizer
	audit    Auditor
	now      func() time.Time
	warn     func(msg string, err error)
}

// NewService returns a Service. checkers maps each cloud to its checker;
// a cloud without one can't be connected. audit and warn may be nil.
func NewService(store Store, secrets Secrets, checkers map[string]Checker, authz Authorizer, audit Auditor, warn func(string, error)) *Service {
	if warn == nil {
		warn = func(string, error) {}
	}
	return &Service{store: store, secrets: secrets, checkers: checkers, authz: authz, audit: audit, now: time.Now, warn: warn}
}

// CreateInput connects a cloud account.
type CreateInput struct {
	// Cloud is gcp or vercel.
	Cloud string `json:"cloud"`
	Name  string `json:"name"`
	// Credential is a service account key's JSON, or a Vercel access
	// token. It's stored in Vault and never returned.
	Credential string `json:"credential"`
	// Settings are the cloud's options: team_id for Vercel.
	Settings map[string]string `json:"settings,omitempty"`
}

// ReplaceInput replaces a connection's credential, such as a rotated key.
type ReplaceInput struct {
	Credential string            `json:"credential"`
	Settings   map[string]string `json:"settings,omitempty"`
}

func (s *Service) checker(cloud string) (Checker, error) {
	c, ok := s.checkers[cloud]
	if !ok {
		return nil, fmt.Errorf("%w: Loomplane connects gcp, vercel and aws, not %q", ErrInvalid, cloud)
	}
	return c, nil
}

// check asks the cloud about a credential. A credential the cloud refused
// is ErrInvalid, with the cloud's reason; a cloud it couldn't reach is
// ErrUnavailable.
func (s *Service) check(ctx context.Context, c Checker, credential []byte, settings map[string]string) (Check, error) {
	if _, err := c.Parse(credential, settings); err != nil {
		return Check{}, err
	}
	res, err := c.Check(ctx, credential, settings)
	switch {
	case err == nil:
		return res, nil
	case rejected(err):
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	default:
		return Check{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
}

func apply(c *Connection, res Check, now time.Time) {
	c.Account, c.Details, c.Missing, c.Message = res.Account, res.Details, res.Missing, ""
	c.Status = StatusReady
	if len(res.Missing) > 0 {
		c.Status = StatusIncomplete
	}
	c.CheckedAt, c.UpdatedAt = &now, now
	c.normalize()
}

// Create checks a credential with its cloud and, if the cloud accepts it,
// stores it. A credential the cloud refuses is never stored.
func (s *Service) Create(ctx context.Context, p security.Principal, in CreateInput) (*Connection, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return nil, err
	}
	cloud := strings.TrimSpace(in.Cloud)
	checker, err := s.checker(cloud)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > maxName {
		return nil, fmt.Errorf("%w: give it a name of at most %d characters", ErrInvalid, maxName)
	}
	cred := []byte(strings.TrimSpace(in.Credential))
	res, err := s.check(ctx, checker, cred, in.Settings)
	if err != nil {
		return nil, err
	}
	id, err := typeid.Generate("conn")
	if err != nil {
		return nil, fmt.Errorf("generate connection id: %w", err)
	}
	now := s.now().UTC()
	c := &Connection{ID: id.String(), OrgID: p.OrgID, Cloud: cloud, Name: name, CreatedBy: p.UserID, CreatedAt: now}
	apply(c, res, now)
	// The secret first: a row without its secret would look connected and
	// fail every deployment, where a secret without its row is unreachable
	// and harmless.
	if err := s.secrets.Put(ctx, p.OrgID, c.ID, cred); err != nil {
		return nil, err
	}
	if err := s.store.CreateConnection(ctx, c); err != nil {
		if derr := s.secrets.Delete(context.WithoutCancel(ctx), p.OrgID, c.ID); derr != nil {
			s.warn("remove the secret of a connection that wasn't saved", derr)
		}
		return nil, err
	}
	s.record(ctx, "cloud_connection.create", p, c)
	return c, nil
}

// List returns the organization's connections.
func (s *Service) List(ctx context.Context, p security.Principal) ([]*Connection, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountRead, ""); err != nil {
		return nil, err
	}
	return s.store.ListConnections(ctx, p.OrgID)
}

// Get returns one connection.
func (s *Service) Get(ctx context.Context, p security.Principal, id string) (*Connection, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountRead, ""); err != nil {
		return nil, err
	}
	return s.store.GetConnection(ctx, p.OrgID, id)
}

// Recheck asks the cloud again, such as after granting the permissions an
// incomplete connection lacked. A credential the cloud now refuses marks
// the connection failed; a cloud it can't reach changes nothing.
func (s *Service) Recheck(ctx context.Context, p security.Principal, id string) (*Connection, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return nil, err
	}
	c, err := s.store.GetConnection(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	checker, err := s.checker(c.Cloud)
	if err != nil {
		return nil, err
	}
	cred, err := s.secrets.Reveal(ctx, p.OrgID, c.ID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	res, err := s.check(ctx, checker, cred, c.Details)
	switch {
	case errors.Is(err, ErrInvalid):
		c.Status, c.Message, c.CheckedAt, c.UpdatedAt = StatusFailed, strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": "), &now, now
	case err != nil:
		return nil, err
	default:
		apply(c, res, now)
	}
	if err := s.store.UpdateConnection(ctx, c); err != nil {
		return nil, err
	}
	s.record(ctx, "cloud_connection.check", p, c)
	return c, nil
}

// Replace swaps a connection's credential for a new one the cloud
// accepts, such as a rotated key. It keeps the connection's ID, so
// nothing that names the connection has to change.
func (s *Service) Replace(ctx context.Context, p security.Principal, id string, in ReplaceInput) (*Connection, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return nil, err
	}
	c, err := s.store.GetConnection(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	checker, err := s.checker(c.Cloud)
	if err != nil {
		return nil, err
	}
	settings := in.Settings
	if settings == nil {
		settings = c.Details
	}
	cred := []byte(strings.TrimSpace(in.Credential))
	res, err := s.check(ctx, checker, cred, settings)
	if err != nil {
		return nil, err
	}
	if err := s.secrets.Put(ctx, p.OrgID, c.ID, cred); err != nil {
		return nil, err
	}
	apply(c, res, s.now().UTC())
	if err := s.store.UpdateConnection(ctx, c); err != nil {
		return nil, err
	}
	s.record(ctx, "cloud_connection.replace", p, c)
	return c, nil
}

// Delete removes a connection and its credential.
func (s *Service) Delete(ctx context.Context, p security.Principal, id string) error {
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return err
	}
	c, err := s.store.GetConnection(ctx, p.OrgID, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteConnection(ctx, p.OrgID, id); err != nil {
		return err
	}
	if err := s.secrets.Delete(context.WithoutCancel(ctx), p.OrgID, id); err != nil {
		// The row is gone, so nothing can reach the secret any more.
		s.warn("remove a deleted connection's secret", err)
	}
	s.record(ctx, "cloud_connection.delete", p, c)
	return nil
}

// Credential returns a ready connection and its credential, for a
// deployment. It checks no role: only Loomplane's own workflow calls it,
// for a plan someone was allowed to approve.
func (s *Service) Credential(ctx context.Context, orgID, id string) (*Connection, []byte, error) {
	c, err := s.store.GetConnection(ctx, orgID, id)
	if err != nil {
		return nil, nil, err
	}
	if c.Status != StatusReady {
		return nil, nil, fmt.Errorf("%w: %s is %s", ErrNotReady, c.Name, c.Status)
	}
	cred, err := s.secrets.Reveal(ctx, orgID, id)
	if err != nil {
		return nil, nil, err
	}
	return c, cred, nil
}

// All returns the organization's connections, for planning to choose
// from. It checks no role, like Credential.
func (s *Service) All(ctx context.Context, orgID string) ([]*Connection, error) {
	return s.store.ListConnections(ctx, orgID)
}

func (s *Service) record(ctx context.Context, action string, p security.Principal, c *Connection) {
	if s.audit != nil {
		s.audit.Record(ctx, action, p, c)
	}
}
