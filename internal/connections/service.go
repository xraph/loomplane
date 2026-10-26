package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/clouds/oauth"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/telemetry"
)

const maxName = 60

const replacementInProgressMessage = "Credential replacement is in progress. Retry replacement if it did not finish."

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
	// Telemetry counts credential checks. It may be nil.
	Telemetry *telemetry.Recorder

	// Signing in to a cloud. The extension sets these.
	Providers   map[string]Provider
	ConfigApps  map[string]oauth.App
	CallbackURL string
	// AppSecrets keeps sign-in app secrets, keyed by organization and cloud.
	AppSecrets Secrets
	Apps       AppStore
	// Lapsed hears about a signed-in connection the cloud stopped
	// honoring. It may be nil.
	Lapsed func(ctx context.Context, c *Connection)

	// grants renews sign-ins. It's nil when the store can't lock a
	// connection, and then nothing is renewed.
	grants grantStore
	// renewals queues renewals before they take a database connection.
	renewals *renewGate
}

// NewService returns a Service. checkers maps each cloud to its checker;
// a cloud without one can't be connected. audit and warn may be nil.
func NewService(store Store, secrets Secrets, checkers map[string]Checker, authz Authorizer, audit Auditor, warn func(string, error)) *Service {
	if warn == nil {
		warn = func(string, error) {}
	}
	grants, _ := store.(grantStore) //nolint:errcheck // a store that can't lock leaves grants nil, as documented
	return &Service{store: store, secrets: secrets, checkers: checkers, authz: authz, audit: audit, now: time.Now, warn: warn, grants: grants, renewals: newRenewGate(maxRenewals)}
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
		return nil, fmt.Errorf("%w: Loomplane connects %s, not %q", ErrInvalid, strings.Join(slices.Sorted(maps.Keys(s.checkers)), ", "), cloud)
	}
	return c, nil
}

// check asks the cloud about a credential. A credential the cloud refused
// is ErrInvalid, with the cloud's reason; a cloud it couldn't reach is
// ErrUnavailable.
func (s *Service) check(ctx context.Context, cloud string, c Checker, credential []byte, settings map[string]string) (Check, error) {
	if _, err := c.Parse(credential, settings); err != nil {
		return Check{}, err
	}
	res, err := c.Check(ctx, credential, settings)
	switch {
	case err == nil && len(res.Missing) > 0:
		s.Telemetry.ConnectionChecked(cloud, StatusIncomplete)
		return res, nil
	case err == nil:
		s.Telemetry.ConnectionChecked(cloud, StatusReady)
		return res, nil
	case rejected(err):
		s.Telemetry.ConnectionChecked(cloud, "rejected")
		return Check{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	default:
		s.Telemetry.ConnectionChecked(cloud, "unavailable")
		return Check{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
}

// refusePastedGrant keeps Method true to what Vault holds: a pasted
// credential that is really a sign-in grant is refused.
func refusePastedGrant(c Checker, cred []byte, method string) error {
	if g, ok := c.(GrantChecker); ok && method == MethodToken && g.IsGrant(cred) {
		return fmt.Errorf("%w: paste the API token itself, not JSON", ErrInvalid)
	}
	return nil
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
	return s.create(ctx, p, in.Cloud, in.Name, []byte(strings.TrimSpace(in.Credential)), in.Settings, MethodToken)
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxName {
		return "", fmt.Errorf("%w: give it a name of at most %d characters", ErrInvalid, maxName)
	}
	return name, nil
}

// create checks cred with its cloud and stores a new connection that got
// its credential by method. The caller has checked the principal's role.
func (s *Service) create(ctx context.Context, p security.Principal, cloud, name string, cred []byte, settings map[string]string, method string) (*Connection, error) {
	cloud = strings.TrimSpace(cloud)
	checker, err := s.checker(cloud)
	if err != nil {
		return nil, err
	}
	name, err = validName(name)
	if err != nil {
		return nil, err
	}
	if refuseErr := refusePastedGrant(checker, cred, method); refuseErr != nil {
		return nil, refuseErr
	}
	res, err := s.check(ctx, cloud, checker, cred, settings)
	if err != nil {
		return nil, err
	}
	id, err := typeid.Generate("conn")
	if err != nil {
		return nil, fmt.Errorf("generate connection id: %w", err)
	}
	now := s.now().UTC()
	c := &Connection{ID: id.String(), OrgID: p.OrgID, Cloud: cloud, Name: name, Method: method, CreatedBy: p.UserID, CreatedAt: now}
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
// the connection failed, or sign_in_needed if it was a sign-in; a cloud it
// can't reach changes nothing.
func (s *Service) Recheck(ctx context.Context, p security.Principal, id string) (*Connection, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return nil, err
	}
	c, err := s.store.GetConnection(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if c.Message == replacementInProgressMessage {
		return nil, ErrUnavailable
	}
	if identityErr := recordedOwnedIdentity(c); identityErr != nil {
		return nil, identityErr
	}
	checker, err := s.checker(c.Cloud)
	if err != nil {
		return nil, err
	}
	var cred []byte
	if c.Method == MethodOAuth {
		// Check the grant a deployment would get, renewed if it's due.
		g, grantErr := s.usableGrant(ctx, c)
		if errors.Is(grantErr, ErrNotReady) {
			if c, err = s.store.GetConnection(ctx, p.OrgID, id); err != nil {
				return nil, err
			}
			s.record(ctx, "cloud_connection.check", p, c)
			return c, nil
		}
		if grantErr != nil {
			return nil, grantErr
		}
		if cred, err = json.Marshal(g); err != nil { // #nosec G117 -- the grant is the credential, bound for Vault, never a response.
			return nil, fmt.Errorf("encode sign-in grant: %w", err)
		}
	} else if cred, err = s.secrets.Reveal(ctx, p.OrgID, c.ID); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	res, err := s.check(ctx, c.Cloud, checker, cred, c.Details)
	if err != nil && legacyPersonalVercel(c) {
		// A failed legacy upgrade preserves the old row as well as its token.
		return nil, err
	}
	switch {
	case errors.Is(err, ErrInvalid) && c.Method == MethodOAuth:
		// Revoking Loomplane in the cloud kills the access token at once,
		// so a refused grant asks for a new sign-in. The account is the
		// same, so the generation stays.
		current, lapsed, markErr := s.signInRefused(ctx, c, fmt.Sprintf("%s refused the sign-in. Sign in again.", c.Cloud), now)
		if markErr != nil {
			return nil, markErr
		}
		s.tellLapsed(ctx, lapsed)
		s.record(ctx, "cloud_connection.check", p, current)
		return current, nil
	case errors.Is(err, ErrInvalid):
		c.Status, c.Message, c.CheckedAt, c.UpdatedAt = StatusFailed, strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": "), &now, now
	case err != nil:
		return nil, err
	default:
		before, after := AccountIdentity(c.Cloud, c.Account, c.Details), AccountIdentity(c.Cloud, res.Account, res.Details)
		if legacyPersonalVercel(c) && verifiedPersonalVercel(res) {
			// This check used the stored token, so its immutable user ID
			// safely upgrades the connection even if its username changed.
			before = after
		}
		if before == "" || after == "" || before != after {
			return nil, fmt.Errorf("%w: the verified account changed; create a new connection to change placement", ErrInvalid)
		}
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
// A pasted credential turns off a connection's sign-in.
func (s *Service) Replace(ctx context.Context, p security.Principal, id string, in ReplaceInput) (*Connection, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return nil, err
	}
	return s.replace(ctx, p, id, []byte(strings.TrimSpace(in.Credential)), in.Settings, MethodToken)
}

// replace swaps connection id's credential for cred, which got there by
// method, if the cloud accepts it for the same account. The caller has
// checked the principal's role.
func (s *Service) replace(ctx context.Context, p security.Principal, id string, cred []byte, settings map[string]string, method string) (*Connection, error) {
	c, err := s.store.GetConnection(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if identityErr := recordedOwnedIdentity(c); identityErr != nil {
		return nil, identityErr
	}
	checker, err := s.checker(c.Cloud)
	if err != nil {
		return nil, err
	}
	if refuseErr := refusePastedGrant(checker, cred, method); refuseErr != nil {
		return nil, refuseErr
	}
	before := AccountIdentity(c.Cloud, c.Account, c.Details)
	if legacyPersonalVercel(c) {
		oldCredential, revealErr := s.secrets.Reveal(ctx, p.OrgID, c.ID)
		if revealErr != nil {
			return nil, revealErr
		}
		oldCheck, checkErr := s.check(ctx, c.Cloud, checker, oldCredential, c.Details)
		if checkErr != nil {
			return nil, fmt.Errorf("verify the existing Vercel token before replacement: %w", checkErr)
		}
		if !verifiedPersonalVercel(oldCheck) {
			return nil, fmt.Errorf("%w: the existing Vercel token did not establish an immutable user identity", ErrInvalid)
		}
		before = AccountIdentity(c.Cloud, oldCheck.Account, oldCheck.Details)
	}
	if settings == nil {
		settings = c.Details
	}
	res, err := s.check(ctx, c.Cloud, checker, cred, settings)
	if err != nil {
		return nil, err
	}
	after := AccountIdentity(c.Cloud, res.Account, res.Details)
	if before == "" || after == "" || before != after {
		return nil, fmt.Errorf("%w: the verified account changed; create a new connection to change placement", ErrInvalid)
	}
	// Persistent stores serialize secret writes and invalidate old approvals first.
	replace := func() error {
		if err := s.secrets.Put(ctx, p.OrgID, c.ID, cred); err != nil {
			return err
		}
		c.Method = method
		apply(c, res, s.now().UTC())
		return s.store.UpdateConnection(ctx, c)
	}
	if versioned, ok := s.store.(interface {
		WithCredentialReplacement(context.Context, *Connection, func() error) error
	}); ok {
		if err := versioned.WithCredentialReplacement(ctx, c, replace); err != nil {
			return nil, err
		}
	} else {
		c.Generation++
		if err := replace(); err != nil {
			return nil, err
		}
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
// for a plan someone was allowed to approve. A signed-in connection's
// credential is its access token, renewed first if it expires within a
// day.
func (s *Service) Credential(ctx context.Context, orgID, id string) (*Connection, []byte, error) {
	c, err := s.store.GetConnection(ctx, orgID, id)
	if err != nil {
		return nil, nil, err
	}
	if c.Message == replacementInProgressMessage || (c.Status != StatusReady && !RegistryPullReady(c)) {
		return nil, nil, fmt.Errorf("%w: %s is %s", ErrNotReady, c.Name, c.Status)
	}
	var cred []byte
	if c.Method == MethodOAuth {
		g, grantErr := s.usableGrant(ctx, c)
		if grantErr != nil {
			return nil, nil, grantErr
		}
		cred = []byte(g.AccessToken)
	} else if cred, err = s.secrets.Reveal(ctx, orgID, id); err != nil {
		return nil, nil, err
	}
	// A replacement may start between reading the row and reading Vault.
	current, err := s.store.GetConnection(ctx, orgID, id)
	if err != nil {
		return nil, nil, err
	}
	if current.Message == replacementInProgressMessage || current.Generation != c.Generation || current.Status != c.Status {
		return nil, nil, ErrChanged
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
