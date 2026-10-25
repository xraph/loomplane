package connections

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
	"github.com/xraph/loomplane/internal/security"
)

// signInTTL is how long someone has to finish a sign-in they started.
const signInTTL = 10 * time.Minute

// SignInInput starts signing in to a cloud: for a new connection with
// Name, or for an existing one with ConnectionID.
type SignInInput struct {
	Cloud        string `json:"cloud"`
	Name         string `json:"name"`
	ConnectionID string `json:"connection_id,omitempty"`
}

// SignIn is where to send someone to sign in.
type SignIn struct {
	AuthorizeURL string `json:"authorize_url"`
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
	if id := strings.TrimSpace(in.ConnectionID); id != "" {
		c, err := s.store.GetConnection(ctx, p.OrgID, id)
		if err != nil {
			return nil, err
		}
		if c.Cloud != cloud {
			return nil, fmt.Errorf("%w: that connection is for %s, not %s", ErrInvalid, c.Cloud, cloud)
		}
		pending.ConnectionID = c.ID
	} else {
		name, err := validName(in.Name)
		if err != nil {
			return nil, err
		}
		pending.Name = name
	}
	app, err := s.app(ctx, p.OrgID, cloud)
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate sign-in state: %w", err)
	}
	pending.State = base64.RawURLEncoding.EncodeToString(raw)
	now := s.now().UTC()
	pending.CreatedAt, pending.ExpiresAt = now, now.Add(signInTTL)
	if err := s.Apps.PutSignIn(ctx, pending); err != nil {
		return nil, err
	}
	return &SignIn{AuthorizeURL: s.Providers[cloud].AuthorizeURL(app, s.CallbackURL, pending.State)}, nil
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
	grant, err := s.Providers[pending.Cloud].Exchange(ctx, app, s.CallbackURL, code)
	switch {
	case errors.Is(err, oauth.ErrRefused):
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	cred, err := json.Marshal(grant) // #nosec G117 -- the grant is the credential, bound for Vault, never a response.
	if err != nil {
		return nil, fmt.Errorf("encode sign-in grant: %w", err)
	}
	if pending.ConnectionID != "" {
		return s.replace(ctx, p, pending.ConnectionID, cred, nil, MethodOAuth)
	}
	return s.create(ctx, p, pending.Cloud, pending.Name, cred, nil, MethodOAuth)
}
