package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"

	"github.com/xraph/loomplane/internal/security"
)

// Every token the fakes hand out starts with one of these, so a test can
// look for any of them where none should be.
const (
	bbAccessPrefix  = "bb-access-SECRET-"
	bbRefreshPrefix = "bb-refresh-SECRET-"
	bbGoodCode      = "good-code"
	bbAcmeUUID      = "{0f2b4a2c-0000-4000-8000-00000000000a}"
	bbOtherUUID     = "{0f2b4a2c-0000-4000-8000-00000000000b}"
)

// noTokens fails when s holds anything that looks like a token.
func noTokens(t *testing.T, what, s string) {
	t.Helper()
	if strings.Contains(s, bbAccessPrefix) || strings.Contains(s, bbRefreshPrefix) || strings.Contains(s, "SECRET") {
		t.Fatalf("%s holds a token: %s", what, s)
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type bbClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *bbClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *bbClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeBBAPI plays Bitbucket. It honours one refresh token at a time and,
// when rotate is set, spends it on every refresh, as Bitbucket may.
type fakeBBAPI struct {
	mu       sync.Mutex
	clock    *bbClock
	lifetime time.Duration
	// workspaces are those the signed-in account belongs to, by slug.
	workspaces  map[string]BitbucketWorkspace
	refresh     string // the refresh token Bitbucket honours now
	rotate      bool
	refuse      bool
	refreshErr  error
	noRefresh   bool  // Exchange answers without a refresh token
	exchangeErr error // Exchange fails with it
	lookupErr   error // Workspace fails with it
	delay       time.Duration
	issued      int
	exchanges   int // Exchange calls, spent code or not
	refreshes   int
	lookups     int // Workspace calls
	repos       map[string][]BitbucketRepository
	repoErr     map[string]error
	repoTokens  []string
}

func newFakeBBAPI(clock *bbClock) *fakeBBAPI {
	return &fakeBBAPI{
		clock: clock, lifetime: 2 * time.Hour,
		workspaces: map[string]BitbucketWorkspace{
			"acme":  {UUID: bbAcmeUUID, Slug: "acme", Name: "Acme Inc", AvatarURL: "https://example.test/acme.png"},
			"other": {UUID: bbOtherUUID, Slug: "other", Name: "Other Ltd"},
		},
		repos:   map[string][]BitbucketRepository{},
		repoErr: map[string]error{},
	}
}

func (f *fakeBBAPI) AuthorizeURL(state string) string {
	return "https://bitbucket.example.test/site/oauth2/authorize?state=" + url.QueryEscape(state)
}

// issue mints a grant. f.mu is held.
func (f *fakeBBAPI) issue(refresh string) BitbucketGrant {
	f.issued++
	return BitbucketGrant{
		AccessToken:  fmt.Sprintf("%s%d", bbAccessPrefix, f.issued),
		RefreshToken: refresh,
		Expires:      f.clock.now().Add(f.lifetime),
	}
}

func (f *fakeBBAPI) Exchange(_ context.Context, code string) (BitbucketGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exchanges++
	if f.exchangeErr != nil {
		return BitbucketGrant{}, f.exchangeErr
	}
	if code != bbGoodCode {
		return BitbucketGrant{}, ErrBitbucketRefused
	}
	if f.noRefresh {
		return f.issue(""), nil
	}
	f.refresh = fmt.Sprintf("%s%d", bbRefreshPrefix, f.issued+1)
	return f.issue(f.refresh), nil
}

func (f *fakeBBAPI) Refresh(_ context.Context, refreshToken string) (BitbucketGrant, error) {
	time.Sleep(f.delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes++
	switch {
	case f.refreshErr != nil:
		return BitbucketGrant{}, f.refreshErr
	case f.refuse, refreshToken != f.refresh:
		return BitbucketGrant{}, ErrBitbucketRefused
	}
	if f.rotate {
		f.refresh = fmt.Sprintf("%s%d", bbRefreshPrefix, f.issued+1)
	}
	return f.issue(f.refresh), nil
}

func (f *fakeBBAPI) CurrentUser(context.Context, string) (BitbucketUser, error) {
	return BitbucketUser{UUID: "{u-1}", Name: "Ada Lovelace"}, nil
}

func (f *fakeBBAPI) Membership(_ context.Context, _, slug string) (BitbucketWorkspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.workspaces {
		if strings.EqualFold(w.Slug, slug) {
			return w, nil
		}
	}
	return BitbucketWorkspace{}, ErrNotAMember
}

func (f *fakeBBAPI) Workspace(_ context.Context, _, uuid string) (BitbucketWorkspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	if f.lookupErr != nil {
		return BitbucketWorkspace{}, f.lookupErr
	}
	for _, w := range f.workspaces {
		if strings.EqualFold(w.UUID, uuid) {
			return w, nil
		}
	}
	return BitbucketWorkspace{}, ErrNotAMember
}

func (f *fakeBBAPI) Repositories(_ context.Context, token, slug string) ([]BitbucketRepository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repoTokens = append(f.repoTokens, token)
	if err := f.repoErr[slug]; err != nil {
		return nil, err
	}
	return f.repos[slug], nil
}

func (f *fakeBBAPI) counts() (refreshes, lookups int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refreshes, f.lookups
}

func (f *fakeBBAPI) set(fn func(f *fakeBBAPI)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// fakeBBStore keeps workspaces in memory, with a real lock per workspace.
type fakeBBStore struct {
	mu      sync.Mutex
	rows    map[string]*BitbucketConnection
	members map[string]bool // org/user
	locks   map[string]*sync.Mutex
	saves   int
	nextID  int
	// commit, when set, is what releasing a lock returns once its work
	// succeeded, as the real store's commit may fail after the fact.
	commit func(ctx context.Context) error
}

func newFakeBBStore() *fakeBBStore {
	return &fakeBBStore{rows: map[string]*BitbucketConnection{}, members: map[string]bool{}, locks: map[string]*sync.Mutex{}}
}

func (s *fakeBBStore) SaveBitbucketWorkspace(_ context.Context, c *BitbucketConnection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	for _, r := range s.rows {
		if r.OrgID == c.OrgID && r.WorkspaceUUID == c.WorkspaceUUID {
			c.ID, c.CreatedAt, c.GrantGeneration = r.ID, r.CreatedAt, r.GrantGeneration+1
			cp := *c
			s.rows[r.ID] = &cp
			return nil
		}
	}
	if c.ID == "" {
		s.nextID++
		c.ID = fmt.Sprintf("bbws_%d", s.nextID)
	}
	c.GrantGeneration = 1
	cp := *c
	s.rows[c.ID] = &cp
	return nil
}

func (s *fakeBBStore) ListBitbucketWorkspaces(_ context.Context, orgID string) ([]*BitbucketConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*BitbucketConnection{}
	for _, r := range s.rows {
		if r.OrgID == orgID {
			cp := *r
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *fakeBBStore) GetBitbucketWorkspace(_ context.Context, orgID, id string) (*BitbucketConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || r.OrgID != orgID {
		return nil, ErrWorkspaceNotFound
	}
	cp := *r
	return &cp, nil
}

func (s *fakeBBStore) row(id string) *BitbucketConnection {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok {
		return nil
	}
	cp := *r
	return &cp
}

func (s *fakeBBStore) DeleteBitbucketWorkspace(_ context.Context, orgID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || r.OrgID != orgID {
		return ErrWorkspaceNotFound
	}
	delete(s.rows, id)
	return nil
}

func (s *fakeBBStore) SetBitbucketWorkspaceStatus(_ context.Context, orgID, id, status, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || r.OrgID != orgID {
		return ErrWorkspaceNotFound
	}
	r.Status, r.Message = status, message
	return nil
}

func (s *fakeBBStore) WithBitbucketLock(ctx context.Context, orgID, id string, fn func() error) error {
	s.mu.Lock()
	l := s.locks[orgID+"/"+id]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[orgID+"/"+id] = l
	}
	commit := s.commit
	s.mu.Unlock()
	l.Lock()
	defer l.Unlock()
	if err := fn(); err != nil {
		return err
	}
	if commit != nil {
		return commit(ctx)
	}
	return nil
}

func (s *fakeBBStore) BitbucketConnectorIsMember(_ context.Context, orgID, userID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.members[orgID+"/"+userID], nil
}

func (s *fakeBBStore) setMember(orgID, userID string, member bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[orgID+"/"+userID] = member
}

func (s *fakeBBStore) SaveBitbucketHook(context.Context, *BitbucketHook) error { return nil }
func (s *fakeBBStore) GetBitbucketHook(context.Context, string) (*BitbucketHook, error) {
	return nil, ErrHookNotFound
}
func (s *fakeBBStore) UpdateBitbucketHook(context.Context, *BitbucketHook) error { return nil }
func (s *fakeBBStore) UpdateBitbucketHookIfOff(context.Context, *BitbucketHook) (bool, error) {
	return true, nil
}
func (s *fakeBBStore) BitbucketHookFor(context.Context, string, string) (*BitbucketHook, error) {
	return nil, ErrHookNotFound
}
func (s *fakeBBStore) BitbucketHooksIn(context.Context, string, string) ([]*BitbucketHook, error) {
	return nil, nil
}
func (s *fakeBBStore) DeleteBitbucketHook(context.Context, string) error { return nil }

// fakeSecrets is Vault.
type fakeSecrets struct {
	mu   sync.Mutex
	vals map[string][]byte
	// missing makes Reveal report ErrBitbucketSecretMissing, as the real
	// Vault's adapter does for a secret that's gone.
	missing bool
}

func newFakeSecrets() *fakeSecrets { return &fakeSecrets{vals: map[string][]byte{}} }

func (v *fakeSecrets) Put(_ context.Context, orgID, id string, credential []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.vals[orgID+"/"+id] = append([]byte(nil), credential...)
	return nil
}

func (v *fakeSecrets) Reveal(_ context.Context, orgID, id string) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	raw, ok := v.vals[orgID+"/"+id]
	if v.missing {
		return nil, ErrBitbucketSecretMissing
	}
	if !ok {
		return nil, errors.New("no such secret")
	}
	return append([]byte(nil), raw...), nil
}

// Delete fails once the caller has given up, as a real Vault call would.
func (v *fakeSecrets) Delete(ctx context.Context, orgID, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.vals, orgID+"/"+id)
	return nil
}

func (v *fakeSecrets) get(orgID, id string) (string, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	raw, ok := v.vals[orgID+"/"+id]
	return string(raw), ok
}

func (v *fakeSecrets) len() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.vals)
}

type bbAuditRecord struct {
	action string
	p      security.Principal
	c      BitbucketConnection
}

type fakeBBAudit struct {
	mu      sync.Mutex
	records []bbAuditRecord
}

func (a *fakeBBAudit) RecordBitbucket(_ context.Context, action string, p security.Principal, c BitbucketConnection) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = append(a.records, bbAuditRecord{action, p, c})
}

type bbFixture struct {
	conns   *BitbucketConnections
	store   *fakeBBStore
	api     *fakeBBAPI
	secrets *fakeSecrets
	audit   *fakeBBAudit
	authz   *security.Authorizer
	clock   *bbClock
	key     []byte
}

func newBBFixture(t *testing.T) *bbFixture {
	t.Helper()
	ws := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(ws))
	if err != nil {
		t.Fatal(err)
	}
	clock := &bbClock{t: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	f := &bbFixture{
		store: newFakeBBStore(), api: newFakeBBAPI(clock), secrets: newFakeSecrets(), audit: &fakeBBAudit{},
		authz: security.NewAuthorizer(eng, ws, nil), clock: clock, key: []byte("0123456789abcdef0123456789abcdef"),
	}
	f.conns = f.newConns()
	return f
}

// newConns is another Loomplane process on the same database, Vault and
// Bitbucket.
func (f *bbFixture) newConns() *BitbucketConnections {
	c := NewBitbucketConnections(f.store, f.api, f.secrets, f.authz, f.audit, f.key)
	c.now = f.clock.now
	return c
}

func (f *bbFixture) as(t *testing.T, org, user, role string) security.Principal {
	t.Helper()
	if err := f.authz.Assign(context.Background(), org, user, role, "test"); err != nil {
		t.Fatal(err)
	}
	f.store.setMember(org, user, true)
	return security.Principal{OrgID: org, UserID: user}
}

// stateFor runs Authorize and returns the state it put in the link.
func (f *bbFixture) stateFor(t *testing.T, p security.Principal, workspace string) string {
	t.Helper()
	link, err := f.conns.Authorize(context.Background(), p, workspace)
	if err != nil {
		t.Fatalf("Authorize(%s): %v", workspace, err)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

func (f *bbFixture) connect(t *testing.T, p security.Principal, workspace string) *BitbucketWorkspaceView {
	t.Helper()
	v, err := f.conns.Connect(context.Background(), p, BitbucketConnectInput{Code: bbGoodCode, State: f.stateFor(t, p, workspace)})
	if err != nil {
		t.Fatalf("Connect(%s): %v", workspace, err)
	}
	return v
}

// expire moves the clock past the cached token's renewal point.
func (f *bbFixture) expire() { f.clock.add(2 * time.Hour) }

func repo(owner, name string) Repository {
	return Repository{Host: HostBitbucket, Owner: owner, Name: name}
}

func TestBitbucketConnect(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	other := f.as(t, "aorg_a", "ausr_other", security.RoleAdmin)
	elsewhere := f.as(t, "aorg_b", "ausr_admin", security.RoleAdmin)
	now := f.clock.now()

	sign := func(key []byte, p security.Principal, workspace string, at time.Time) string {
		s, err := SignWorkspaceState(key, p.OrgID, p.UserID, workspace, at)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	githubState, err := SignState(f.key, admin.OrgID, admin.UserID, now)
	if err != nil {
		t.Fatal(err)
	}
	refusals := map[string]string{
		"bad signature":             sign([]byte("another key, another key, another"), admin, "acme", now),
		"tampered":                  sign(f.key, admin, "acme", now) + "x",
		"expired":                   sign(f.key, admin, "acme", now.Add(-16*time.Minute)),
		"state for another user":    sign(f.key, other, "acme", now),
		"state for another org":     sign(f.key, elsewhere, "acme", now),
		"state without a workspace": githubState,
		"no state":                  "",
	}
	for name, state := range refusals {
		t.Run(name, func(t *testing.T) {
			if _, connErr := f.conns.Connect(ctx, admin, BitbucketConnectInput{Code: bbGoodCode, State: state}); !errors.Is(connErr, ErrBadState) {
				t.Fatalf("Connect: %v, want ErrBadState", connErr)
			}
		})
	}

	t.Run("not a member", func(t *testing.T) {
		_, err = f.conns.Connect(ctx, admin, BitbucketConnectInput{Code: bbGoodCode, State: f.stateFor(t, admin, "notmine")})
		if !errors.Is(err, ErrNotAMember) {
			t.Fatalf("Connect: %v, want ErrNotAMember", err)
		}
		noTokens(t, "the error", err.Error())
	})
	t.Run("no refresh token", func(t *testing.T) {
		f.api.set(func(a *fakeBBAPI) { a.noRefresh = true })
		defer f.api.set(func(a *fakeBBAPI) { a.noRefresh = false })
		_, err = f.conns.Connect(ctx, admin, BitbucketConnectInput{Code: bbGoodCode, State: f.stateFor(t, admin, "acme")})
		if !errors.Is(err, ErrBitbucketRefused) {
			t.Fatalf("Connect: %v, want ErrBitbucketRefused", err)
		}
	})
	t.Run("a spent code", func(t *testing.T) {
		_, err = f.conns.Connect(ctx, admin, BitbucketConnectInput{Code: "spent", State: f.stateFor(t, admin, "acme")})
		if !errors.Is(err, ErrBitbucketRefused) {
			t.Fatalf("Connect: %v, want ErrBitbucketRefused", err)
		}
	})
	if f.store.saves != 0 || f.secrets.len() != 0 || len(f.audit.records) != 0 {
		t.Fatalf("a refused connection left something behind: %d saves, %d secrets, %d audit records", f.store.saves, f.secrets.len(), len(f.audit.records))
	}

	v := f.connect(t, admin, "acme")
	noTokens(t, "the view", jsonOf(t, v))
	if v.Workspace != "acme" || v.Name != "Acme Inc" || v.ConnectedBy != "Ada Lovelace" || v.Status != BitbucketReady ||
		v.AvatarURL != "https://example.test/acme.png" || v.ManageURL != "https://bitbucket.org/acme/workspace/settings" || !v.CreatedAt.Equal(now) {
		t.Fatalf("view = %+v", v)
	}
	row := f.store.row(v.ID)
	if row == nil || row.OrgID != "aorg_a" || row.WorkspaceUUID != bbAcmeUUID || row.Status != BitbucketReady ||
		row.CreatedBy != admin.UserID || row.BitbucketUserUUID != "{u-1}" {
		t.Fatalf("row = %+v", row)
	}
	held, ok := f.secrets.get("aorg_a", v.ID)
	if !ok || held != `{"refresh_token":"`+f.api.refresh+`"}` {
		t.Fatalf("Vault holds %q, want only the refresh token", held)
	}
	if len(f.audit.records) != 1 || f.audit.records[0].action != "bitbucket.connect" || f.audit.records[0].c.ID != v.ID {
		t.Fatalf("audit = %+v", f.audit.records)
	}
	noTokens(t, "the audit record", fmt.Sprintf("%+v", f.audit.records))

	// A connection Bitbucket stopped honouring is connected again by
	// someone else: same row, new sign-in, ready.
	if err = f.store.SetBitbucketWorkspaceStatus(ctx, "aorg_a", v.ID, BitbucketReconnectNeeded, "gone"); err != nil {
		t.Fatal(err)
	}
	again, err := f.conns.Connect(ctx, other, BitbucketConnectInput{Code: bbGoodCode, State: f.stateFor(t, other, "ACME")})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != v.ID || again.Status != BitbucketReady || again.Message != "" {
		t.Fatalf("reconnected view = %+v, want id %s and ready", again, v.ID)
	}
	if row := f.store.row(v.ID); row.CreatedBy != other.UserID || row.Status != BitbucketReady {
		t.Fatalf("reconnected row = %+v", row)
	}
	if again, _ := f.secrets.get("aorg_a", v.ID); again == held || again != `{"refresh_token":"`+f.api.refresh+`"}` {
		t.Fatalf("Vault holds %q after reconnecting, want the new refresh token", again)
	}
	newest := fmt.Sprintf("%s%d", bbAccessPrefix, f.api.issued)
	if tok, err := f.conns.Token(ctx, "aorg_a", repo("acme", "api")); err != nil || tok != newest {
		t.Fatalf("Token after reconnecting = %q, %v; want the new sign-in's", tok, err)
	}

	t.Run("needs RepositoryConnect", func(t *testing.T) {
		dev := f.as(t, "aorg_a", "ausr_dev", security.RoleDeveloper)
		state, err := SignWorkspaceState(f.key, dev.OrgID, dev.UserID, "acme", now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.conns.Connect(ctx, dev, BitbucketConnectInput{Code: bbGoodCode, State: state}); !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("a developer connecting: %v", err)
		}
	})
}

func TestBitbucketConnect_emptyCodeOrStateNeverReachesBitbucket(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	state := f.stateFor(t, admin, "acme")
	for name, in := range map[string]BitbucketConnectInput{
		"no code":    {State: state},
		"no state":   {Code: bbGoodCode},
		"neither":    {},
		"blank code": {Code: "   ", State: state},
	} {
		if _, err := f.conns.Connect(ctx, admin, in); !errors.Is(err, ErrBadState) {
			t.Fatalf("%s: %v, want ErrBadState", name, err)
		}
	}
	if f.api.exchanges != 0 {
		t.Fatalf("Bitbucket was asked to exchange %d codes", f.api.exchanges)
	}
}

func TestBitbucketAuthorize_needsRepositoryConnect(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	dev := f.as(t, "aorg_a", "ausr_dev", security.RoleDeveloper)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)

	if _, err := f.conns.Authorize(ctx, dev, "acme"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a developer authorizing: %v", err)
	}
	st, err := VerifyState(f.key, f.stateFor(t, admin, "acme"), f.clock.now())
	if err != nil || st.OrgID != "aorg_a" || st.UserID != admin.UserID || st.Workspace != "acme" {
		t.Fatalf("state = %+v, %v", st, err)
	}
	for _, bad := range []string{"", "a/b", `a"b`, "a b"} {
		if _, err := f.conns.Authorize(ctx, admin, bad); !errors.Is(err, ErrBadWorkspace) {
			t.Fatalf("Authorize(%q): %v, want ErrBadWorkspace", bad, err)
		}
	}
	// A typed slug is held to the rule an address is, so a workspace that
	// connects is one a source can name, and the other way round.
	for _, slug := range []string{"acme", "Acme", "ACME-2", "a_b", "0acme", "a.b", "-acme", "_acme", "a+b", "é", strings.Repeat("a", 62), strings.Repeat("a", 63)} {
		_, err := f.conns.Authorize(ctx, admin, slug)
		_, parseErr := ParseURL("https://bitbucket.org/" + slug + "/api")
		if (err == nil) != (parseErr == nil) {
			t.Fatalf("Authorize(%q) = %v, but ParseURL said %v", slug, err, parseErr)
		}
		if err != nil && !errors.Is(err, ErrBadWorkspace) {
			t.Fatalf("Authorize(%q): %v, want ErrBadWorkspace", slug, err)
		}
	}
	if st, err := VerifyState(f.key, f.stateFor(t, admin, "Acme"), f.clock.now()); err != nil || st.Workspace != "acme" {
		t.Fatalf("a slug typed in capitals = %+v, %v; want it folded", st, err)
	}
	none := NewBitbucketConnections(f.store, nil, f.secrets, f.authz, nil, f.key)
	if _, err := none.Authorize(ctx, admin, "acme"); !errors.Is(err, ErrBitbucketNotConfigured) {
		t.Fatalf("unconfigured Authorize: %v", err)
	}
	if _, err := none.Connect(ctx, admin, BitbucketConnectInput{Code: bbGoodCode}); !errors.Is(err, ErrBitbucketNotConfigured) {
		t.Fatalf("unconfigured Connect: %v", err)
	}
	if tok, err := none.Token(ctx, "aorg_a", repo("acme", "api")); tok != "" || err != nil {
		t.Fatalf("unconfigured Token = %q, %v", tok, err)
	}
}

func TestBitbucketStatus(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	viewer := f.as(t, "aorg_a", "ausr_viewer", security.RoleViewer)
	v := f.connect(t, admin, "acme")
	f.connect(t, admin, "other")
	if err := f.store.SetBitbucketWorkspaceStatus(ctx, "aorg_a", v.ID, BitbucketReconnectNeeded, "Bitbucket no longer accepts it."); err != nil {
		t.Fatal(err)
	}

	st, err := f.conns.Status(ctx, viewer)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Configured || st.CanConnect || len(st.Workspaces) != 2 || len(st.Problems) != 1 || !strings.Contains(st.Problems[0], "acme") {
		t.Fatalf("status for a viewer = %+v", st)
	}
	noTokens(t, "the status", jsonOf(t, st))
	if st, err = f.conns.Status(ctx, admin); err != nil || !st.CanConnect {
		t.Fatalf("status for an admin = %+v, %v", st, err)
	}
	none := NewBitbucketConnections(f.store, nil, f.secrets, f.authz, nil, f.key)
	if st, err = none.Status(ctx, admin); err != nil || st.Configured || st.CanConnect || len(st.Workspaces) != 0 {
		t.Fatalf("unconfigured status = %+v, %v", st, err)
	}
	if _, err := f.conns.Status(ctx, security.Principal{OrgID: "aorg_a", UserID: "ausr_stranger"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a stranger's status: %v", err)
	}
}

func TestBitbucketToken_refreshesTheSlug(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	v := f.connect(t, admin, "acme")

	f.api.set(func(a *fakeBBAPI) {
		w := a.workspaces["acme"]
		// Bitbucket's answer may not match the stored UUID's case.
		w.UUID, w.Slug, w.Name = strings.ToUpper(w.UUID), "acme-new", "Acme Renamed"
		delete(a.workspaces, "acme")
		a.workspaces["acme-new"] = w
	})
	f.expire()
	// The refresh finds the new name. The old one may by now be somebody
	// else's workspace, so it gets no token.
	if tok, err := f.conns.Token(ctx, "aorg_a", repo("acme", "api")); tok != "" || err != nil {
		t.Fatalf("Token for the old name = %q, %v", tok, err)
	}
	row := f.store.row(v.ID)
	if row.WorkspaceSlug != "acme-new" || row.WorkspaceName != "Acme Renamed" || row.ID != v.ID || row.CreatedBy != admin.UserID ||
		row.Status != BitbucketReady || row.WorkspaceUUID != bbAcmeUUID {
		t.Fatalf("row = %+v", row)
	}
	tok, err := f.conns.Token(ctx, "aorg_a", repo("acme-new", "api"))
	if err != nil || tok != bbAccessPrefix+"2" {
		t.Fatalf("Token for the new name = %q, %v", tok, err)
	}
	if refreshes, lookups := f.api.counts(); refreshes != 1 || lookups != 1 {
		t.Fatalf("%d refreshes and %d lookups, want one of each", refreshes, lookups)
	}
}

func TestBitbucketToken_routing(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	a := f.as(t, "aorg_a", "ausr_a", security.RoleAdmin)
	b := f.as(t, "aorg_b", "ausr_b", security.RoleAdmin)
	f.connect(t, a, "other")
	vb := f.connect(t, b, "acme")

	for name, c := range map[string]struct {
		org  string
		repo Repository
	}{
		"org B's workspace, asked for by org A": {"aorg_a", repo("acme", "api")},
		"a workspace org A hasn't connected":    {"aorg_a", repo("third", "api")},
		"an organization with no connections":   {"aorg_c", repo("acme", "api")},
		"GitHub":                                {"aorg_b", Repository{Host: HostGitHub, Owner: "acme", Name: "api"}},
		"github.com":                            {"aorg_b", Repository{Host: "github.com", Owner: "acme", Name: "api"}},
		"another host":                          {"aorg_b", Repository{Host: "bitbucket.example.test", Owner: "acme", Name: "api"}},
		"a lookalike host":                      {"aorg_b", Repository{Host: "bitbucket.org.example.test", Owner: "acme", Name: "api"}},
	} {
		t.Run(name, func(t *testing.T) {
			if tok, err := f.conns.Token(ctx, c.org, c.repo); tok != "" || err != nil {
				t.Fatalf("Token = %q, %v; want an anonymous read", tok, err)
			}
		})
	}
	ta, err := f.conns.Token(ctx, "aorg_a", repo("other", "api"))
	if err != nil || ta == "" {
		t.Fatalf("org A's own workspace = %q, %v", ta, err)
	}
	tb, err := f.conns.Token(ctx, "aorg_b", repo("ACME", "api"))
	if err != nil || tb == "" || tb == ta {
		t.Fatalf("org B's own workspace = %q, %v (org A's is %q)", tb, err, ta)
	}

	if err = f.store.SetBitbucketWorkspaceStatus(ctx, "aorg_b", vb.ID, BitbucketReconnectNeeded, "gone"); err != nil {
		t.Fatal(err)
	}
	if tok, tokErr := f.conns.Token(ctx, "aorg_b", repo("acme", "api")); tok != "" || tokErr != nil {
		t.Fatalf("a reconnect_needed workspace = %q, %v", tok, tokErr)
	}

	c, tok, err := f.conns.ConnectionFor(ctx, "aorg_a", "Other")
	if err != nil || c == nil || c.WorkspaceSlug != "other" || tok != ta {
		t.Fatalf("ConnectionFor(other) = %+v, %q, %v", c, tok, err)
	}
	if c, tok, err = f.conns.ConnectionFor(ctx, "aorg_b", "acme"); err != nil || c == nil || c.ID != vb.ID || tok != "" {
		t.Fatalf("ConnectionFor a reconnect_needed workspace = %+v, %q, %v", c, tok, err)
	}
	if _, _, err = f.conns.ConnectionFor(ctx, "aorg_a", "acme"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("ConnectionFor another org's workspace: %v", err)
	}
}

func TestBitbucketToken_cachesUntilAMinuteBeforeExpiry(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	f.connect(t, admin, "acme")

	// Connecting caches the token it was given, so nothing is refreshed.
	if tok, err := f.conns.Token(ctx, "aorg_a", repo("acme", "api")); err != nil || tok != bbAccessPrefix+"1" {
		t.Fatalf("Token = %q, %v", tok, err)
	}
	f.expire()
	first, err := f.conns.Token(ctx, "aorg_a", repo("acme", "api"))
	if err != nil || first != bbAccessPrefix+"2" {
		t.Fatalf("Token = %q, %v", first, err)
	}
	f.clock.add(time.Hour)
	if tok, tokErr := f.conns.Token(ctx, "aorg_a", repo("acme", "web")); tokErr != nil || tok != first {
		t.Fatalf("Token inside the window = %q, %v; want %q", tok, tokErr, first)
	}
	f.clock.add(time.Hour - 61*time.Second) // 61 seconds before expiry
	if tok, tokErr := f.conns.Token(ctx, "aorg_a", repo("acme", "api")); tokErr != nil || tok != first {
		t.Fatalf("Token 61s before expiry = %q, %v; want %q", tok, tokErr, first)
	}
	if refreshes, _ := f.api.counts(); refreshes != 1 {
		t.Fatalf("%d refreshes inside the window, want 1", refreshes)
	}
	f.clock.add(2 * time.Second) // 59 seconds before expiry
	tok, err := f.conns.Token(ctx, "aorg_a", repo("acme", "api"))
	if err != nil || tok == first || tok == "" {
		t.Fatalf("Token 59s before expiry = %q, %v; want a fresh one", tok, err)
	}
	if refreshes, _ := f.api.counts(); refreshes != 2 {
		t.Fatalf("%d refreshes, want 2", refreshes)
	}
}

func TestBitbucketToken_concurrentCallersSpendTheRefreshTokenOnce(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	v := f.connect(t, admin, "acme")
	f.api.set(func(a *fakeBBAPI) { a.rotate, a.delay = true, 20*time.Millisecond })
	f.expire()

	tokens := make([]string, 10)
	errs := make([]error, 10)
	var wg sync.WaitGroup
	for i := range tokens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens[i], errs[i] = f.conns.Token(ctx, "aorg_a", repo("acme", "api"))
		}()
	}
	wg.Wait()
	for i := range tokens {
		if errs[i] != nil || tokens[i] == "" || tokens[i] != tokens[0] {
			t.Fatalf("caller %d got %q, %v; caller 0 got %q", i, tokens[i], errs[i], tokens[0])
		}
	}
	if refreshes, _ := f.api.counts(); refreshes != 1 {
		t.Fatalf("%d refreshes, want 1", refreshes)
	}
	held, _ := f.secrets.get("aorg_a", v.ID)
	if held != `{"refresh_token":"`+f.api.refresh+`"}` {
		t.Fatalf("Vault holds %q, want the newest refresh token %q", held, f.api.refresh)
	}
	if row := f.store.row(v.ID); row.Status != BitbucketReady {
		t.Fatalf("status = %s", row.Status)
	}
}

// Two processes share the database lock and Vault, so neither spends a
// refresh token the other already has.
func TestBitbucketToken_twoProcessesNeverSpendOneRefreshTokenTwice(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	v := f.connect(t, admin, "acme")
	f.api.set(func(a *fakeBBAPI) { a.rotate, a.delay = true, 10*time.Millisecond })
	f.expire()

	procs := []*BitbucketConnections{f.newConns(), f.newConns()}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []string
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := procs[i%2].Token(ctx, "aorg_a", repo("acme", "api"))
			if err != nil || tok == "" {
				mu.Lock()
				failures = append(failures, fmt.Sprintf("%q, %v", tok, err))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(failures) != 0 {
		t.Fatalf("callers failed: %v", failures)
	}
	if refreshes, _ := f.api.counts(); refreshes > 2 {
		t.Fatalf("%d refreshes, want at most one per process", refreshes)
	}
	if row := f.store.row(v.ID); row.Status != BitbucketReady {
		t.Fatalf("status = %s %q: a spent refresh token was used", row.Status, row.Message)
	}
	if held, _ := f.secrets.get("aorg_a", v.ID); held != `{"refresh_token":"`+f.api.refresh+`"}` {
		t.Fatalf("Vault holds %q, want %q", held, f.api.refresh)
	}
}

func TestBitbucketToken_refusedRefreshMarksReconnect(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	v := f.connect(t, admin, "acme")

	// A failure Bitbucket might recover from is an error, and changes nothing.
	f.api.set(func(a *fakeBBAPI) { a.refreshErr = errors.New("connection reset") })
	f.expire()
	tok, err := f.conns.Token(ctx, "aorg_a", repo("acme", "api"))
	if err == nil || tok != "" {
		t.Fatalf("Token on a network failure = %q, %v; want an error", tok, err)
	}
	noTokens(t, "the error", err.Error())
	if row := f.store.row(v.ID); row.Status != BitbucketReady {
		t.Fatalf("a network failure changed the status to %s", row.Status)
	}

	f.api.set(func(a *fakeBBAPI) { a.refreshErr, a.refuse = nil, true })
	tok, err = f.conns.Token(ctx, "aorg_a", repo("acme", "api"))
	if tok != "" || err != nil {
		t.Fatalf("Token on a refusal = %q, %v; want an anonymous read", tok, err)
	}
	row := f.store.row(v.ID)
	if row.Status != BitbucketReconnectNeeded || row.Message != bitbucketRefusedMessage {
		t.Fatalf("row = %s %q", row.Status, row.Message)
	}
	if !strings.Contains(row.Message, "Bitbucket") || !strings.Contains(row.Message, "again") {
		t.Fatalf("the message should tell a person what happened and what to do: %q", row.Message)
	}
	before, _ := f.api.counts()
	if tok, err = f.conns.Token(ctx, "aorg_a", repo("acme", "api")); tok != "" || err != nil {
		t.Fatalf("Token after the refusal = %q, %v", tok, err)
	}
	if after, _ := f.api.counts(); after != before {
		t.Fatalf("a refused sign-in was tried again (%d refreshes, then %d)", before, after)
	}
	if _, ok := f.secrets.get("aorg_a", v.ID); !ok {
		t.Fatal("the refused sign-in's secret should stay until it's replaced or disconnected")
	}
}

// Bitbucket turning down Loomplane's own key and secret is the operator's
// to fix. The workspace's sign-in is still good, so nothing about it may
// change: private repositories can't be read meanwhile, public ones are
// read without it, and the same sign-in renews once the configuration is
// right.
func TestBitbucketToken_rejectedConsumerLeavesTheSignIn(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	var warned []string
	f.conns.SetWarn(func(msg string, err error) { warned = append(warned, msg+": "+err.Error()) })
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	v := f.connect(t, admin, "acme")
	held, _ := f.secrets.get("aorg_a", v.ID)
	saves := f.store.saves
	if st, err := f.conns.Status(ctx, admin); err != nil || len(st.Problems) != 0 {
		t.Fatalf("status before the rejection = %+v, %v", st, err)
	}

	f.api.set(func(a *fakeBBAPI) { a.refreshErr = ErrBitbucketConsumerRejected })
	f.expire()
	tok, err := f.conns.Token(ctx, "aorg_a", repo("acme", "api"))
	if err != nil || tok != "" {
		t.Fatalf("Token with a rejected consumer = %q, %v; want no token and no error", tok, err)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "client_secret") {
		t.Fatalf("the operator wasn't told: %q", warned)
	}
	noTokens(t, "the warning", warned[0])
	row := f.store.row(v.ID)
	if row.Status != BitbucketReady || row.Message != "" || f.store.saves != saves {
		t.Fatalf("a rejected consumer changed the row: %s %q, %d saves", row.Status, row.Message, f.store.saves-saves)
	}
	if now, ok := f.secrets.get("aorg_a", v.ID); !ok || now != held {
		t.Fatal("a rejected consumer touched the stored sign-in")
	}
	st, err := f.conns.Status(ctx, admin)
	if err != nil || len(st.Problems) != 1 || !strings.Contains(st.Problems[0], "private repositories") {
		t.Fatalf("status with a rejected consumer = %+v, %v", st, err)
	}
	noTokens(t, "the status", jsonOf(t, st))
	if st.Workspaces[0].Status != BitbucketReady {
		t.Fatalf("the workspace reads %s", st.Workspaces[0].Status)
	}
	list, err := f.conns.Repositories(ctx, admin)
	if err != nil || len(list.Problems) != 1 {
		t.Fatalf("listing with a rejected consumer = %+v, %v", list, err)
	}
	if row := f.store.row(v.ID); row.Status != BitbucketReady {
		t.Fatalf("listing marked the row %s", row.Status)
	}

	// The operator fixes the configuration, and the same sign-in renews.
	f.api.set(func(a *fakeBBAPI) { a.refreshErr = nil })
	tok, err = f.conns.Token(ctx, "aorg_a", repo("acme", "api"))
	if err != nil || !strings.HasPrefix(tok, bbAccessPrefix) {
		t.Fatalf("Token once the consumer is fixed = %q, %v", tok, err)
	}
	if f.api.exchanges != 1 {
		t.Fatalf("%d sign-ins, want the first one only", f.api.exchanges)
	}
	if st, err = f.conns.Status(ctx, admin); err != nil || len(st.Problems) != 0 {
		t.Fatalf("status once the consumer is fixed = %+v, %v", st, err)
	}
}

// With a rejected consumer an analysis of a public repository in a
// connected workspace still reads it, anonymously, and a private one is
// one Bitbucket won't show.
func TestBitbucketToken_rejectedConsumerStillReadsPublicRepositories(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	f.connect(t, admin, "acme")
	f.api.set(func(a *fakeBBAPI) { a.refreshErr = ErrBitbucketConsumerRejected })
	f.expire()

	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		signedIn := r.Header.Get("Authorization") != ""
		switch r.URL.EscapedPath() {
		case "/repositories/acme/site": // public
			_, _ = w.Write([]byte(`{"mainbranch":{"name":"trunk"}}`))
		case "/repositories/acme/site/refs/branches/trunk":
			_, _ = w.Write([]byte(`{"target":{"hash":"` + sha + `"}}`))
		case "/repositories/acme/api", "/repositories/acme/api/refs/branches/trunk": // private
			if !signedIn {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"mainbranch":{"name":"trunk"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	bb := NewBitbucket(srv.URL, srv.URL)

	// What the runner does: a token for the repository, then Resolve.
	tok, err := f.conns.Token(ctx, "aorg_a", repo("acme", "site"))
	if err != nil {
		t.Fatalf("repository access: %v", err)
	}
	got, err := bb.Resolve(ctx, repo("acme", "site"), "", tok)
	if err != nil || got != sha {
		t.Fatalf("Resolve the public repository = %q, %v", got, err)
	}
	if tok, err = f.conns.Token(ctx, "aorg_a", repo("acme", "api")); err != nil {
		t.Fatalf("repository access: %v", err)
	}
	if _, err = bb.Resolve(ctx, repo("acme", "api"), "", tok); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve the private repository: %v, want ErrNotFound", err)
	}
	for _, a := range auth {
		if a != "" {
			t.Fatalf("Bitbucket was sent a sign-in: %q", a)
		}
	}
}

// Connecting with a rejected consumer says so, and keeps nothing.
func TestBitbucketConnect_rejectedConsumer(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	f.api.set(func(a *fakeBBAPI) { a.exchangeErr = ErrBitbucketConsumerRejected })
	_, err := f.conns.Connect(ctx, admin, BitbucketConnectInput{Code: bbGoodCode, State: f.stateFor(t, admin, "acme")})
	if !errors.Is(err, ErrBitbucketConsumerRejected) {
		t.Fatalf("Connect: %v, want ErrBitbucketConsumerRejected", err)
	}
	if rows, _ := f.store.ListBitbucketWorkspaces(ctx, "aorg_a"); len(rows) != 0 || f.secrets.len() != 0 {
		t.Fatalf("a rejected consumer kept %d rows and %d secrets", len(rows), f.secrets.len())
	}
	// The card says so too, until a sign-in works.
	st, err := f.conns.Status(ctx, admin)
	if err != nil || len(st.Problems) != 1 || st.Problems[0] != bitbucketConsumerProblem {
		t.Fatalf("status after a rejected connect = %+v, %v", st, err)
	}
	f.api.set(func(a *fakeBBAPI) { a.exchangeErr = nil })
	f.connect(t, admin, "acme")
	if st, err = f.conns.Status(ctx, admin); err != nil || len(st.Problems) != 0 {
		t.Fatalf("status once a connect works = %+v, %v", st, err)
	}
}

func TestBitbucketToken_connectorLeft(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	v := f.connect(t, admin, "acme")
	f.store.setMember("aorg_a", admin.UserID, false)

	f.expire()
	if tok, err := f.conns.Token(ctx, "aorg_a", repo("acme", "api")); tok != "" || err != nil {
		t.Fatalf("Token after the connector left = %q, %v", tok, err)
	}
	row := f.store.row(v.ID)
	if row.Status != BitbucketReconnectNeeded || row.Message != bitbucketConnectorLeftMessage {
		t.Fatalf("row = %s %q", row.Status, row.Message)
	}
	if refreshes, _ := f.api.counts(); refreshes != 0 {
		t.Fatalf("%d refreshes on behalf of someone who left", refreshes)
	}
}

func TestBitbucketRepositories_oneWorkspaceFailing(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	viewer := f.as(t, "aorg_a", "ausr_viewer", security.RoleViewer)
	acme := f.connect(t, admin, "acme")
	f.connect(t, admin, "other")
	f.api.set(func(a *fakeBBAPI) {
		a.repos["acme"] = []BitbucketRepository{
			{FullName: "acme/web", DefaultBranch: "main"},
			{FullName: "acme/API", Private: true, DefaultBranch: "trunk"},
		}
		a.repoErr["other"] = ErrRateLimited
	})

	list, err := f.conns.Repositories(ctx, viewer)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Repositories) != 2 || list.Repositories[0].FullName != "acme/API" || list.Repositories[0].Workspace != acme.ID ||
		!list.Repositories[0].Private || list.Repositories[1].FullName != "acme/web" {
		t.Fatalf("repositories = %+v", list.Repositories)
	}
	if len(list.Problems) != 1 || !strings.Contains(list.Problems[0], "other") {
		t.Fatalf("problems = %q, want one naming the failing workspace", list.Problems)
	}
	noTokens(t, "the list", jsonOf(t, list))
	for _, tok := range f.api.repoTokens {
		if !strings.HasPrefix(tok, bbAccessPrefix) {
			t.Fatalf("Repositories was called with %q", tok)
		}
	}

	// A workspace that needs connecting again is named, and not asked.
	if err = f.store.SetBitbucketWorkspaceStatus(ctx, "aorg_a", acme.ID, BitbucketReconnectNeeded, bitbucketRefusedMessage); err != nil {
		t.Fatal(err)
	}
	calls := len(f.api.repoTokens)
	list, err = f.conns.Repositories(ctx, viewer)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Repositories) != 0 || len(list.Problems) != 2 || len(f.api.repoTokens) != calls+1 {
		t.Fatalf("list = %+v after %d calls", list, len(f.api.repoTokens)-calls)
	}
	if _, err := f.conns.Repositories(ctx, security.Principal{OrgID: "aorg_a", UserID: "ausr_stranger"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a stranger listing: %v", err)
	}
}

func TestBitbucketDisconnect(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	dev := f.as(t, "aorg_a", "ausr_dev", security.RoleDeveloper)
	elsewhere := f.as(t, "aorg_b", "ausr_b", security.RoleAdmin)
	v := f.connect(t, admin, "acme")
	vb := f.connect(t, elsewhere, "acme")
	if tok, err := f.conns.Token(ctx, "aorg_a", repo("acme", "api")); tok == "" || err != nil {
		t.Fatalf("Token = %q, %v", tok, err)
	}

	var called []string
	f.conns.SetOnDisconnect(func(_ context.Context, orgID, connectionID string) {
		// It runs while the row is still there to read.
		if f.store.row(connectionID) == nil {
			t.Error("OnDisconnect ran after the row was deleted")
		}
		called = append(called, orgID+"/"+connectionID)
	})
	if err := f.conns.Disconnect(ctx, dev, v.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a developer disconnecting: %v", err)
	}
	if err := f.conns.Disconnect(ctx, admin, vb.ID); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("disconnecting another org's workspace: %v", err)
	}
	if len(called) != 0 {
		t.Fatalf("OnDisconnect ran for a refused disconnect: %v", called)
	}
	// The caller giving up partway must not leave the refresh token behind.
	gone, giveUp := context.WithCancel(ctx)
	f.conns.SetOnDisconnect(func(_ context.Context, orgID, connectionID string) {
		if f.store.row(connectionID) == nil {
			t.Error("OnDisconnect ran after the row was deleted")
		}
		called = append(called, orgID+"/"+connectionID)
		giveUp()
	})
	if err := f.conns.Disconnect(gone, admin, v.ID); err != nil {
		t.Fatal(err)
	}
	if len(called) != 1 || called[0] != "aorg_a/"+v.ID {
		t.Fatalf("OnDisconnect calls = %v", called)
	}
	if f.store.row(v.ID) != nil {
		t.Fatal("the row is still there")
	}
	if _, ok := f.secrets.get("aorg_a", v.ID); ok {
		t.Fatal("the refresh token is still in Vault")
	}
	f.conns.mu.Lock()
	_, cached := f.conns.tokens["aorg_a/"+v.ID]
	f.conns.mu.Unlock()
	if cached {
		t.Fatal("the access token is still in memory")
	}
	if tok, err := f.conns.Token(ctx, "aorg_a", repo("acme", "api")); tok != "" || err != nil {
		t.Fatalf("Token after disconnecting = %q, %v", tok, err)
	}
	if _, ok := f.secrets.get("aorg_b", vb.ID); f.store.row(vb.ID) == nil || !ok {
		t.Fatal("org B's connection went too")
	}
	if tok, err := f.conns.Token(ctx, "aorg_b", repo("acme", "api")); tok == "" || err != nil {
		t.Fatalf("org B's Token = %q, %v", tok, err)
	}
	last := f.audit.records[len(f.audit.records)-1]
	if last.action != "bitbucket.disconnect" || last.c.ID != v.ID || last.p.UserID != admin.UserID {
		t.Fatalf("audit = %+v", last)
	}
	noTokens(t, "the audit records", fmt.Sprintf("%+v", f.audit.records))
}

// Once the row is deleted the workspace is disconnected, so the audit
// records it even when releasing the lock then fails because the caller
// gave up.
func TestBitbucketDisconnect_auditsWhenTheLockReleaseFails(t *testing.T) {
	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	v := f.connect(t, admin, "acme")
	var warned []string
	f.conns.SetWarn(func(msg string, _ error) { warned = append(warned, msg) })

	ctx, giveUp := context.WithCancel(context.Background())
	f.store.commit = func(ctx context.Context) error {
		giveUp()
		return ctx.Err()
	}
	audited := len(f.audit.records)
	if err := f.conns.Disconnect(ctx, admin, v.ID); err != nil {
		t.Fatalf("Disconnect = %v; the workspace is gone, so it disconnected", err)
	}
	if f.store.row(v.ID) != nil {
		t.Fatal("the row is still there")
	}
	if _, ok := f.secrets.get("aorg_a", v.ID); ok {
		t.Fatal("the refresh token is still in Vault")
	}
	if len(f.audit.records) != audited+1 {
		t.Fatalf("%d audit records for the disconnect, want 1", len(f.audit.records)-audited)
	}
	if last := f.audit.records[len(f.audit.records)-1]; last.action != "bitbucket.disconnect" || last.c.ID != v.ID {
		t.Fatalf("audit = %+v", last)
	}
	if len(warned) != 1 {
		t.Fatalf("warnings = %q, want the lock's", warned)
	}

	// Disconnecting it again finds nothing, and records nothing.
	f.store.commit = nil
	if err := f.conns.Disconnect(context.Background(), admin, v.ID); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("a second Disconnect = %v", err)
	}
	if len(f.audit.records) != audited+1 {
		t.Fatal("a failed disconnect was recorded")
	}
}

// A reconnect keeps the connection's ID, so a process that cached a token
// from the old sign-in must notice the row now holds another.
func TestBitbucketToken_reconnectRetiresOtherProcessesTokens(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	leaver := f.as(t, "aorg_a", "ausr_leaver", security.RoleAdmin)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	p1, p2 := f.newConns(), f.newConns()
	f.conns = p2
	v := f.connect(t, leaver, "acme")

	// P1 caches T1 from the leaver's sign-in. P2's token from connecting
	// is due by now too.
	f.expire()
	t1, err := p1.Token(ctx, "aorg_a", repo("acme", "api"))
	if err != nil || t1 == "" {
		t.Fatalf("P1 Token = %q, %v", t1, err)
	}
	// The leaver goes. P2 renews, finds it, and marks the workspace.
	f.store.setMember("aorg_a", leaver.UserID, false)
	if tok, tokErr := p2.Token(ctx, "aorg_a", repo("acme", "api")); tok != "" || tokErr != nil {
		t.Fatalf("P2 Token after the leaver left = %q, %v", tok, tokErr)
	}
	if row := f.store.row(v.ID); row.Status != BitbucketReconnectNeeded {
		t.Fatalf("status = %s", row.Status)
	}
	// An admin reconnects through P2. The row is ready again, same ID.
	again := f.connect(t, admin, "acme")
	if again.ID != v.ID {
		t.Fatalf("reconnect changed the ID from %s to %s", v.ID, again.ID)
	}
	// T1 has nearly two hours left, but it's the leaver's.
	before, _ := f.api.counts()
	tok, err := p1.Token(ctx, "aorg_a", repo("acme", "api"))
	if err != nil || tok == "" || tok == t1 {
		t.Fatalf("P1 Token after the reconnect = %q, %v; T1 was %q", tok, err, t1)
	}
	if after, _ := f.api.counts(); after != before+1 {
		t.Fatalf("P1 made %d refreshes, want one from the new sign-in", after-before)
	}
	if row := f.store.row(v.ID); row.Status != BitbucketReady || row.CreatedBy != admin.UserID {
		t.Fatalf("row = %s by %s", row.Status, row.CreatedBy)
	}
}

// After a renewal, a Bitbucket account that left the workspace can't read
// in it, so the connection needs connecting again.
func TestBitbucketToken_accountLeftTheWorkspace(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	v := f.connect(t, admin, "acme")

	// A lookup that merely failed changes nothing.
	f.api.set(func(a *fakeBBAPI) { a.lookupErr = ErrRateLimited })
	f.expire()
	tok, err := f.conns.Token(ctx, "aorg_a", repo("acme", "api"))
	if err != nil || tok == "" {
		t.Fatalf("Token when the lookup failed = %q, %v", tok, err)
	}
	if row := f.store.row(v.ID); row.Status != BitbucketReady {
		t.Fatalf("a failed lookup changed the status to %s", row.Status)
	}

	f.api.set(func(a *fakeBBAPI) {
		a.lookupErr = nil
		delete(a.workspaces, "acme")
	})
	f.expire()
	if tok, err = f.conns.Token(ctx, "aorg_a", repo("acme", "api")); tok != "" || err != nil {
		t.Fatalf("Token after the account left = %q, %v", tok, err)
	}
	row := f.store.row(v.ID)
	if row.Status != BitbucketReconnectNeeded || row.Message != bitbucketAccountLeftMessage {
		t.Fatalf("row = %s %q", row.Status, row.Message)
	}
	f.conns.mu.Lock()
	_, cached := f.conns.tokens["aorg_a/"+v.ID]
	f.conns.mu.Unlock()
	if cached {
		t.Fatal("the access token is still in memory")
	}
}

func TestBitbucketToken_missingSecretMarksReconnect(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	v := f.connect(t, admin, "acme")
	// Vault lost the sign-in, as the real one reports it.
	f.secrets.missing = true
	f.expire()

	tok, err := f.conns.Token(ctx, "aorg_a", repo("acme", "api"))
	if tok != "" || err != nil {
		t.Fatalf("Token with the sign-in gone = %q, %v; want an anonymous read", tok, err)
	}
	row := f.store.row(v.ID)
	if row.Status != BitbucketReconnectNeeded || row.Message != bitbucketUnreadableMessage {
		t.Fatalf("row = %s %q", row.Status, row.Message)
	}
	if refreshes, _ := f.api.counts(); refreshes != 0 {
		t.Fatalf("%d refreshes with no refresh token to spend", refreshes)
	}
}

// Connection reads a connection as it's stored, and renews nothing.
func TestBitbucketConnection_readsWithoutRenewing(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	v := f.connect(t, admin, "acme")
	f.expire()
	got, err := f.conns.Connection(ctx, "aorg_a", v.ID)
	if err != nil || got.ID != v.ID || got.Status != BitbucketReady {
		t.Fatalf("Connection = %+v, %v", got, err)
	}
	if refreshes, _ := f.api.counts(); refreshes != 0 {
		t.Fatalf("%d refreshes to read a connection", refreshes)
	}
	if _, err := f.conns.Connection(ctx, "aorg_b", v.ID); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("another organization's connection: %v", err)
	}
	none := NewBitbucketConnections(f.store, nil, f.secrets, f.authz, nil, f.key)
	if _, err := none.Connection(ctx, "aorg_a", v.ID); !errors.Is(err, ErrBitbucketNotConfigured) {
		t.Fatalf("unconfigured Connection: %v", err)
	}
}

// ReadyConnection reads the organization's ready connection to a workspace
// as it's stored, and never renews its sign-in, even with a rejected
// consumer.
func TestBitbucketReadyConnection_neverRenews(t *testing.T) {
	ctx := context.Background()
	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	v := f.connect(t, admin, "acme")
	f.expire()
	f.api.set(func(a *fakeBBAPI) { a.refreshErr = ErrBitbucketConsumerRejected })

	got, err := f.conns.ReadyConnection(ctx, "aorg_a", "ACME")
	if err != nil || got.ID != v.ID || got.Status != BitbucketReady {
		t.Fatalf("ReadyConnection = %+v, %v", got, err)
	}
	if refreshes, lookups := f.api.counts(); refreshes != 0 || lookups != 0 {
		t.Fatalf("%d refreshes and %d lookups to read a connection", refreshes, lookups)
	}
	if f.conns.consumerWasRejected() {
		t.Fatal("reading a connection flagged the consumer")
	}
	if _, err := f.conns.ReadyConnection(ctx, "aorg_b", "acme"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("another organization's: %v", err)
	}
	if _, err := f.conns.ReadyConnection(ctx, "aorg_a", "other"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("a workspace that isn't connected: %v", err)
	}
	if err := f.store.SetBitbucketWorkspaceStatus(ctx, "aorg_a", v.ID, BitbucketReconnectNeeded, "again"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conns.ReadyConnection(ctx, "aorg_a", "acme"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("one that needs connecting again: %v", err)
	}
	none := NewBitbucketConnections(f.store, nil, f.secrets, f.authz, nil, f.key)
	if _, err := none.ReadyConnection(ctx, "aorg_a", "acme"); !errors.Is(err, ErrBitbucketNotConfigured) {
		t.Fatalf("unconfigured: %v", err)
	}
}
