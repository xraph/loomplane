package repositories

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
	"github.com/xraph/loomplane/internal/security"
)

// gitlabSignInLifetime is how long someone has to finish signing in.
const gitlabSignInLifetime = 15 * time.Minute

// StartGitLabSignInInput picks the host to sign in through, by its
// GitLabHostView.ID.
type StartGitLabSignInInput struct {
	HostID string `json:"host_id"`
}

// GitLabSignInStart is where to send the browser to sign in.
type GitLabSignInStart struct {
	AuthorizeURL string `json:"authorize_url"`
}

// ConnectGitLabInput is what GitLab's redirect carries back.
type ConnectGitLabInput struct {
	Code  string `json:"code"`
	State string `json:"state"`
}

// StartSignIn keeps a pending sign-in for the caller and returns GitLab's
// authorize URL, asking for read_api with a PKCE challenge. The verifier
// stays here.
func (c *GitLabConnections) StartSignIn(ctx context.Context, p security.Principal, in StartGitLabSignInInput) (*GitLabSignInStart, error) {
	if err := c.cfg.Authz.Require(ctx, p, security.RepositoryConnect, ""); err != nil {
		return nil, err
	}
	h, err := c.hostByID(ctx, p.OrgID, in.HostID)
	if err != nil {
		return nil, err
	}
	verifier, challenge, err := oauth.NewPKCE()
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, randErr := rand.Read(raw); randErr != nil {
		return nil, fmt.Errorf("generate a sign-in state: %w", randErr)
	}
	state := base64.RawURLEncoding.EncodeToString(raw)
	now := c.cfg.Now().UTC()
	err = c.cfg.Store.SaveSignIn(ctx, &GitLabSignIn{
		State: state, OrgID: p.OrgID, UserID: p.UserID, Host: h.Host, Verifier: verifier,
		ExpiresAt: now.Add(gitlabSignInLifetime), CreatedAt: now,
	})
	if err != nil {
		return nil, err
	}
	return &GitLabSignInStart{AuthorizeURL: c.client(h).AuthorizeURL(h.ClientID, c.cfg.CallbackURL, state, challenge)}, nil
}

// Connect finishes a sign-in the caller started: it spends the pending
// sign-in, trades the code for a grant, reads who signed in and the GitLab
// release, and keeps the connection. The grant goes to Vault only.
func (c *GitLabConnections) Connect(ctx context.Context, p security.Principal, in ConnectGitLabInput) (*GitLabConnectionView, error) {
	if err := c.cfg.Authz.Require(ctx, p, security.RepositoryConnect, ""); err != nil {
		return nil, err
	}
	s, err := c.cfg.Store.TakeSignIn(ctx, in.State, c.cfg.Now())
	if err != nil {
		return nil, err
	}
	if s.OrgID != p.OrgID || s.UserID != p.UserID {
		return nil, ErrBadState
	}
	// The host is looked up again as the caller's organization may use it
	// now: an organization host it removed since is gone.
	h, ok, err := c.cfg.Registry.Host(ctx, p.OrgID, s.Host)
	if err != nil {
		return nil, err
	}
	if !ok || h.ClientID == "" {
		return nil, ErrGitLabHostNotFound
	}
	app, err := c.app(ctx, h)
	if err != nil {
		return nil, err
	}
	gl := c.client(h)
	grant, err := gl.Exchange(ctx, app, c.cfg.CallbackURL, in.Code, s.Verifier)
	if err != nil {
		return nil, fmt.Errorf("finish signing in to %s: %w", h.Host, err)
	}
	user, err := gl.User(ctx, grant.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("read the account signed in to %s: %w", h.Host, err)
	}
	// An old release connects with a warning, and one that won't say
	// connects as an unknown release: neither is refused.
	version, err := gl.Version(ctx, grant.AccessToken)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		version = ""
	}
	conn := &GitLabConnection{
		OrgID: p.OrgID, Host: h.Host, HostID: h.ID, Kind: GitLabKindOAuth,
		GitLabUserID: user.ID, Username: user.Username, AvatarURL: user.AvatarURL, Version: version,
		CreatedBy: p.UserID, CreatedAt: c.cfg.Now().UTC(),
	}
	id, err := c.cfg.Store.SaveConnection(ctx, conn)
	if err != nil {
		return nil, err
	}
	conn.ID = id
	raw, err := json.Marshal(grant) // #nosec G117 -- the grant is the credential, bound for Vault, never a response.
	if err != nil {
		return nil, fmt.Errorf("encode the GitLab sign-in: %w", err)
	}
	// Under the grant lock, so a renewal of the same connection running
	// now can't overwrite the new grant with one from the old sign-in. A
	// renewal GitLab refused while this waited for the lock has marked the
	// old sign-in lapsed, so the connection is saved again once the new
	// grant is in, which clears that.
	err = c.cfg.Store.WithGrantLock(ctx, p.OrgID, id, func() error {
		if putErr := c.cfg.Secrets.Put(ctx, p.OrgID, id, raw); putErr != nil {
			return putErr
		}
		_, saveErr := c.cfg.Store.SaveConnection(ctx, conn)
		return saveErr
	})
	if err != nil {
		// Without its grant the connection can't read anything, so it
		// shows as needing a sign-in rather than looking connected.
		err = fmt.Errorf("keep the GitLab sign-in: %w", err)
		if markErr := c.cfg.Store.MarkLapsed(context.WithoutCancel(ctx), p.OrgID, id, c.cfg.Now().UTC()); markErr != nil {
			return nil, errors.Join(err, markErr)
		}
		return nil, err
	}
	c.record(ctx, AuditGitLabConnect, p, map[string]any{"host": h.Host, "username": user.Username, "connection_id": id})
	saved, err := c.ownConnection(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	v := c.connectionView(saved)
	return &v, nil
}
