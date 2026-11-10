package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
)

// ErrGitLabSignInLapsed means GitLab stopped honoring a connection's
// sign-in, so someone has to sign in again. Errors that wrap it are
// *GitLabSignInLapsedError, which names the account and the host.
var ErrGitLabSignInLapsed = errors.New("a GitLab sign-in needs signing in again")

// GitLabSignInLapsedError is a lapsed sign-in, by account and host.
type GitLabSignInLapsedError struct {
	Username string
	Host     string
}

func (e *GitLabSignInLapsedError) Error() string {
	return fmt.Sprintf("The GitLab sign-in for %s on %s needs signing in again", e.Username, e.Host)
}

// Is makes the error ErrGitLabSignInLapsed.
func (e *GitLabSignInLapsedError) Is(target error) bool { return target == ErrGitLabSignInLapsed }

// gitlabRenewAhead is how close to expiry an access token is renewed.
// GitLab's last two hours.
const gitlabRenewAhead = 5 * time.Minute

// gitlabRenewTimeout bounds a refresh and keeping its grant, once neither
// may be abandoned with the caller.
const gitlabRenewTimeout = 30 * time.Second

// Token returns a token that reads repo for orgID. It tries orgID's live
// connections on repo's host, oldest first, and keeps the first that can
// see the project. None is "", so a public project is read anonymously.
// Only orgID's own connections are ever tried.
//
// A connection lapses only when GitLab refuses to renew its grant
// (oauth.ErrRefused). GitLab answers 401, 403 and 404 alike to the project
// check, and the client reads all three as ErrNotFound, so a token GitLab
// revoked before it expires reads as "can't see it" and the next connection
// is tried; the revocation shows at the next renewal, within two hours,
// which GitLab refuses. A lapsed connection never reads as "not found":
// when no live connection sees the project and one on the host has lapsed,
// a project GitLab shows anonymously still reads with no token, and any
// other is ErrGitLabSignInLapsed, naming the oldest lapsed account and its
// host.
func (c *GitLabConnections) Token(ctx context.Context, orgID string, repo Repository) (string, error) {
	conns, err := c.cfg.Store.ListConnections(ctx, orgID)
	if err != nil {
		return "", err
	}
	sortOldestFirst(conns)
	host := canonicalGitLabHost(repo.Host)
	var (
		lapsed  *GitLabConnection
		lastErr error
	)
	for _, conn := range conns {
		if conn.OrgID != orgID || canonicalGitLabHost(conn.Host) != host {
			continue
		}
		if conn.LapsedAt != nil {
			if lapsed == nil {
				lapsed = conn
			}
			continue
		}
		h, ok, err := c.cfg.Registry.Host(ctx, orgID, conn.Host)
		if err != nil {
			lastErr = err
			continue
		}
		if !ok {
			continue
		}
		g, err := c.grant(ctx, conn)
		if errors.Is(err, ErrGitLabSignInLapsed) {
			if lapsed == nil {
				lapsed = conn
			}
			continue
		}
		if err != nil {
			lastErr = err
			continue
		}
		_, err = c.client(h).Project(ctx, repo, g.AccessToken)
		if err == nil {
			return g.AccessToken, nil
		}
		if !errors.Is(err, ErrNotFound) {
			lastErr = err
		}
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if lastErr != nil {
		// A connection that might have read the project couldn't be
		// asked, so reading anonymously could fail for good where trying
		// again would work.
		return "", lastErr
	}
	if lapsed == nil {
		return "", nil
	}
	// No live connection sees the project, but a lapsed one might. A
	// public project reads without a token, so ask GitLab anonymously:
	// only one that needs a sign-in is worth the lapsed error, which tells
	// someone what to do where "not found" wouldn't.
	lh, found, hostErr := c.cfg.Registry.Host(ctx, orgID, lapsed.Host)
	if hostErr != nil {
		return "", hostErr
	}
	if found {
		_, anonErr := c.client(lh).Project(ctx, repo, "")
		switch {
		case anonErr == nil:
			return "", nil
		case !errors.Is(anonErr, ErrNotFound):
			return "", anonErr
		}
	}
	return "", &GitLabSignInLapsedError{Username: lapsed.Username, Host: canonicalGitLabHost(lapsed.Host)}
}

// Lookup returns c as the token source of any host the registry reads.
func (c *GitLabConnections) Lookup(ctx context.Context, host string) (TokenSource, bool) {
	if _, ok := c.cfg.Registry.Reader(ctx, host); !ok {
		return nil, false
	}
	return c, true
}

// readGrant returns the grant Vault holds for conn.
func (c *GitLabConnections) readGrant(ctx context.Context, conn *GitLabConnection) (oauth.Grant, error) {
	raw, err := c.cfg.Secrets.Reveal(ctx, conn.OrgID, conn.ID)
	if err != nil {
		return oauth.Grant{}, fmt.Errorf("read the GitLab sign-in for %s on %s: %w", conn.Username, conn.Host, err)
	}
	var g oauth.Grant
	if err := json.Unmarshal(raw, &g); err != nil || g.AccessToken == "" {
		// The decode error could quote the secret, so it isn't kept.
		return oauth.Grant{}, fmt.Errorf("the stored GitLab sign-in for %s on %s can't be read; sign in again", conn.Username, conn.Host)
	}
	return g, nil
}

// grant returns a grant to call GitLab with for conn, renewed when it's
// due. It goes the way cloud sign-ins renew (connections.Service.renew):
// the process gate, a re-read, the grant lock across processes, another
// re-read, then the refresh, with a context the caller can't cancel since
// GitLab spends the old refresh token whether or not its answer is read.
// A refresh GitLab refuses marks the connection lapsed, and is a
// *GitLabSignInLapsedError.
func (c *GitLabConnections) grant(ctx context.Context, conn *GitLabConnection) (oauth.Grant, error) {
	held, err := c.readGrant(ctx, conn)
	if err != nil {
		return oauth.Grant{}, err
	}
	if !held.Due(c.cfg.Now(), gitlabRenewAhead) {
		return held, nil
	}
	g, err := c.renew(ctx, conn)
	switch {
	case err == nil:
		return g, nil
	case errors.Is(err, ErrGitLabSignInLapsed), ctx.Err() != nil:
		return oauth.Grant{}, err
	case held.Expiry.After(c.cfg.Now()):
		// GitLab couldn't be reached, but the token held still works.
		return held, nil
	default:
		return oauth.Grant{}, err
	}
}

func (c *GitLabConnections) renew(ctx context.Context, conn *GitLabConnection) (oauth.Grant, error) {
	leave, enterErr := c.gate.Enter(ctx, conn.OrgID+"/"+conn.ID)
	if enterErr != nil {
		return oauth.Grant{}, enterErr
	}
	defer leave()
	// Whoever went first has most likely renewed it already.
	if latest, readErr := c.readGrant(ctx, conn); readErr == nil && !latest.Due(c.cfg.Now(), gitlabRenewAhead) {
		return latest, nil
	}
	var g oauth.Grant
	lapsed := false
	lockErr := c.cfg.Store.WithGrantLock(ctx, conn.OrgID, conn.ID, func() error {
		current, err := c.cfg.Store.GetConnection(ctx, conn.ID)
		if err != nil {
			return err
		}
		if current.OrgID != conn.OrgID {
			return ErrGitLabConnectionNotFound
		}
		if current.LapsedAt != nil {
			lapsed = true
			return nil
		}
		latest, err := c.readGrant(ctx, current)
		if err != nil {
			return err
		}
		if !latest.Due(c.cfg.Now(), gitlabRenewAhead) {
			g = latest
			return nil
		}
		h, ok, err := c.cfg.Registry.Host(ctx, current.OrgID, current.Host)
		if err != nil {
			return err
		}
		if !ok {
			return ErrGitLabHostNotFound
		}
		app, err := c.app(ctx, h)
		if err != nil {
			return err
		}
		// GitLab may spend the old refresh token before its answer is
		// read, so a caller giving up must not cut the answer off, nor
		// stop the new grant being kept.
		detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitlabRenewTimeout)
		defer cancel()
		fresh, err := c.client(h).Refresh(detached, app, latest)
		if errors.Is(err, oauth.ErrRefused) {
			lapsed = true
			return c.cfg.Store.MarkLapsed(detached, current.OrgID, current.ID, c.cfg.Now().UTC())
		}
		if err != nil {
			return fmt.Errorf("renew the GitLab sign-in for %s on %s: %w", current.Username, current.Host, err)
		}
		if fresh.RefreshToken == "" {
			fresh.RefreshToken = latest.RefreshToken
		}
		raw, err := json.Marshal(fresh) // #nosec G117 -- the grant is the credential, bound for Vault, never a response.
		if err != nil {
			return fmt.Errorf("encode the renewed GitLab sign-in: %w", err)
		}
		if err := c.cfg.Secrets.Put(detached, current.OrgID, current.ID, raw); err != nil {
			return fmt.Errorf("keep the renewed GitLab sign-in: %w", err)
		}
		g = fresh
		return nil
	})
	if lockErr != nil {
		return oauth.Grant{}, lockErr
	}
	if lapsed {
		return oauth.Grant{}, &GitLabSignInLapsedError{Username: conn.Username, Host: canonicalGitLabHost(conn.Host)}
	}
	return g, nil
}
