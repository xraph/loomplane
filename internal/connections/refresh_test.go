package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
)

// lockingStore is a memStore that is safe across goroutines and has the
// per-connection lock and the list of sign-ins that renewing needs.
type lockingStore struct {
	*memStore
	mu    sync.Mutex
	held  map[string]*sync.Mutex
	locks int
	// departed are the users no longer in the organization.
	departed map[string]bool
	// entrants counts callers inside WithConnectionLock or waiting on it,
	// as a pooled database connection would be; most is its peak.
	entrants, most int
}

func (l *lockingStore) GetConnection(ctx context.Context, org, id string) (*Connection, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.memStore.GetConnection(ctx, org, id)
}

func (l *lockingStore) UpdateConnection(ctx context.Context, c *Connection) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.memStore.UpdateConnection(ctx, c)
}

func (l *lockingStore) WithConnectionLock(_ context.Context, org, id string, fn func() error) error {
	l.mu.Lock()
	if l.held == nil {
		l.held = map[string]*sync.Mutex{}
	}
	key := org + "/" + id
	if l.held[key] == nil {
		l.held[key] = &sync.Mutex{}
	}
	lock := l.held[key]
	l.locks++
	l.entrants++
	l.most = max(l.most, l.entrants)
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.entrants--
		l.mu.Unlock()
	}()
	lock.Lock()
	defer lock.Unlock()
	return fn()
}

func (l *lockingStore) stats() (locks, most int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.locks, l.most
}

func (l *lockingStore) OAuthConnections(context.Context) ([]*Connection, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []*Connection
	for _, c := range l.rows {
		if c.Method == MethodOAuth && c.Status != StatusSignInNeeded {
			out = append(out, &c)
		}
	}
	return out, nil
}

func (l *lockingStore) KeylessConnections(context.Context) ([]*Connection, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []*Connection
	for _, c := range l.rows {
		if c.Method == MethodKeyless && (c.Status == StatusReady || c.Status == StatusIncomplete ||
			c.Status == StatusFailed && strings.HasPrefix(c.Message, keylessCheckPrefix)) {
			out = append(out, &c)
		}
	}
	return out, nil
}

func (l *lockingStore) LeftSignIns(_ context.Context, clouds []string) ([]*Connection, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []*Connection
	for _, c := range l.rows {
		if c.Method == MethodOAuth && c.Status != StatusSignInNeeded && slices.Contains(clouds, c.Cloud) && c.SignedInBy != "" && l.departed[c.SignedInBy] {
			out = append(out, &c)
		}
	}
	return out, nil
}

// brokenMembers is a lockingStore whose membership query fails.
type brokenMembers struct{ *lockingStore }

func (brokenMembers) LeftSignIns(context.Context, []string) ([]*Connection, error) {
	return nil, errors.New("authsome_members is unreachable")
}

type lockingSecrets struct {
	*memSecrets
	mu sync.Mutex
	// unbounded counts writes whose context had no deadline.
	unbounded int
}

func (l *lockingSecrets) Put(ctx context.Context, org, id string, v []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := ctx.Deadline(); !ok {
		l.unbounded++
	}
	return l.memSecrets.Put(ctx, org, id, v)
}

func (l *lockingSecrets) Reveal(ctx context.Context, org, id string) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.memSecrets.Reveal(ctx, org, id)
}

// renewer is a Provider whose Refresh hands back a grant per refresh
// token, or an error, and counts its calls.
type renewer struct {
	noProvider
	mu     sync.Mutex
	calls  int
	tokens []string
	apps   []oauth.App
	grants map[string]oauth.Grant
	errs   map[string]error
	delay  time.Duration
	// started hears of each call, if set; block holds every call until
	// it's closed.
	started chan struct{}
	block   chan struct{}
	// during runs inside each call, as the cloud answers. honorCancel
	// makes a call fail once its context ends, as an HTTP client does.
	during      func()
	honorCancel bool
	// unbounded counts calls whose context had no deadline.
	unbounded int
	// probed lists the access tokens Probe was asked about; probeErrs
	// maps an access token to what Probe answers for it.
	probed    []string
	probeErrs map[string]error
}

func (r *renewer) Probe(_ context.Context, _ oauth.App, g oauth.Grant) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.probed = append(r.probed, g.AccessToken)
	return r.probeErrs[g.AccessToken]
}

func (r *renewer) Refresh(ctx context.Context, app oauth.App, held oauth.Grant) (oauth.Grant, error) {
	refreshToken := held.RefreshToken
	if r.started != nil {
		r.started <- struct{}{}
	}
	if r.block != nil {
		<-r.block
	}
	time.Sleep(r.delay)
	if r.during != nil {
		r.during()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := ctx.Deadline(); !ok {
		r.unbounded++
	}
	if r.honorCancel && ctx.Err() != nil {
		return oauth.Grant{}, ctx.Err()
	}
	r.calls++
	r.tokens = append(r.tokens, refreshToken)
	r.apps = append(r.apps, app)
	if err, ok := r.errs[refreshToken]; ok {
		return oauth.Grant{}, err
	}
	return r.grants[refreshToken], nil
}

type refreshRig struct {
	*signInRig
	locked  *lockingStore
	vault   *lockingSecrets
	renewer *renewer
	mu      sync.Mutex
	lapsed  []*Connection
	warned  []string
	// attached counts Lapsed calls whose context a caller could cancel.
	attached int
}

func newRefreshRig(t *testing.T) *refreshRig {
	t.Helper()
	base := newSignInRig(t)
	r := &refreshRig{signInRig: base, locked: &lockingStore{memStore: base.store}, vault: &lockingSecrets{memSecrets: base.secrets},
		renewer: &renewer{grants: map[string]oauth.Grant{}, errs: map[string]error{}}}
	old := base.s
	r.s = NewService(r.locked, r.vault, old.checkers, old.authz, old.audit, func(msg string, err error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.warned = append(r.warned, msg+": "+err.Error())
	})
	r.s.now = old.now
	r.s.Providers = map[string]Provider{DigitalOcean: r.renewer}
	r.s.ConfigApps, r.s.CallbackURL, r.s.Apps, r.s.AppSecrets = old.ConfigApps, old.CallbackURL, old.Apps, old.AppSecrets
	r.s.Lapsed = func(ctx context.Context, c *Connection) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if ctx.Done() != nil {
			r.attached++
		}
		r.lapsed = append(r.lapsed, c)
	}
	return r
}

// signedIn stores a ready DigitalOcean connection signed in with token,
// whose grant expires at expiry and renews with "rt-"+token.
func (r *refreshRig) signedIn(t *testing.T, id, token string, expiry time.Time) {
	t.Helper()
	r.store.rows[id] = Connection{ID: id, OrgID: "org", Cloud: DigitalOcean, Name: id, Method: MethodOAuth, Account: "Acme",
		Details: map[string]string{"team_uuid": "team-1", "account_uuid": "acct-1"}, Status: StatusReady, Generation: 3}
	r.keep(t, id, oauth.Grant{AccessToken: token, RefreshToken: "rt-" + token, Expiry: expiry, Account: map[string]string{"team_uuid": "team-1", "team_name": "Acme", "account_uuid": "acct-1"}})
}

func (r *refreshRig) keep(t *testing.T, id string, g oauth.Grant) {
	t.Helper()
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	r.secrets.kept["org/"+id] = raw
}

func (r *refreshRig) kept(t *testing.T, id string) oauth.Grant {
	t.Helper()
	var g oauth.Grant
	if err := json.Unmarshal(r.secrets.kept["org/"+id], &g); err != nil {
		t.Fatalf("vault holds %q: %v", r.secrets.kept["org/"+id], err)
	}
	return g
}

func TestNewServiceFindsTheGrantStore(t *testing.T) {
	if s := NewService(&lockingStore{memStore: &memStore{}}, nil, nil, nil, nil, nil); s.grants == nil {
		t.Fatal("a store with a connection lock wasn't used to renew sign-ins")
	}
	if s := NewService(&memStore{}, nil, nil, nil, nil, nil); s.grants != nil {
		t.Fatal("a store without a connection lock was used to renew sign-ins")
	}
}

func TestCredentialHandsOutTheAccessToken(t *testing.T) {
	r := newRefreshRig(t)
	r.signedIn(t, "conn_do", "at-1", r.clock.Add(48*time.Hour))
	c, cred, err := r.s.Credential(t.Context(), "org", "conn_do")
	if err != nil {
		t.Fatal(err)
	}
	if string(cred) != "at-1" || c.ID != "conn_do" {
		t.Fatalf("credential %q for %+v", cred, c)
	}
	if r.renewer.calls != 0 || r.locked.locks != 0 {
		t.Fatalf("a grant two days from expiry was renewed: calls=%d locks=%d", r.renewer.calls, r.locked.locks)
	}

	// A token connection still hands out what Vault holds.
	tokenConnection(r.signInRig)
	if _, cred, err := r.s.Credential(t.Context(), "org", "conn_do"); err != nil || string(cred) != "dop_v1_team" {
		t.Fatalf("token connection: %q %v", cred, err)
	}
}

func TestCredentialRefreshesADueGrant(t *testing.T) {
	r := newRefreshRig(t)
	r.signedIn(t, "conn_do", "at-1", r.clock.Add(time.Hour))
	r.renewer.grants["rt-at-1"] = oauth.Grant{AccessToken: "at-2", RefreshToken: "rt-at-2", Expiry: r.clock.Add(30 * 24 * time.Hour)}
	_, cred, err := r.s.Credential(t.Context(), "org", "conn_do")
	if err != nil {
		t.Fatal(err)
	}
	if string(cred) != "at-2" {
		t.Fatalf("credential %q", cred)
	}
	if len(r.renewer.tokens) != 1 || r.renewer.tokens[0] != "rt-at-1" || r.renewer.apps[0].ClientID != "cfg-id" {
		t.Fatalf("refresh got tokens=%v apps=%v", r.renewer.tokens, r.renewer.apps)
	}
	g := r.kept(t, "conn_do")
	if g.AccessToken != "at-2" || g.RefreshToken != "rt-at-2" || !g.Expiry.Equal(r.clock.Add(30*24*time.Hour)) || g.Account["team_uuid"] != "team-1" {
		t.Fatalf("vault holds %+v", g)
	}
	if row := r.store.rows["conn_do"]; row.Generation != 3 || row.Status != StatusReady {
		t.Fatalf("renewing changed the row: %+v", row)
	}

	// A cloud that doesn't rotate the refresh token keeps the old one.
	r.signedIn(t, "conn_do", "at-3", r.clock.Add(time.Hour))
	r.renewer.grants["rt-at-3"] = oauth.Grant{AccessToken: "at-4", Expiry: r.clock.Add(30 * 24 * time.Hour)}
	if _, cred, err := r.s.Credential(t.Context(), "org", "conn_do"); err != nil || string(cred) != "at-4" {
		t.Fatalf("credential %q %v", cred, err)
	}
	if g := r.kept(t, "conn_do"); g.RefreshToken != "rt-at-3" {
		t.Fatalf("vault lost the refresh token: %+v", g)
	}
}

func TestCredentialRefreshesOnceAcrossConcurrentCallers(t *testing.T) {
	r := newRefreshRig(t)
	r.signedIn(t, "conn_do", "at-1", r.clock.Add(time.Hour))
	r.renewer.grants["rt-at-1"] = oauth.Grant{AccessToken: "at-2", RefreshToken: "rt-at-2", Expiry: r.clock.Add(30 * 24 * time.Hour)}
	r.renewer.delay = 20 * time.Millisecond
	var wg sync.WaitGroup
	creds := make([]string, 8)
	errs := make([]error, 8)
	for i := range 8 {
		wg.Go(func() {
			_, cred, err := r.s.Credential(t.Context(), "org", "conn_do")
			creds[i], errs[i] = string(cred), err
		})
	}
	wg.Wait()
	for i := range 8 {
		if errs[i] != nil || creds[i] != "at-2" {
			t.Fatalf("caller %d: %q %v", i, creds[i], errs[i])
		}
	}
	if r.renewer.calls != 1 {
		t.Fatalf("the single-use refresh token was spent %d times", r.renewer.calls)
	}
}

func TestCredentialKeepsAValidTokenWhenRefreshCantReachTheCloud(t *testing.T) {
	r := newRefreshRig(t)
	r.signedIn(t, "conn_do", "at-1", r.clock.Add(time.Hour))
	r.renewer.errs["rt-at-1"] = errors.New("dial tcp: connection refused")
	before := string(r.secrets.kept["org/conn_do"])
	_, cred, err := r.s.Credential(t.Context(), "org", "conn_do")
	if err != nil || string(cred) != "at-1" {
		t.Fatalf("credential %q %v", cred, err)
	}
	if string(r.secrets.kept["org/conn_do"]) != before || r.store.rows["conn_do"].Status != StatusReady || len(r.lapsed) != 0 {
		t.Fatalf("a failed renewal changed something: row=%+v lapsed=%d", r.store.rows["conn_do"], len(r.lapsed))
	}
	if len(r.warned) != 1 {
		t.Fatalf("warned %v", r.warned)
	}

	// Once the token has expired there is nothing to fall back on.
	r.signedIn(t, "conn_do", "at-1", r.clock.Add(-time.Minute))
	_, cred, err = r.s.Credential(t.Context(), "org", "conn_do")
	if !errors.Is(err, ErrUnavailable) || cred != nil {
		t.Fatalf("expired: %q %v", cred, err)
	}
	if r.store.rows["conn_do"].Status != StatusReady {
		t.Fatalf("an unreachable cloud lapsed the sign-in: %+v", r.store.rows["conn_do"])
	}
	for _, msg := range append(r.warned, err.Error()) {
		if strings.Contains(msg, "at-1") {
			t.Fatalf("a token leaked: %s", msg)
		}
	}
}

func TestRefusedRefreshNeedsSignIn(t *testing.T) {
	r := newRefreshRig(t)
	r.signedIn(t, "conn_do", "at-1", r.clock.Add(time.Hour))
	r.renewer.errs["rt-at-1"] = oauth.ErrRefused
	_, cred, err := r.s.Credential(t.Context(), "org", "conn_do")
	if !errors.Is(err, ErrNotReady) || cred != nil {
		t.Fatalf("got %q %v", cred, err)
	}
	if strings.Contains(err.Error(), "at-1") {
		t.Fatalf("a token leaked: %v", err)
	}
	row := r.store.rows["conn_do"]
	if row.Status != StatusSignInNeeded || row.Message == "" || row.Generation != 3 {
		t.Fatalf("row: %+v", row)
	}
	if len(r.lapsed) != 1 || r.lapsed[0].ID != "conn_do" || r.lapsed[0].Status != StatusSignInNeeded {
		t.Fatalf("lapsed: %v", r.lapsed)
	}
	// The connection is already marked, so a caller giving up now must not
	// lose the notice.
	if r.attached != 0 {
		t.Fatal("Lapsed ran on a context the caller could cancel")
	}
	if _, _, err := r.s.Credential(t.Context(), "org", "conn_do"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("again: %v", err)
	}
	if len(r.lapsed) != 1 || r.renewer.calls != 1 {
		t.Fatalf("lapsed %d times, refreshed %d times", len(r.lapsed), r.renewer.calls)
	}
}

func TestRefreshWithoutAnAppNeedsSignIn(t *testing.T) {
	r := newRefreshRig(t)
	r.s.ConfigApps = nil
	r.signedIn(t, "conn_do", "at-1", r.clock.Add(time.Hour))
	if _, _, err := r.s.Credential(t.Context(), "org", "conn_do"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("got %v", err)
	}
	row := r.store.rows["conn_do"]
	if row.Status != StatusSignInNeeded || !strings.Contains(row.Message, "sign-in app") || row.Generation != 3 {
		t.Fatalf("row: %+v", row)
	}
	if r.renewer.calls != 0 || len(r.lapsed) != 1 {
		t.Fatalf("calls=%d lapsed=%d", r.renewer.calls, len(r.lapsed))
	}
}

func TestRecheckRenewsASignInFirst(t *testing.T) {
	r := newRefreshRig(t)
	r.signedIn(t, "conn_do", "expired", r.clock.Add(time.Hour))
	r.renewer.grants["rt-expired"] = oauth.Grant{AccessToken: "at-team-1", RefreshToken: "rt-2", Expiry: r.clock.Add(30 * 24 * time.Hour)}
	c, err := r.s.Recheck(t.Context(), starter, "conn_do")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusReady || r.renewer.calls != 1 || r.kept(t, "conn_do").AccessToken != "at-team-1" {
		t.Fatalf("connection %+v, calls %d", c, r.renewer.calls)
	}

	r.signedIn(t, "conn_do", "at-team-1", r.clock.Add(time.Hour))
	r.renewer.errs["rt-at-team-1"] = oauth.ErrRefused
	c, err = r.s.Recheck(t.Context(), starter, "conn_do")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusSignInNeeded || r.store.rows["conn_do"].Status != StatusSignInNeeded || len(r.lapsed) != 1 {
		t.Fatalf("connection %+v, lapsed %d", c, len(r.lapsed))
	}
}

// Revoking Loomplane in the cloud kills the access token at once, before
// it's due for renewal. Checking it again must ask for a sign-in, not
// call the connection refused.
func TestRecheckOfARevokedSignInNeedsSignIn(t *testing.T) {
	r := newRefreshRig(t)
	r.signedIn(t, "conn_do", "revoked", r.clock.Add(30*24*time.Hour))
	c, err := r.s.Recheck(t.Context(), starter, "conn_do")
	if err != nil {
		t.Fatal(err)
	}
	row := r.store.rows["conn_do"]
	for _, got := range []Connection{*c, row} {
		if got.Status != StatusSignInNeeded || !strings.Contains(got.Message, "refused the sign-in") || got.Generation != 3 {
			t.Fatalf("connection: %+v", got)
		}
		if strings.Contains(got.Message, "revoked") {
			t.Fatalf("a token leaked: %q", got.Message)
		}
	}
	if r.renewer.calls != 0 {
		t.Fatalf("a grant a month from expiry was renewed %d times", r.renewer.calls)
	}
	if len(r.lapsed) != 1 || r.lapsed[0].ID != "conn_do" || r.attached != 0 {
		t.Fatalf("lapsed %v, %d on a cancellable context", r.lapsed, r.attached)
	}
	if n := len(r.audit.actions); n == 0 || r.audit.actions[n-1] != "cloud_connection.check" {
		t.Fatalf("audit: %v", r.audit.actions)
	}

	// Checking again changes nothing and tells nobody twice.
	if c, err = r.s.Recheck(t.Context(), starter, "conn_do"); err != nil || c.Status != StatusSignInNeeded {
		t.Fatalf("again: %+v %v", c, err)
	}
	if len(r.lapsed) != 1 {
		t.Fatalf("lapsed %d times", len(r.lapsed))
	}

	// A refused token connection is still refused, not a sign-in.
	tokenConnection(r.signInRig)
	r.secrets.kept["org/conn_do"] = []byte("dop_v1_revoked")
	if c, err = r.s.Recheck(t.Context(), starter, "conn_do"); err != nil || c.Status != StatusFailed {
		t.Fatalf("token connection: %+v %v", c, err)
	}
	if len(r.lapsed) != 1 {
		t.Fatalf("a token connection lapsed: %v", r.lapsed)
	}
}

// A cloud that is down changes nothing about a sign-in, and a grant that
// can't be read is a failed connection, not one to sign in to again.
func TestRecheckKeepsASignInThroughAnOutage(t *testing.T) {
	r := newRefreshRig(t)
	r.signedIn(t, "conn_do", "at-outage", r.clock.Add(30*24*time.Hour))
	if _, err := r.s.Recheck(t.Context(), starter, "conn_do"); !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvalid) {
		t.Fatalf("digitalocean down: %v", err)
	}
	if row := r.store.rows["conn_do"]; row.Status != StatusReady || row.Message != "" {
		t.Fatalf("an outage changed the row: %+v", row)
	}

	r.signedIn(t, "conn_do", "not a token", r.clock.Add(30*24*time.Hour))
	c, err := r.s.Recheck(t.Context(), starter, "conn_do")
	if err != nil || c.Status != StatusFailed || r.store.rows["conn_do"].Status != StatusFailed {
		t.Fatalf("unreadable grant: %+v %v", c, err)
	}
	if len(r.lapsed) != 0 {
		t.Fatalf("lapsed %v", r.lapsed)
	}

	v := newVercelSignInRig(t)
	v.store.rows["conn_v"] = Connection{ID: "conn_v", OrgID: "org", Cloud: Vercel, Name: "Acme", Method: MethodOAuth,
		Account: "slug-team_one", Details: map[string]string{"team_id": "team_one"}, Status: StatusReady, Generation: 1}
	raw, err := json.Marshal(oauth.Grant{AccessToken: "vcp-down", Account: map[string]string{"team_id": "team_one"}})
	if err != nil {
		t.Fatal(err)
	}
	v.secrets.kept["org/conn_v"] = raw
	if _, err = v.s.Recheck(t.Context(), starter, "conn_v"); !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvalid) {
		t.Fatalf("vercel down: %v", err)
	}
	if row := v.store.rows["conn_v"]; row.Status != StatusReady || row.Message != "" {
		t.Fatalf("an outage changed the row: %+v", row)
	}
}

// DigitalOcean spends the old refresh token as soon as it answers, so a
// caller giving up mid-renewal must not lose the new one.
func TestARenewalTheCallerAbandonsIsStillKept(t *testing.T) {
	r := newRefreshRig(t)
	r.signedIn(t, "conn_do", "at-1", r.clock.Add(time.Hour))
	r.renewer.grants["rt-at-1"] = oauth.Grant{AccessToken: "at-2", RefreshToken: "rt-at-2", Expiry: r.clock.Add(30 * 24 * time.Hour)}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r.renewer.during, r.renewer.honorCancel = cancel, true
	// The caller gave up, so what it gets back doesn't matter. What Vault
	// keeps does.
	_, _, _ = r.s.Credential(ctx, "org", "conn_do")
	if g := r.kept(t, "conn_do"); g.RefreshToken != "rt-at-2" || g.AccessToken != "at-2" {
		t.Fatalf("vault holds %+v", g)
	}
	if r.renewer.unbounded != 0 || r.vault.unbounded != 0 {
		t.Fatalf("unbounded calls: refresh %d, vault %d", r.renewer.unbounded, r.vault.unbounded)
	}
}

func TestRefreshDueRefreshesOnlyWhatsDue(t *testing.T) {
	r := newRefreshRig(t)
	r.signedIn(t, "conn_later", "later", r.clock.Add(30*24*time.Hour))
	r.signedIn(t, "conn_soon", "soon", r.clock.Add(3*24*time.Hour))
	r.signedIn(t, "conn_revoked", "revoked", r.clock.Add(2*24*time.Hour))
	r.signedIn(t, "conn_down", "down", r.clock.Add(2*24*time.Hour))
	r.signedIn(t, "conn_lapsed", "lapsed", r.clock.Add(time.Hour))
	row := r.store.rows["conn_lapsed"]
	row.Status = StatusSignInNeeded
	r.store.rows["conn_lapsed"] = row
	tokenConnection(r.signInRig)
	r.renewer.grants["rt-soon"] = oauth.Grant{AccessToken: "soon-2", RefreshToken: "rt-soon-2", Expiry: r.clock.Add(30 * 24 * time.Hour)}
	r.renewer.errs["rt-revoked"] = oauth.ErrRefused
	r.renewer.errs["rt-down"] = errors.New("503")

	report, err := r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report != (RefreshReport{Refreshed: 1, Lapsed: 1, Failed: 1, Probed: 1}) {
		t.Fatalf("report %+v", report)
	}
	if r.renewer.calls != 3 {
		t.Fatalf("refreshed %v", r.renewer.tokens)
	}
	if r.kept(t, "conn_soon").AccessToken != "soon-2" || r.kept(t, "conn_later").AccessToken != "later" {
		t.Fatal("vault holds the wrong grants")
	}
	if r.store.rows["conn_revoked"].Status != StatusSignInNeeded || r.store.rows["conn_down"].Status != StatusReady || len(r.lapsed) != 1 {
		t.Fatalf("revoked=%+v down=%+v", r.store.rows["conn_revoked"], r.store.rows["conn_down"])
	}

	if err := r.s.RefreshAll(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshNeedsTheConnectionLock(t *testing.T) {
	r := newRefreshRig(t)
	r.s.grants = nil
	r.signedIn(t, "conn_do", "at-1", r.clock.Add(-time.Minute))
	if _, _, err := r.s.Credential(t.Context(), "org", "conn_do"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	if r.renewer.calls != 0 {
		t.Fatal("renewed without the lock")
	}
	if _, err := r.s.RefreshDue(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("refresh due: %v", err)
	}
}

func TestOnlyOneCallerPerConnectionWaitsOnTheDatabaseLock(t *testing.T) {
	r := newRefreshRig(t)
	r.signedIn(t, "conn_do", "at-1", r.clock.Add(time.Hour))
	r.renewer.grants["rt-at-1"] = oauth.Grant{AccessToken: "at-2", RefreshToken: "rt-at-2", Expiry: r.clock.Add(30 * 24 * time.Hour)}
	r.renewer.delay = 30 * time.Millisecond
	var wg sync.WaitGroup
	errs := make([]error, 32)
	for i := range errs {
		wg.Go(func() { _, _, errs[i] = r.s.Credential(t.Context(), "org", "conn_do") })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	if _, most := r.locked.stats(); most != 1 {
		t.Fatalf("%d callers held or waited on the database lock at once", most)
	}
	if r.renewer.calls != 1 {
		t.Fatalf("refreshed %d times", r.renewer.calls)
	}
}

func TestACallerGivingUpInTheQueueLeavesPromptly(t *testing.T) {
	r := newRefreshRig(t)
	r.signedIn(t, "conn_do", "at-1", r.clock.Add(time.Hour))
	r.renewer.grants["rt-at-1"] = oauth.Grant{AccessToken: "at-2", RefreshToken: "rt-at-2", Expiry: r.clock.Add(30 * 24 * time.Hour)}
	r.renewer.started, r.renewer.block = make(chan struct{}, 1), make(chan struct{})
	leader := make(chan error, 1)
	go func() {
		_, _, err := r.s.Credential(t.Context(), "org", "conn_do")
		leader <- err
	}()
	<-r.renewer.started

	ctx, cancel := context.WithCancel(t.Context())
	follower := make(chan error, 1)
	go func() {
		_, _, err := r.s.Credential(ctx, "org", "conn_do")
		follower <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-follower:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("follower: %v", err)
		}
	case <-time.After(time.Second):
		close(r.renewer.block)
		t.Fatal("a cancelled caller stayed queued")
	}
	if locks, _ := r.locked.stats(); locks != 1 {
		t.Fatalf("the cancelled caller reached the database lock: %d entries", locks)
	}
	close(r.renewer.block)
	if err := <-leader; err != nil {
		t.Fatal(err)
	}
	// The slots it waited for are free again.
	r.signedIn(t, "conn_do", "at-5", r.clock.Add(time.Hour))
	r.renewer.grants["rt-at-5"] = oauth.Grant{AccessToken: "at-6", Expiry: r.clock.Add(30 * 24 * time.Hour)}
	r.renewer.started = nil
	if _, cred, err := r.s.Credential(t.Context(), "org", "conn_do"); err != nil || string(cred) != "at-6" {
		t.Fatalf("after: %q %v", cred, err)
	}
}

func TestRenewalsAcrossConnectionsAreCapped(t *testing.T) {
	r := newRefreshRig(t)
	r.renewer.delay = 50 * time.Millisecond
	ids := make([]string, 3*maxRenewals)
	for i := range ids {
		ids[i] = "conn_" + string(rune('a'+i))
		token := "tok-" + ids[i]
		r.signedIn(t, ids[i], token, r.clock.Add(time.Hour))
		r.renewer.grants["rt-"+token] = oauth.Grant{AccessToken: token + "-2", Expiry: r.clock.Add(30 * 24 * time.Hour)}
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		for range 3 {
			wg.Go(func() {
				if _, cred, err := r.s.Credential(t.Context(), "org", id); err != nil || string(cred) != "tok-"+id+"-2" {
					t.Errorf("%s: %q %v", id, cred, err)
				}
			})
		}
	}
	wg.Wait()
	if _, most := r.locked.stats(); most > maxRenewals || most < 2 {
		t.Fatalf("%d renewals held or waited on the database at once; the cap is %d", most, maxRenewals)
	}
	if r.renewer.calls != len(ids) {
		t.Fatalf("refreshed %d times for %d connections", r.renewer.calls, len(ids))
	}
}

// vercelSignIn stores a ready Vercel connection, conn_vercel, whose
// grant never expires, last checked at checked.
func (r *refreshRig) vercelSignIn(t *testing.T, checked *time.Time) {
	t.Helper()
	r.s.Providers[Vercel] = r.renewer
	r.s.ConfigApps[Vercel] = oauth.App{ClientID: "vercel-id", ClientSecret: "vercel-secret", Slug: "loomplane"}
	const id, token = "conn_vercel", "vt-1"
	r.store.rows[id] = Connection{ID: id, OrgID: "org", Cloud: Vercel, Name: id, Method: MethodOAuth, Account: "Acme",
		Details: map[string]string{"team_id": "team_1"}, Status: StatusReady, Generation: 3, CheckedAt: checked}
	r.keep(t, id, oauth.Grant{AccessToken: token, Account: map[string]string{"team_id": "team_1"}})
}

func TestRefreshDueProbesAQuietSignIn(t *testing.T) {
	r := newRefreshRig(t)
	old := r.clock.Add(-48 * time.Hour)
	r.vercelSignIn(t, &old)
	report, err := r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report != (RefreshReport{Probed: 1}) {
		t.Fatalf("report %+v", report)
	}
	if !slices.Equal(r.renewer.probed, []string{"vt-1"}) || r.renewer.calls != 0 {
		t.Fatalf("probed %v, renewed %d times", r.renewer.probed, r.renewer.calls)
	}
	row := r.store.rows["conn_vercel"]
	if row.CheckedAt == nil || !row.CheckedAt.Equal(r.clock) || row.Generation != 3 || row.Status != StatusReady {
		t.Fatalf("row: %+v", row)
	}
	// Probed just now, so the next pass leaves it be.
	if _, err := r.s.RefreshDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(r.renewer.probed) != 1 {
		t.Fatalf("probed again: %v", r.renewer.probed)
	}
}

func TestRefreshDueProbesASignInNeverChecked(t *testing.T) {
	r := newRefreshRig(t)
	r.vercelSignIn(t, nil)
	if report, err := r.s.RefreshDue(t.Context()); err != nil || report.Probed != 1 {
		t.Fatalf("report %+v %v", report, err)
	}
	if row := r.store.rows["conn_vercel"]; row.CheckedAt == nil {
		t.Fatalf("row: %+v", row)
	}
}

func TestRefreshDueSkipsARecentlyCheckedSignIn(t *testing.T) {
	r := newRefreshRig(t)
	recent := r.clock.Add(-time.Hour)
	r.vercelSignIn(t, &recent)
	report, err := r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report != (RefreshReport{}) || len(r.renewer.probed) != 0 {
		t.Fatalf("report %+v, probed %v", report, r.renewer.probed)
	}
	if row := r.store.rows["conn_vercel"]; !row.CheckedAt.Equal(recent) {
		t.Fatalf("row: %+v", row)
	}
}

func TestRefreshDueMarksAnUninstalledIntegration(t *testing.T) {
	r := newRefreshRig(t)
	old := r.clock.Add(-48 * time.Hour)
	r.vercelSignIn(t, &old)
	r.renewer.probeErrs = map[string]error{"vt-1": fmt.Errorf("%w: uninstalled", oauth.ErrRefused)}
	report, err := r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report != (RefreshReport{Lapsed: 1}) {
		t.Fatalf("report %+v", report)
	}
	row := r.store.rows["conn_vercel"]
	if row.Status != StatusSignInNeeded || row.Message == "" || row.Generation != 3 {
		t.Fatalf("row: %+v", row)
	}
	if len(r.lapsed) != 1 || r.lapsed[0].ID != "conn_vercel" || r.attached != 0 {
		t.Fatalf("lapsed %v, %d on a cancellable context", r.lapsed, r.attached)
	}
}

func TestRefreshDueRecordsARejectedAppFoundByAProbe(t *testing.T) {
	r := newRefreshRig(t)
	old := r.clock.Add(-48 * time.Hour)
	r.vercelSignIn(t, &old)
	r.renewer.probeErrs = map[string]error{"vt-1": oauth.ErrAppRejected}
	report, err := r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report != (RefreshReport{Lapsed: 1}) || r.store.rows["conn_vercel"].Status != StatusSignInNeeded || len(r.lapsed) != 1 {
		t.Fatalf("report %+v row %+v", report, r.store.rows["conn_vercel"])
	}
	if r.apps.problems["org/"+Vercel] == "" {
		t.Fatalf("no app problem recorded: %v", r.apps.problems)
	}
}

func TestRefreshDueProbeThatCantReachTheCloudChangesNothing(t *testing.T) {
	r := newRefreshRig(t)
	old := r.clock.Add(-48 * time.Hour)
	r.vercelSignIn(t, &old)
	r.renewer.probeErrs = map[string]error{"vt-1": errors.New("503")}
	report, err := r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	row := r.store.rows["conn_vercel"]
	if report != (RefreshReport{Failed: 1}) || row.Status != StatusReady || !row.CheckedAt.Equal(old) || len(r.lapsed) != 0 {
		t.Fatalf("report %+v row %+v", report, row)
	}
}

func TestRefreshAllSweepsExpiredSignIns(t *testing.T) {
	r := newRefreshRig(t)
	r.apps.signIns = map[string]*PendingSignIn{
		"old":  {State: "old", ExpiresAt: r.clock.Add(-time.Minute)},
		"live": {State: "live", ExpiresAt: r.clock.Add(time.Minute)},
	}
	if err := r.s.RefreshAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.apps.signIns["old"]; ok || r.apps.signIns["live"] == nil {
		t.Fatalf("sign-ins: %v", r.apps.signIns)
	}
}

type failingSweep struct{ AppStore }

func (failingSweep) SweepReservations(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (failingSweep) SweepSignIns(context.Context, time.Time) (int64, error) {
	return 0, errors.New("database is down")
}

func TestRefreshAllWarnsWhenTheSweepFails(t *testing.T) {
	r := newRefreshRig(t)
	r.s.Apps = failingSweep{r.apps}
	if err := r.s.RefreshAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(r.warned, func(w string) bool { return strings.Contains(w, "database is down") }) {
		t.Fatalf("warned: %v", r.warned)
	}
}

// failingLock runs the callback, then reports the lock failed, as a
// commit that fails after the writes outside its transaction landed.
type failingLock struct{ *lockingStore }

func (f failingLock) WithConnectionLock(ctx context.Context, org, id string, fn func() error) error {
	if err := f.lockingStore.WithConnectionLock(ctx, org, id, fn); err != nil {
		return err
	}
	return errors.New("release the connection lock")
}

func TestLapseIsToldEvenWhenTheLockFails(t *testing.T) {
	r := newRefreshRig(t)
	r.s.grants = failingLock{r.locked}
	r.signedIn(t, "conn_do", "at-1", r.clock.Add(time.Hour))
	r.renewer.errs["rt-at-1"] = oauth.ErrRefused
	_, _, err := r.s.renew(t.Context(), mustConn(t, r, "conn_do"), r.kept(t, "conn_do"), refreshAhead)
	if err == nil || errors.Is(err, ErrNotReady) {
		t.Fatalf("got %v", err)
	}
	if r.store.rows["conn_do"].Status != StatusSignInNeeded {
		t.Fatalf("row: %+v", r.store.rows["conn_do"])
	}
	if len(r.lapsed) != 1 || r.attached != 0 {
		t.Fatalf("lapsed %v, %d on a cancellable context", r.lapsed, r.attached)
	}
}

func TestLeftSignerIsToldEvenWhenTheLockFails(t *testing.T) {
	r := newRefreshRig(t)
	r.s.grants = failingLock{r.locked}
	r.signedIn(t, "conn_do", "at-1", r.clock.Add(30*24*time.Hour))
	row := r.store.rows["conn_do"]
	row.Cloud, row.SignedInBy = GCP, "ada"
	r.store.rows["conn_do"] = row
	r.locked.departed = map[string]bool{"ada": true}
	report, err := r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.Failed != 1 || len(r.lapsed) != 1 {
		t.Fatalf("report %+v, lapsed %v", report, r.lapsed)
	}
}

func mustConn(t *testing.T, r *refreshRig, id string) *Connection {
	t.Helper()
	c, err := r.s.store.GetConnection(t.Context(), "org", id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
