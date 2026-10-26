package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
)

// refreshAhead is how close to expiry a deployment's sign-in is renewed.
const refreshAhead = 24 * time.Hour

// backgroundAhead is how close to expiry the background job renews a
// sign-in, so one nobody deploys with still stays fresh.
const backgroundAhead = 7 * 24 * time.Hour

// renewTimeout bounds asking the cloud for a new grant, and keeping it,
// once neither may be abandoned with the caller. The lock is held
// throughout, so neither may wait forever either.
const renewTimeout = 30 * time.Second

// maxRenewals caps the renewals one process runs against the database at
// once. Each holds a pooled connection for its lock and borrows another
// for its queries, so this keeps renewals to half the default pool of 16.
const maxRenewals = 4

// grantStore is what renewing a sign-in needs from the database: one lock
// per connection, shared with credential replacement, and every sign-in
// that could need renewing. *PgStore has it.
type grantStore interface {
	OAuthConnections(ctx context.Context) ([]*Connection, error)
	WithConnectionLock(ctx context.Context, orgID, id string, fn func() error) error
}

// renewGate queues renewals in the process before they reach the
// database. A caller waiting on the database lock holds a pooled
// connection, so without it many callers of one connection could take
// the whole pool and leave none for the caller holding the lock.
type renewGate struct {
	mu    sync.Mutex
	queue map[string]*renewQueue
	slots chan struct{}
}

// renewQueue lets one caller of a connection through at a time.
type renewQueue struct {
	turn  chan struct{}
	users int
}

func newRenewGate(slots int) *renewGate {
	return &renewGate{queue: map[string]*renewQueue{}, slots: make(chan struct{}, slots)}
}

// enter waits for key's turn and then for a free slot, or for ctx to end.
// The caller calls leave when done.
func (g *renewGate) enter(ctx context.Context, key string) (leave func(), err error) {
	g.mu.Lock()
	q := g.queue[key]
	if q == nil {
		q = &renewQueue{turn: make(chan struct{}, 1)}
		g.queue[key] = q
	}
	q.users++
	g.mu.Unlock()
	forget := func() {
		g.mu.Lock()
		q.users--
		if q.users == 0 {
			delete(g.queue, key)
		}
		g.mu.Unlock()
	}
	select {
	case q.turn <- struct{}{}:
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
	select {
	case g.slots <- struct{}{}:
	case <-ctx.Done():
		<-q.turn
		forget()
		return nil, ctx.Err()
	}
	return func() {
		<-g.slots
		<-q.turn
		forget()
	}, nil
}

// RefreshReport counts what one pass over the sign-ins did.
type RefreshReport struct{ Refreshed, Lapsed, Failed int }

// readGrant returns the sign-in grant Vault holds for c.
func (s *Service) readGrant(ctx context.Context, c *Connection) (oauth.Grant, error) {
	raw, err := s.secrets.Reveal(ctx, c.OrgID, c.ID)
	if err != nil {
		return oauth.Grant{}, err
	}
	var g oauth.Grant
	if err := json.Unmarshal(raw, &g); err != nil || g.AccessToken == "" {
		// The decode error could quote the secret, so it isn't kept.
		return oauth.Grant{}, fmt.Errorf("%w: the stored sign-in for %s can't be read; sign in again", ErrInvalid, c.Name)
	}
	return g, nil
}

// usableGrant returns a grant to call the cloud with: renewed if it's
// due, or the one Vault holds while it still works and the cloud can't be
// reached. A sign-in the cloud no longer honors is ErrNotReady.
func (s *Service) usableGrant(ctx context.Context, c *Connection) (oauth.Grant, error) {
	held, err := s.readGrant(ctx, c)
	if err != nil {
		return oauth.Grant{}, err
	}
	g, _, err := s.renew(ctx, c, held, refreshAhead)
	switch {
	case err == nil:
		return g, nil
	case errors.Is(err, ErrNotReady), errors.Is(err, ErrChanged):
		return oauth.Grant{}, err
	case ctx.Err() != nil:
		// The caller gave up, so it needs no token.
		return oauth.Grant{}, ctx.Err()
	case held.Expiry.After(s.now()):
		s.warn("renew the sign-in of "+c.ID+"; using its unexpired token", err)
		return held, nil
	case errors.Is(err, ErrUnavailable):
		return oauth.Grant{}, err
	default:
		return oauth.Grant{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
}

// deployGrant is the grant a deployment's credential comes from: the one
// Vault holds, for a cloud whose sessions renew themselves from it, or
// else a usable one.
func (s *Service) deployGrant(ctx context.Context, c *Connection) (oauth.Grant, error) {
	if _, ok := s.Providers[c.Cloud].(Handout); ok {
		return s.readGrant(ctx, c)
	}
	return s.usableGrant(ctx, c)
}

// renew trades held's refresh token for a new grant if held expires
// within ahead. Callers in this process queue in s.renewals first, so at
// most one per connection waits on the database. It then takes the
// connection's lock and reads Vault again, so of several callers in any
// process only the first spends the refresh token and the rest use what
// it stored. renewed reports whether this
// call renewed it. A refusal, a refused sign-in app, or a cloud without
// one marks the connection sign_in_needed and is ErrNotReady. A refused
// app is also recorded on the app.
func (s *Service) renew(ctx context.Context, c *Connection, held oauth.Grant, ahead time.Duration) (g oauth.Grant, renewed bool, err error) {
	if !held.Due(s.now(), ahead) {
		return held, false, nil
	}
	if s.grants == nil || s.renewals == nil {
		return oauth.Grant{}, false, fmt.Errorf("%w: renewing a sign-in needs the database", ErrUnavailable)
	}
	leave, err := s.renewals.enter(ctx, c.OrgID+"/"+c.ID)
	if err != nil {
		return oauth.Grant{}, false, err
	}
	defer leave()
	// Whoever went first has most likely renewed it already.
	if latest, readErr := s.readGrant(ctx, c); readErr == nil && !latest.Due(s.now(), ahead) {
		return latest, false, nil
	}
	var lapsed *Connection
	var why string
	lockErr := s.grants.WithConnectionLock(ctx, c.OrgID, c.ID, func() error {
		current, err := s.store.GetConnection(ctx, c.OrgID, c.ID)
		if err != nil {
			return err
		}
		if current.Method != MethodOAuth || current.Generation != c.Generation || current.Message == replacementInProgressMessage {
			return ErrChanged
		}
		latest, err := s.readGrant(ctx, current)
		if err != nil {
			return err
		}
		if !latest.Due(s.now(), ahead) {
			g = latest
			return nil
		}
		app, err := s.app(ctx, c.OrgID, c.Cloud)
		if errors.Is(err, ErrNoApp) {
			why = noAppWhy(c.Cloud)
			lapsed, err = s.needSignIn(ctx, current, why)
			return err
		}
		if err != nil {
			return err
		}
		provider, ok := s.Providers[c.Cloud]
		if !ok {
			return fmt.Errorf("%w: Loomplane has no sign-in for %s", ErrUnavailable, c.Cloud)
		}
		// The cloud may spend the old refresh token before its answer is
		// read, so a caller giving up must not cut the answer off.
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), renewTimeout)
		defer cancel()
		fresh, err := provider.Refresh(refreshCtx, app, latest)
		if errors.Is(err, oauth.ErrAppRejected) {
			why = s.appRejected(ctx, c.OrgID, c.Cloud)
			lapsed, err = s.needSignIn(ctx, current, why)
			return err
		}
		if errors.Is(err, oauth.ErrRefused) {
			why = fmt.Sprintf("%s no longer accepts this sign-in. Sign in again.", c.Cloud)
			lapsed, err = s.needSignIn(ctx, current, why)
			return err
		}
		if err != nil {
			return fmt.Errorf("%w: renew the sign-in: %w", ErrUnavailable, err)
		}
		if fresh.AccessToken == "" {
			return fmt.Errorf("%w: %s renewed the sign-in without an access token", ErrUnavailable, c.Cloud)
		}
		// A cloud that doesn't rotate refresh tokens leaves them out.
		if fresh.RefreshToken == "" {
			fresh.RefreshToken = latest.RefreshToken
		}
		if fresh.Account == nil {
			fresh.Account = latest.Account
		}
		raw, err := json.Marshal(fresh) // #nosec G117 -- the grant is the credential, bound for Vault, never a response.
		if err != nil {
			return fmt.Errorf("encode the renewed sign-in: %w", err)
		}
		// The cloud has spent the old refresh token, so a caller giving up
		// now must not stop the new one being kept.
		putCtx, cancelPut := context.WithTimeout(context.WithoutCancel(ctx), renewTimeout)
		defer cancelPut()
		if err := s.secrets.Put(putCtx, c.OrgID, c.ID, raw); err != nil {
			return err
		}
		g, renewed = fresh, true
		return nil
	})
	if lockErr != nil {
		return oauth.Grant{}, false, lockErr
	}
	if why != "" {
		s.tellLapsed(ctx, lapsed)
		return oauth.Grant{}, false, fmt.Errorf("%w: %s", ErrNotReady, why)
	}
	return g, renewed, nil
}

// noAppWhy is why a sign-in for cloud needs signing in again once the
// cloud's sign-in app is gone.
func noAppWhy(cloud string) string {
	return fmt.Sprintf("%s has no sign-in app set up, so Loomplane can't renew this sign-in. Set up the app, then sign in again.", cloud)
}

// handoutWithoutApp passes err on, unless it is ErrNoApp from building a
// handout for signed-in c. Without its app no session can renew itself,
// so, as renew does, c then needs a new sign-in, Lapsed is told, and it
// is ErrNotReady.
func (s *Service) handoutWithoutApp(ctx context.Context, c *Connection, err error) error {
	if !errors.Is(err, ErrNoApp) {
		return err
	}
	why := noAppWhy(c.Cloud)
	_, lapsed, markErr := s.signInRefused(ctx, c, why, nil)
	if markErr != nil {
		return markErr
	}
	s.tellLapsed(ctx, lapsed)
	return fmt.Errorf("%w: %s", ErrNotReady, why)
}

// needSignIn marks c as needing a new sign-in. It returns c if this call
// changed it, so Lapsed is told once. The account is unchanged, so the
// store keeps the generation and reviewed plans stay valid.
func (s *Service) needSignIn(ctx context.Context, c *Connection, why string) (*Connection, error) {
	if c.Status == StatusSignInNeeded {
		return nil, nil
	}
	c.Status, c.Message, c.UpdatedAt = StatusSignInNeeded, why, s.now().UTC()
	if err := s.store.UpdateConnection(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// tellLapsed passes a connection needSignIn changed to Lapsed. The change
// is already saved, so a caller giving up now must not lose the notice.
func (s *Service) tellLapsed(ctx context.Context, c *Connection) {
	if c != nil && s.Lapsed != nil {
		s.Lapsed(context.WithoutCancel(ctx), c)
	}
}

// signInRefused marks c as needing a new sign-in, such as after the cloud
// refused its token, under the connection's lock, unless it changed since
// c was read. A change is stamped as checked at checked, if given. It
// returns the connection as it is now, and whether this call changed it.
func (s *Service) signInRefused(ctx context.Context, c *Connection, why string, checked *time.Time) (current, lapsed *Connection, err error) {
	mark := func() error {
		latest, getErr := s.store.GetConnection(ctx, c.OrgID, c.ID)
		if getErr != nil {
			return getErr
		}
		if latest.Method != MethodOAuth || latest.Generation != c.Generation || latest.Message == replacementInProgressMessage {
			return ErrChanged
		}
		if latest.Status != StatusSignInNeeded && checked != nil {
			// needSignIn saves only a change, so only a change is stamped.
			latest.CheckedAt = checked
		}
		current = latest
		var markErr error
		lapsed, markErr = s.needSignIn(ctx, latest, why)
		return markErr
	}
	if s.grants == nil {
		err = mark()
	} else {
		err = s.grants.WithConnectionLock(ctx, c.OrgID, c.ID, mark)
	}
	return current, lapsed, err
}

// RefreshDue renews every sign-in that expires within a week, so one
// nobody deploys with stays usable. One that fails doesn't stop the rest.
func (s *Service) RefreshDue(ctx context.Context) (RefreshReport, error) {
	var report RefreshReport
	if s.grants == nil {
		return report, fmt.Errorf("%w: renewing sign-ins needs the database", ErrUnavailable)
	}
	conns, err := s.grants.OAuthConnections(ctx)
	if err != nil {
		return report, err
	}
	for _, c := range conns {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		held, err := s.readGrant(ctx, c)
		if err == nil {
			_, renewed, rerr := s.renew(ctx, c, held, backgroundAhead)
			switch {
			case errors.Is(rerr, ErrNotReady):
				report.Lapsed++
				continue
			case rerr == nil && renewed:
				report.Refreshed++
				continue
			case rerr == nil:
				continue
			}
			err = rerr
		}
		report.Failed++
		s.warn("renew the sign-in of "+c.ID, err)
	}
	return report, nil
}

// RefreshAll runs RefreshDue for the scheduled job and logs what needs
// attention.
func (s *Service) RefreshAll(ctx context.Context) error {
	report, err := s.RefreshDue(ctx)
	if err != nil {
		return err
	}
	if report.Lapsed > 0 || report.Failed > 0 {
		s.warn("renew cloud sign-ins", fmt.Errorf("%d renewed, %d need signing in again, %d failed",
			report.Refreshed, report.Lapsed, report.Failed))
	}
	return nil
}
