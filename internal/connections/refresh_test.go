package connections

import (
	"context"
	"encoding/json"
	"errors"
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

type lockingSecrets struct {
	*memSecrets
	mu sync.Mutex
}

func (l *lockingSecrets) Put(ctx context.Context, org, id string, v []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
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
}

func (r *renewer) Refresh(_ context.Context, app oauth.App, refreshToken string) (oauth.Grant, error) {
	if r.started != nil {
		r.started <- struct{}{}
	}
	if r.block != nil {
		<-r.block
	}
	time.Sleep(r.delay)
	r.mu.Lock()
	defer r.mu.Unlock()
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
	r.s.Lapsed = func(_ context.Context, c *Connection) {
		r.mu.Lock()
		defer r.mu.Unlock()
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
	if report != (RefreshReport{Refreshed: 1, Lapsed: 1, Failed: 1}) {
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
