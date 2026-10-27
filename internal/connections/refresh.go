package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
)

// refreshAhead is how close to expiry a deployment's sign-in is renewed.
const refreshAhead = 24 * time.Hour

// backgroundAhead is how close to expiry the background job renews a
// sign-in, so one nobody deploys with still stays fresh.
const backgroundAhead = 7 * 24 * time.Hour

// keylessCheckPrefix starts the message of a keyless connection the
// background check failed, so the check knows it may heal it. One a
// Recheck failed doesn't have it.
const keylessCheckPrefix = "Daily check: "

// probeEvery is how long a sign-in that needs no renewing goes between
// asks of the cloud whether it still honors it, so an uninstalled
// integration or a revoked app shows up before a deployment hits it.
const probeEvery = 24 * time.Hour

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
// that could need renewing, or whose signer has left. *PgStore has it.
type grantStore interface {
	OAuthConnections(ctx context.Context) ([]*Connection, error)
	LeftSignIns(ctx context.Context, clouds []string) ([]*Connection, error)
	KeylessConnections(ctx context.Context) ([]*Connection, error)
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

// RefreshReport counts what one pass over the sign-ins did. Probed is how
// many quiet sign-ins the cloud still honors.
type RefreshReport struct{ Refreshed, Lapsed, Left, Failed, Probed int }

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
			return fmt.Errorf("%w: Loomplane has no sign-in for %s", ErrUnavailable, cloudTitle(c.Cloud))
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
			why = noLongerAccepted(c.Cloud)
			lapsed, err = s.needSignIn(ctx, current, why)
			return err
		}
		if err != nil {
			return fmt.Errorf("%w: renew the sign-in: %w", ErrUnavailable, err)
		}
		if fresh.AccessToken == "" {
			return fmt.Errorf("%w: %s renewed the sign-in without an access token", ErrUnavailable, cloudTitle(c.Cloud))
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
		// A lapse saved inside the lock isn't part of its transaction, so
		// it stands even though the lock failed.
		s.tellLapsed(ctx, lapsed)
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
	return fmt.Sprintf("%s has no sign-in app set up, so Loomplane can't renew this sign-in. Set up the app, then sign in again.", cloudTitle(cloud))
}

// noLongerAccepted is why a sign-in for cloud needs signing in again once
// the cloud stops honoring it.
func noLongerAccepted(cloud string) string {
	return fmt.Sprintf("%s no longer accepts this sign-in. Sign in again.", cloudTitle(cloud))
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

// refuseTrust marks keyless c as failed with the cloud's message. It
// returns c if this call changed it, so Lapsed is told once. The account is
// unchanged, so the store keeps the generation.
func (s *Service) refuseTrust(ctx context.Context, c *Connection, why string) (*Connection, error) {
	if c.Status == StatusFailed {
		return nil, nil
	}
	c.Status, c.Message, c.UpdatedAt = StatusFailed, why, s.now().UTC()
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
// If the lock fails after the change was saved, the change stands, so
// Lapsed is told here and the error is returned without it.
func (s *Service) signInRefused(ctx context.Context, c *Connection, why string, checked *time.Time) (current, lapsed *Connection, err error) {
	return s.refusedUnderLock(ctx, c, MethodOAuth, StatusSignInNeeded, why, checked, s.needSignIn)
}

// refusedUnderLock marks c, a connection of method, with mark under the
// connection's lock, unless it changed since c was read. settled is the
// status that needs no marking. A change is stamped as checked at checked,
// if given. See signInRefused for what it returns.
func (s *Service) refusedUnderLock(ctx context.Context, c *Connection, method, settled, why string, checked *time.Time, mark func(context.Context, *Connection, string) (*Connection, error)) (current, lapsed *Connection, err error) {
	run := func() error {
		latest, getErr := s.store.GetConnection(ctx, c.OrgID, c.ID)
		if getErr != nil {
			return getErr
		}
		if latest.Method != method || latest.Generation != c.Generation || latest.Message == replacementInProgressMessage {
			return ErrChanged
		}
		// Someone checked it after the refusal was seen, so that answer is
		// the newer one.
		if checked != nil && latest.CheckedAt != nil && latest.CheckedAt.After(*checked) {
			return ErrChanged
		}
		if latest.Status != settled && checked != nil {
			// mark saves only a change, so only a change is stamped.
			latest.CheckedAt = checked
		}
		current = latest
		var markErr error
		lapsed, markErr = mark(ctx, latest, why)
		return markErr
	}
	if s.grants == nil {
		err = run()
	} else {
		err = s.grants.WithConnectionLock(ctx, c.OrgID, c.ID, run)
	}
	if err != nil && lapsed != nil {
		s.tellLapsed(ctx, lapsed)
		lapsed = nil
	}
	return current, lapsed, err
}

// personalClouds are the clouds whose sign-in acts as the person who
// made it, so it ends being theirs to give when they leave.
var personalClouds = []string{GCP, Azure}

// askAgainWhenLeft marks every sign-in to a personal cloud whose signer
// has left the organization as needing a new one, and tells Lapsed. A
// sign-in someone has since replaced is passed over. One that fails
// doesn't stop the rest.
func (s *Service) askAgainWhenLeft(ctx context.Context, report *RefreshReport) error {
	gone, err := s.grants.LeftSignIns(ctx, personalClouds)
	if err != nil {
		return err
	}
	for _, c := range gone {
		if err := ctx.Err(); err != nil {
			return err
		}
		why := fmt.Sprintf("The person who signed in to %s has left the organization. Sign in again.", c.Name)
		_, lapsed, markErr := s.signInRefused(ctx, c, why, nil)
		switch {
		case errors.Is(markErr, ErrChanged):
			continue
		case markErr != nil:
			report.Failed++
			s.warn("ask for a new sign-in of "+c.ID, markErr)
			continue
		}
		if lapsed != nil {
			report.Left++
		}
		s.tellLapsed(ctx, lapsed)
	}
	return nil
}

// probeQuiet asks the cloud whether the grant it holds for c is still
// honored, unless that was checked within probeEvery. A refusal marks c
// as needing a new sign-in and tells Lapsed; an answer that it is honored
// stamps CheckedAt. Any other failure changes nothing and is counted as
// failed.
func (s *Service) probeQuiet(ctx context.Context, c *Connection, held oauth.Grant, report *RefreshReport) {
	now := s.now().UTC()
	if c.CheckedAt != nil && now.Sub(*c.CheckedAt) < probeEvery {
		return
	}
	fail := func(err error) {
		report.Failed++
		s.warn("check the sign-in of "+c.ID, err)
	}
	provider, ok := s.Providers[c.Cloud]
	if !ok {
		fail(fmt.Errorf("%w: Loomplane has no sign-in for %s", ErrUnavailable, cloudTitle(c.Cloud)))
		return
	}
	var why string
	app, err := s.app(ctx, c.OrgID, c.Cloud)
	switch {
	case errors.Is(err, ErrNoApp):
		why = noAppWhy(c.Cloud)
	case err != nil:
		fail(err)
		return
	default:
		probeCtx, cancel := context.WithTimeout(ctx, renewTimeout)
		err = provider.Probe(probeCtx, app, held)
		cancel()
		switch {
		case errors.Is(err, oauth.ErrAppRejected):
			why = s.appRejected(ctx, c.OrgID, c.Cloud)
		case errors.Is(err, oauth.ErrRefused):
			why = noLongerAccepted(c.Cloud)
		case err != nil:
			fail(err)
			return
		}
	}
	if why == "" {
		switch stampErr := s.stampChecked(ctx, c, now); {
		case errors.Is(stampErr, ErrChanged):
		case stampErr != nil:
			fail(stampErr)
		default:
			report.Probed++
		}
		return
	}
	_, lapsed, err := s.signInRefused(ctx, c, why, &now)
	switch {
	case errors.Is(err, ErrChanged):
	case err != nil:
		fail(err)
	case lapsed != nil:
		report.Lapsed++
		s.tellLapsed(ctx, lapsed)
	}
}

// stampChecked records that the cloud honored c's sign-in at checked,
// under the connection's lock, unless it changed since c was read. Only
// the check time is saved, so the generation stays.
func (s *Service) stampChecked(ctx context.Context, c *Connection, checked time.Time) error {
	return s.stampMethod(ctx, c, MethodOAuth, checked)
}

// stampMethod is stampChecked for a connection of method.
func (s *Service) stampMethod(ctx context.Context, c *Connection, method string, checked time.Time) error {
	return s.grants.WithConnectionLock(ctx, c.OrgID, c.ID, func() error {
		latest, err := s.store.GetConnection(ctx, c.OrgID, c.ID)
		if err != nil {
			return err
		}
		if latest.Method != method || latest.Generation != c.Generation || latest.Status != c.Status || latest.Message == replacementInProgressMessage {
			return ErrChanged
		}
		latest.CheckedAt, latest.UpdatedAt = &checked, checked
		return s.store.UpdateConnection(ctx, latest)
	})
}

// keylessRefusal is a keyless connection whose cloud refused its trust in
// this run, and why, waiting to be marked.
type keylessRefusal struct {
	c   *Connection
	why string
	at  time.Time
}

// checkKeyless asks the cloud, once a day, whether it still trusts
// Loomplane for each keyless connection, with the handout a deployment
// would get. An answer that it is trusted stamps CheckedAt, and moves a
// connection this check failed itself back to ready. Any other failure
// changes nothing and is counted as failed. One that fails doesn't stop
// the rest.
//
// A refusal marks the connection failed with the cloud's message and
// tells Lapsed once, unless it looks like Loomplane's own fault: its
// issuer isn't serving the key it signs with at its public URL, which is
// asked once, before anything is marked, or every connection to one cloud
// was refused, two or more of them. Then the refusals are counted as
// failed, nothing changes, and it warns once.
func (s *Service) checkKeyless(ctx context.Context, report *RefreshReport) error {
	conns, err := s.grants.KeylessConnections(ctx)
	if err != nil {
		return err
	}
	probed := map[string]int{}
	refused := map[string][]keylessRefusal{}
	for _, c := range conns {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, asked := s.probeKeyless(ctx, c, report)
		if !asked || c.Status == StatusFailed {
			// One this check failed already doesn't count toward the
			// breaker, and its refusal has nothing left to mark.
			continue
		}
		probed[c.Cloud]++
		if r != nil {
			refused[c.Cloud] = append(refused[c.Cloud], *r)
		}
	}
	var pending []keylessRefusal
	for _, cloud := range slices.Sorted(maps.Keys(refused)) {
		list := refused[cloud]
		if len(list) >= 2 && len(list) == probed[cloud] {
			report.Failed += len(list)
			s.warn("check keyless connections to "+cloud, fmt.Errorf("%s refused all %d of them, which looks like Loomplane's issuer and not their trusts, so none is marked failed", cloudTitle(cloud), len(list)))
			continue
		}
		pending = append(pending, list...)
	}
	if len(pending) == 0 {
		return nil
	}
	if s.IssuerServing != nil {
		if err := s.IssuerServing(ctx); err != nil {
			report.Failed += len(pending)
			s.warn(fmt.Sprintf("check Loomplane's issuer before marking %d keyless connections failed; none is marked", len(pending)), err)
			return nil
		}
	}
	for _, r := range pending {
		s.markKeyless(ctx, r, report)
	}
	return nil
}

// probeKeyless asks c's cloud whether it trusts Loomplane, unless that
// was asked within probeEvery. asked says whether it was. A refusal of a
// connection that isn't failed yet is returned, for checkKeyless to mark.
// One this check failed and the cloud still refuses is stamped, so it's
// asked again a day on.
func (s *Service) probeKeyless(ctx context.Context, c *Connection, report *RefreshReport) (refusal *keylessRefusal, asked bool) {
	now := s.now().UTC()
	if c.CheckedAt != nil && now.Sub(*c.CheckedAt) < probeEvery {
		return nil, false
	}
	fail := func(err error) {
		report.Failed++
		s.warn("check the keyless trust of "+c.ID, err)
	}
	checker, err := s.checker(c.Cloud)
	if err != nil {
		fail(err)
		return nil, true
	}
	cred, err := s.keylessHandout(ctx, c)
	if err != nil {
		fail(err)
		return nil, true
	}
	checkCtx, cancel := context.WithTimeout(ctx, renewTimeout)
	res, err := s.check(checkCtx, c.Cloud, checker, cred, c.Details)
	cancel()
	switch {
	case err == nil && c.Status == StatusFailed:
		switch healErr := s.healKeyless(ctx, c, res, now); {
		case errors.Is(healErr, ErrChanged):
		case healErr != nil:
			fail(healErr)
		default:
			report.Probed++
		}
	case err == nil:
		switch stampErr := s.stampMethod(ctx, c, MethodKeyless, now); {
		case errors.Is(stampErr, ErrChanged):
		case stampErr != nil:
			fail(stampErr)
		default:
			report.Probed++
		}
	case errors.Is(err, ErrInvalid) && c.Status == StatusFailed:
		if stampErr := s.stampMethod(ctx, c, MethodKeyless, now); stampErr != nil && !errors.Is(stampErr, ErrChanged) {
			fail(stampErr)
		}
	case errors.Is(err, ErrInvalid):
		why := keylessCheckPrefix + strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": ")
		return &keylessRefusal{c: c, why: why, at: now}, true
	default:
		fail(err)
	}
	return nil, true
}

// markKeyless marks a refused keyless connection failed, under its lock,
// and tells Lapsed once.
func (s *Service) markKeyless(ctx context.Context, r keylessRefusal, report *RefreshReport) {
	_, lapsed, err := s.refusedUnderLock(ctx, r.c, MethodKeyless, StatusFailed, r.why, &r.at, s.refuseTrust)
	switch {
	case errors.Is(err, ErrChanged):
	case err != nil:
		report.Failed++
		s.warn("check the keyless trust of "+r.c.ID, err)
	case lapsed != nil:
		report.Lapsed++
		s.tellLapsed(ctx, lapsed)
	}
}

// healKeyless moves keyless c, which this check failed, back to what the
// cloud just answered, under the connection's lock, unless it changed since
// c was read or someone has checked it since. The account is unchanged, so
// the generation stays, and nobody is told.
func (s *Service) healKeyless(ctx context.Context, c *Connection, res Check, checked time.Time) error {
	return s.grants.WithConnectionLock(ctx, c.OrgID, c.ID, func() error {
		latest, err := s.store.GetConnection(ctx, c.OrgID, c.ID)
		if err != nil {
			return err
		}
		if latest.Method != MethodKeyless || latest.Generation != c.Generation || latest.Status != StatusFailed || !strings.HasPrefix(latest.Message, keylessCheckPrefix) {
			return ErrChanged
		}
		latest.Status, latest.Missing, latest.Message = StatusReady, res.Missing, ""
		if len(res.Missing) > 0 {
			latest.Status = StatusIncomplete
		}
		latest.CheckedAt, latest.UpdatedAt = &checked, checked
		return s.store.UpdateConnection(ctx, latest)
	})
}

// RefreshDue first asks for a new sign-in wherever the person who made it
// has left, then renews every sign-in that expires within a week, so one
// nobody deploys with stays usable. A sign-in with nothing to renew is
// probed once a day instead, as is each keyless connection's trust. One
// that fails doesn't stop the rest.
func (s *Service) RefreshDue(ctx context.Context) (RefreshReport, error) {
	var report RefreshReport
	if s.grants == nil {
		return report, fmt.Errorf("%w: renewing sign-ins needs the database", ErrUnavailable)
	}
	if err := s.askAgainWhenLeft(ctx, &report); err != nil {
		if ctx.Err() != nil {
			return report, err
		}
		// Renewing doesn't depend on who has left, so it carries on.
		report.Failed++
		s.warn("find sign-ins whose signer left", err)
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
		if err == nil && !held.Due(s.now(), backgroundAhead) {
			// Renewing talks to the cloud for a grant that is due; one
			// that isn't has to be asked about instead.
			s.probeQuiet(ctx, c, held, &report)
			continue
		}
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
	if err := s.checkKeyless(ctx, &report); err != nil {
		if ctx.Err() != nil {
			return report, err
		}
		report.Failed++
		s.warn("find keyless connections to check", err)
	}
	return report, nil
}

// RefreshAll runs RefreshDue for the scheduled job and logs what needs
// attention.
func (s *Service) RefreshAll(ctx context.Context) error {
	// Abandoned sign-ins are swept whatever else happens.
	if s.Apps != nil {
		if _, err := s.Apps.SweepSignIns(ctx, s.now()); err != nil {
			s.warn("sweep expired sign-ins", err)
		}
	}
	// So are the reservations nobody used, and the signing keys are turned
	// over. Neither stops the rest.
	if s.Apps != nil {
		if _, err := s.Apps.SweepReservations(ctx, s.now()); err != nil {
			s.warn("sweep expired keyless reservations", err)
		}
	}
	if s.Rotate != nil {
		if err := s.Rotate(ctx); err != nil {
			s.warn("rotate the issuer's signing keys", err)
		}
	}
	report, err := s.RefreshDue(ctx)
	if err != nil {
		return err
	}
	if report.Lapsed > 0 || report.Left > 0 || report.Failed > 0 {
		s.warn("renew cloud sign-ins", fmt.Errorf("%d renewed, %d checked, %d need signing in again or lost their keyless trust, %d lost the person who signed in, %d failed",
			report.Refreshed, report.Probed, report.Lapsed, report.Left, report.Failed))
	}
	return nil
}
