package connections

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/oauth"
	"github.com/xraph/loomplane/internal/security"
)

// signInTTL is how long someone has to finish a sign-in they started.
const signInTTL = 10 * time.Minute

// pkceClouds sign in with PKCE: the verifier stays with Loomplane and only
// its challenge goes to the cloud.
var pkceClouds = map[string]bool{GCP: true, Azure: true}

// SignInInput starts signing in to a cloud: for a new connection with
// Name, or for an existing one with ConnectionID. Settings are the
// choices a cloud's sign-in needs first, such as a GCP project.
type SignInInput struct {
	Cloud        string            `json:"cloud"`
	Name         string            `json:"name"`
	ConnectionID string            `json:"connection_id,omitempty"`
	Settings     map[string]string `json:"settings,omitempty"`
}

// SignIn is where to send someone to sign in, and the state the cloud
// should send back. Vercel may leave the state out, so the dashboard
// keeps it.
type SignIn struct {
	AuthorizeURL string `json:"authorize_url"`
	State        string `json:"state"`
}

// CompleteInput is what the cloud sent back to the callback.
type CompleteInput struct {
	State string `json:"state"`
	Code  string `json:"code"`
}

// StartSignIn records a sign-in and returns the cloud's page to send
// someone to. The state it carries works once, for the same person, for
// ten minutes.
func (s *Service) StartSignIn(ctx context.Context, p security.Principal, in SignInInput) (*SignIn, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return nil, err
	}
	// The cloud sends people back to the callback URL. Without one, a
	// sign-in could never finish, so none is recorded.
	if s.CallbackURL == "" {
		return nil, fmt.Errorf("%w: Loomplane has no callback URL for sign-ins; set public_url", ErrUnavailable)
	}
	cloud := strings.TrimSpace(in.Cloud)
	if err := s.provider(cloud); err != nil {
		return nil, err
	}
	if _, err := s.checker(cloud); err != nil {
		return nil, err
	}
	pending := &PendingSignIn{OrgID: p.OrgID, UserID: p.UserID, Cloud: cloud}
	settings := in.Settings
	if id := strings.TrimSpace(in.ConnectionID); id != "" {
		c, err := s.store.GetConnection(ctx, p.OrgID, id)
		if err != nil {
			return nil, err
		}
		if c.Cloud != cloud {
			return nil, fmt.Errorf("%w: that connection is for %s, not %s", ErrInvalid, cloudTitle(c.Cloud), cloudTitle(cloud))
		}
		pending.ConnectionID = c.ID
		// Signing in again keeps the connection where it is.
		settings = c.Details
	} else {
		name, err := validName(in.Name)
		if err != nil {
			return nil, err
		}
		pending.Name = name
	}
	settings, err := signInSettings(cloud, settings)
	if err != nil {
		return nil, err
	}
	pending.Settings = settings
	app, err := s.app(ctx, p.OrgID, cloud)
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate sign-in state: %w", err)
	}
	pending.State = base64.RawURLEncoding.EncodeToString(raw)
	asked := oauth.AuthorizeRequest{Redirect: s.CallbackURL, State: pending.State, Settings: settings}
	if pkceClouds[cloud] {
		if pending.Verifier, asked.Challenge, err = oauth.NewPKCE(); err != nil {
			return nil, err
		}
	}
	now := s.now().UTC()
	pending.CreatedAt, pending.ExpiresAt = now, now.Add(signInTTL)
	if err := s.Apps.PutSignIn(ctx, pending); err != nil {
		return nil, err
	}
	return &SignIn{AuthorizeURL: s.Providers[cloud].AuthorizeURL(app, asked), State: pending.State}, nil
}

// signInSettings keeps only the settings cloud's sign-in takes, checked.
// A cloud that takes none gets an empty map.
func signInSettings(cloud string, in map[string]string) (map[string]string, error) {
	var keys []string
	switch cloud {
	case GCP:
		keys = []string{"project_id"}
	case Azure:
		keys = []string{"tenant_id", "subscription_id", "resource_group"}
	}
	out := map[string]string{}
	for _, k := range keys {
		if v := strings.TrimSpace(in[k]); v != "" {
			out[k] = v
		}
	}
	if cloud == GCP {
		if out["project_id"] == "" {
			return nil, fmt.Errorf("%w: give the ID of the Google Cloud project to sign in to", ErrInvalid)
		}
		if err := gcp.CheckProjectID(out["project_id"]); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	}
	if cloud == Azure {
		// Normalized as a principal's connection records them, so a
		// sign-in lands on the same identity.
		tenant, subscription, group, err := azure.CheckPlacement(out)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		out = map[string]string{"tenant_id": tenant, "subscription_id": subscription, "resource_group": group}
	}
	return out, nil
}

// CompleteSignIn trades the code for a grant, checks it with the cloud,
// and stores it: as a new connection, or in place of the credential of
// the connection the sign-in named. Only the person who started the
// sign-in can finish it.
func (s *Service) CompleteSignIn(ctx context.Context, p security.Principal, in CompleteInput) (*Connection, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return nil, err
	}
	state, code := strings.TrimSpace(in.State), strings.TrimSpace(in.Code)
	if state == "" || code == "" {
		return nil, fmt.Errorf("%w: the cloud sent back an incomplete sign-in; start again", ErrInvalid)
	}
	pending, err := s.Apps.TakeSignIn(ctx, state)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("%w: this sign-in is unknown or already finished; start again", ErrInvalid)
	}
	if err != nil {
		return nil, err
	}
	if pending.OrgID != p.OrgID || pending.UserID != p.UserID {
		return nil, fmt.Errorf("%w: this sign-in was started by someone else", ErrInvalid)
	}
	if !s.now().Before(pending.ExpiresAt) {
		return nil, fmt.Errorf("%w: this sign-in expired; start again", ErrInvalid)
	}
	if providerErr := s.provider(pending.Cloud); providerErr != nil {
		return nil, providerErr
	}
	app, err := s.app(ctx, p.OrgID, pending.Cloud)
	if err != nil {
		return nil, err
	}
	grant, err := s.Providers[pending.Cloud].Exchange(ctx, app, oauth.CodeRequest{
		Redirect: s.CallbackURL, Code: code, Verifier: pending.Verifier, Settings: pending.Settings,
	})
	switch {
	case errors.Is(err, oauth.ErrAppRejected):
		return nil, fmt.Errorf("%w: %s", ErrInvalid, s.appRejected(ctx, p.OrgID, pending.Cloud))
	case errors.Is(err, oauth.ErrRefused):
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	// The cloud took the app, so whatever it refused before is fixed.
	if clearErr := s.Apps.ClearAppProblem(ctx, p.OrgID, pending.Cloud); clearErr != nil {
		s.warn("clear the problem of a sign-in app that worked", clearErr)
	}
	cred, err := json.Marshal(grant) // #nosec G117 -- the grant is the credential, bound for Vault, never a response.
	if err != nil {
		return nil, fmt.Errorf("encode sign-in grant: %w", err)
	}
	checked, err := s.checkable(ctx, &Connection{OrgID: p.OrgID, Cloud: pending.Cloud, Details: pending.Settings}, grant)
	if err != nil {
		return nil, err
	}
	if pending.ConnectionID != "" {
		// The check takes the sign-in's placement over the connection's
		// other settings, so a sign-in that reaches elsewhere is refused.
		current, getErr := s.store.GetConnection(ctx, p.OrgID, pending.ConnectionID)
		if getErr != nil {
			return nil, getErr
		}
		settings := maps.Clone(current.Details)
		if settings == nil {
			settings = map[string]string{}
		}
		maps.Copy(settings, pending.Settings)
		return s.replace(ctx, p, pending.ConnectionID, cred, checked, settings, MethodOAuth)
	}
	return s.create(ctx, p, "", pending.Cloud, pending.Name, cred, checked, pending.Settings, MethodOAuth)
}

// checkable is what a check of signed-in c gets for grant g: a handout,
// for a cloud whose sessions renew themselves, or else the grant itself.
// Either is a secret.
func (s *Service) checkable(ctx context.Context, c *Connection, g oauth.Grant) ([]byte, error) {
	if h, ok := s.Providers[c.Cloud].(Handout); ok {
		app, err := s.app(ctx, c.OrgID, c.Cloud)
		if err != nil {
			return nil, err
		}
		return h.Handout(app, g, c.Details)
	}
	cred, err := json.Marshal(g) // #nosec G117 -- the grant is the credential, bound for a check, never a response.
	if err != nil {
		return nil, fmt.Errorf("encode sign-in grant: %w", err)
	}
	return cred, nil
}
