package connections

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/oauth"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/telemetry"
	"github.com/xraph/loomplane/pkg/provider"
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

	// Connecting keylessly. The extension sets these. Without a usable
	// issuer, nothing keyless works.
	Keyless       KeylessIssuer
	KeylessClouds map[string]KeylessCloud
	// Rotate turns over the issuer's signing keys. The scheduled job calls
	// it. It may be nil.
	Rotate func(context.Context) error
	// IssuerServing checks that Loomplane's issuer answers at its public
	// URL with the key it signs with, as a cloud would fetch it. The
	// background check asks it once a run, before it marks any keyless
	// connection failed. Nil skips the check.
	IssuerServing func(context.Context) error

	// grants renews sign-ins. It's nil when the store can't lock a
	// connection, and then nothing is renewed.
	grants grantStore
	// renewals queues renewals before they take a database connection.
	renewals *renewGate

	// GPUReader reads a server or cluster's GPUs. Nil uses the real SSH and Kubernetes readers.
	GPUReader func(ctx context.Context, c *Connection, credential []byte) (provider.GPUInventory, error)
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

// pastedGrantAdvice is a GrantChecker that says, in its cloud's words,
// what to do instead of pasting a sign-in's grant.
type pastedGrantAdvice interface {
	PastedGrant() string
}

// PastedGrant implements pastedGrantAdvice.
func (GCPChecker) PastedGrant() string {
	return "paste a service account key, or sign in to Google instead of pasting a signed-in key"
}

// PastedGrant implements pastedGrantAdvice.
func (AzureChecker) PastedGrant() string {
	return "paste a service principal's client secret, or sign in to Azure instead of pasting a signed-in key"
}

// refusePastedGrant keeps Method true to what Vault holds: a pasted
// credential that is really a sign-in grant, or a keyless handout, is
// refused.
func refusePastedGrant(c Checker, cred []byte, method string) error {
	if g, ok := c.(GrantChecker); ok && method == MethodToken && g.IsGrant(cred) {
		if keylessJSON(cred) {
			return fmt.Errorf("%w: that's a keyless handout, not a credential; connect keylessly from the connect dialog instead", ErrInvalid)
		}
		if a, ok := c.(pastedGrantAdvice); ok {
			return fmt.Errorf("%w: %s", ErrInvalid, a.PastedGrant())
		}
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
	return s.create(ctx, p, "", in.Cloud, in.Name, []byte(strings.TrimSpace(in.Credential)), nil, in.Settings, MethodToken)
}

// newConnectionID makes the ID of a new connection.
func newConnectionID() (string, error) {
	id, err := typeid.Generate("conn")
	if err != nil {
		return "", fmt.Errorf("generate connection id: %w", err)
	}
	return id.String(), nil
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxName {
		return "", fmt.Errorf("%w: give it a name of at most %d characters", ErrInvalid, maxName)
	}
	return name, nil
}

// create checks cred with its cloud and stores a new connection that got
// its credential by method, with ID id, or a new one when id is empty.
// checked is what the check gets instead of cred, such as a sign-in's
// handout; nil checks cred. A keyless or in-cluster connection writes
// nothing to Vault. The caller has checked the principal's role.
func (s *Service) create(ctx context.Context, p security.Principal, id, cloud, name string, cred, checked []byte, settings map[string]string, method string) (*Connection, error) {
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
	if checked == nil {
		checked = cred
	}
	res, err := s.check(ctx, cloud, checker, checked, settings)
	if err != nil {
		return nil, err
	}
	if id == "" {
		if id, err = newConnectionID(); err != nil {
			return nil, err
		}
	}
	now := s.now().UTC()
	c := &Connection{ID: id, OrgID: p.OrgID, Cloud: cloud, Name: name, Method: method, CreatedBy: p.UserID, CreatedAt: now}
	c.SignedInBy = signer(p, method)
	apply(c, res, now)
	keeps := method != MethodKeyless && method != MethodInCluster
	// The secret first: a row without its secret would look connected and
	// fail every deployment, where a secret without its row is unreachable
	// and harmless.
	if keeps {
		if err := s.secrets.Put(ctx, p.OrgID, c.ID, cred); err != nil {
			return nil, err
		}
	}
	if err := s.store.CreateConnection(ctx, c); err != nil {
		if keeps {
			if derr := s.secrets.Delete(context.WithoutCancel(ctx), p.OrgID, c.ID); derr != nil {
				s.warn("remove the secret of a connection that wasn't saved", derr)
			}
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
// the connection failed. A sign-in the cloud no longer honors, or whose
// sign-in app it refuses, marks it sign_in_needed instead. A cloud it
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
		// Check what a deployment would get.
		g, grantErr := s.deployGrant(ctx, c)
		if grantErr == nil {
			cred, grantErr = s.checkable(ctx, c, g)
			grantErr = s.handoutWithoutApp(ctx, c, grantErr)
		}
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
	} else if c.Method == MethodKeyless {
		// A trust the cloud refuses fails the way a refused token does.
		if cred, err = s.keylessHandout(ctx, c); err != nil {
			return nil, err
		}
	} else if c.Method == MethodInCluster {
		// With the operator's switch off, the check fails it, and says why.
		cred = kubernetes.InClusterCredential()
	} else if cred, err = s.secrets.Reveal(ctx, p.OrgID, c.ID); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	res, err := s.check(ctx, c.Cloud, checker, cred, c.Details)
	if err != nil && legacyPersonalVercel(c) {
		// A failed legacy upgrade preserves the old row as well as its token.
		return nil, err
	}
	var why string
	switch {
	case c.Method != MethodOAuth || err == nil:
	case errors.Is(err, oauth.ErrAppRejected):
		// Signing in again with the same app won't help, so the app
		// shows the problem too.
		why = s.appRejected(ctx, c.OrgID, c.Cloud)
	case refusedSignIn(err):
		// Revoking Loomplane in the cloud kills the access token at once,
		// so a refused grant asks for a new sign-in.
		why = fmt.Sprintf("%s refused the sign-in. Sign in again.", cloudTitle(c.Cloud))
	}
	if why != "" {
		// The account is the same, so the generation stays.
		current, lapsed, markErr := s.signInRefused(ctx, c, why, &now)
		if markErr != nil {
			return nil, markErr
		}
		s.tellLapsed(ctx, lapsed)
		s.record(ctx, "cloud_connection.check", p, current)
		return current, nil
	}
	switch {
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
	return s.replace(ctx, p, id, []byte(strings.TrimSpace(in.Credential)), nil, in.Settings, MethodToken)
}

// signer is who a credential that got there by method is the sign-in of:
// the person signing in, or no one for a token.
func signer(p security.Principal, method string) string {
	if method == MethodOAuth {
		return p.UserID
	}
	return ""
}

// replace swaps connection id's credential for cred, which got there by
// method, if the cloud accepts it for the same account. checked is what
// the check gets instead of cred, as for create. A move to keyless writes
// nothing to Vault; the caller removes the old secret. The caller has
// checked the principal's role.
func (s *Service) replace(ctx context.Context, p security.Principal, id string, cred, checked []byte, settings map[string]string, method string) (*Connection, error) {
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
	if checked == nil {
		checked = cred
	}
	res, err := s.check(ctx, c.Cloud, checker, checked, settings)
	if err != nil {
		return nil, err
	}
	after := AccountIdentity(c.Cloud, res.Account, res.Details)
	if before == "" || after == "" || before != after {
		return nil, fmt.Errorf("%w: the verified account changed; create a new connection to change placement", ErrInvalid)
	}
	// Persistent stores serialize secret writes and invalidate old approvals first.
	replace := func() error {
		if method != MethodKeyless {
			if err := s.secrets.Put(ctx, p.OrgID, c.ID, cred); err != nil {
				return err
			}
		}
		c.Method = method
		c.SignedInBy = signer(p, method)
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
// day, or its handout, for a cloud whose sessions renew themselves. A
// keyless connection's is its keyless handout, and an in-cluster one's
// the in-cluster credential, and for neither is Vault read.
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
		g, grantErr := s.deployGrant(ctx, c)
		if grantErr != nil {
			return nil, nil, grantErr
		}
		if _, ok := s.Providers[c.Cloud].(Handout); ok {
			if cred, err = s.checkable(ctx, c, g); err != nil {
				return nil, nil, s.handoutWithoutApp(ctx, c, err)
			}
		} else {
			cred = []byte(g.AccessToken)
		}
	} else if c.Method == MethodKeyless {
		if cred, err = s.keylessHandout(ctx, c); err != nil {
			return nil, nil, err
		}
	} else if c.Method == MethodInCluster {
		cred = kubernetes.InClusterCredential()
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
