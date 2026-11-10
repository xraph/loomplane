package repositories_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

// The instance host every test signs in through, and the secrets that must
// never leave the service.
const (
	gcHost           = "gitlab.example.com"
	gcInstanceSecret = "instance-client-secret-xyz"
	gcOrgSecret      = "org-client-secret-xyz"
)

// memGitLabStore is a GitLabStore in memory.
type memGitLabStore struct {
	mu      sync.Mutex
	hosts   []*repositories.GitLabHost
	signIns map[string]*repositories.GitLabSignIn
	conns   []*repositories.GitLabConnection
	locks   map[string]*sync.Mutex
	seq     int
}

func newMemGitLabStore() *memGitLabStore {
	return &memGitLabStore{signIns: map[string]*repositories.GitLabSignIn{}, locks: map[string]*sync.Mutex{}}
}

func (s *memGitLabStore) SaveHost(_ context.Context, h *repositories.GitLabHost) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.hosts {
		if e.OrgID == h.OrgID && strings.EqualFold(e.Host, h.Host) {
			return repositories.ErrGitLabHostExists
		}
	}
	if h.ID == "" {
		s.seq++
		h.ID = "glh_" + strconv.Itoa(s.seq)
	}
	cp := *h
	s.hosts = append(s.hosts, &cp)
	return nil
}

func (s *memGitLabStore) ListHosts(_ context.Context, orgID string) ([]*repositories.GitLabHost, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*repositories.GitLabHost
	for _, h := range s.hosts {
		if h.OrgID == orgID {
			cp := *h
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *memGitLabStore) HostsNamed(_ context.Context, host string) ([]*repositories.GitLabHost, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*repositories.GitLabHost
	for _, h := range s.hosts {
		if strings.EqualFold(h.Host, host) {
			cp := *h
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *memGitLabStore) DeleteHost(_ context.Context, orgID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, h := range s.hosts {
		if h.OrgID == orgID && h.ID == id {
			for _, c := range s.conns {
				if c.OrgID == orgID && c.HostID == id {
					return repositories.ErrGitLabHostInUse
				}
			}
			s.hosts = append(s.hosts[:i], s.hosts[i+1:]...)
			return nil
		}
	}
	return repositories.ErrGitLabHostNotFound
}

func (s *memGitLabStore) SaveSignIn(_ context.Context, in *repositories.GitLabSignIn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *in
	s.signIns[in.State] = &cp
	return nil
}

func (s *memGitLabStore) TakeSignIn(_ context.Context, state string, now time.Time) (*repositories.GitLabSignIn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.signIns[state]
	delete(s.signIns, state)
	if !ok || !in.ExpiresAt.After(now) {
		return nil, repositories.ErrBadState
	}
	return in, nil
}

func (s *memGitLabStore) SaveConnection(_ context.Context, c *repositories.GitLabConnection) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.conns {
		if e.OrgID == c.OrgID && strings.EqualFold(e.Host, c.Host) && e.GitLabUserID == c.GitLabUserID {
			e.Username, e.AvatarURL, e.Version, e.LapsedAt = c.Username, c.AvatarURL, c.Version, nil
			return e.ID, nil
		}
	}
	cp := *c
	if cp.ID == "" {
		s.seq++
		cp.ID = "glc_" + strconv.Itoa(s.seq)
	}
	cp.LapsedAt, cp.WebhookSet = nil, false
	s.conns = append(s.conns, &cp)
	return cp.ID, nil
}

func (s *memGitLabStore) ListConnections(_ context.Context, orgID string) ([]*repositories.GitLabConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*repositories.GitLabConnection
	for _, c := range s.conns {
		if c.OrgID == orgID {
			cp := *c
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *memGitLabStore) GetConnection(_ context.Context, id string) (*repositories.GitLabConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		if c.ID == id {
			cp := *c
			return &cp, nil
		}
	}
	return nil, repositories.ErrGitLabConnectionNotFound
}

func (s *memGitLabStore) MarkLapsed(_ context.Context, orgID, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		if c.OrgID == orgID && c.ID == id {
			c.LapsedAt = &at
		}
	}
	return nil
}

func (s *memGitLabStore) DeleteConnection(_ context.Context, orgID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.conns {
		if c.OrgID == orgID && c.ID == id {
			s.conns = append(s.conns[:i], s.conns[i+1:]...)
			return nil
		}
	}
	return repositories.ErrGitLabConnectionNotFound
}

func (s *memGitLabStore) WithGrantLock(_ context.Context, orgID, id string, fn func() error) error {
	s.mu.Lock()
	l := s.locks[orgID+"/"+id]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[orgID+"/"+id] = l
	}
	s.mu.Unlock()
	l.Lock()
	defer l.Unlock()
	return fn()
}

func (s *memGitLabStore) SetWebhookSet(_ context.Context, orgID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		if c.OrgID == orgID && c.ID == id {
			c.WebhookSet = true
			return nil
		}
	}
	return repositories.ErrGitLabConnectionNotFound
}

func (s *memGitLabStore) setWebhook(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		if c.ID == id {
			c.WebhookSet = true
		}
	}
}

func (s *memGitLabStore) connection(t *testing.T, id string) *repositories.GitLabConnection {
	t.Helper()
	c, err := s.GetConnection(context.Background(), id)
	if err != nil {
		t.Fatalf("connection %s: %v", id, err)
	}
	return c
}

// memSecrets is Secrets in memory.
type memSecrets struct {
	mu   sync.Mutex
	vals map[string][]byte
}

func (m *memSecrets) Put(_ context.Context, orgID, id string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vals[orgID+"/"+id] = append([]byte(nil), value...)
	return nil
}

func (m *memSecrets) Reveal(_ context.Context, orgID, id string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.vals[orgID+"/"+id]
	if !ok {
		return nil, fmt.Errorf("no secret %s: %w", id, repositories.ErrSecretNotFound)
	}
	return v, nil
}

func (m *memSecrets) Delete(_ context.Context, orgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.vals, orgID+"/"+id)
	return nil
}

// grant reads the grant org-a keeps for connection id.
func (m *memSecrets) grant(t *testing.T, id string) oauth.Grant {
	t.Helper()
	raw, err := m.Reveal(context.Background(), "org-a", id)
	if err != nil {
		t.Fatalf("grant of %s: %v", id, err)
	}
	var g oauth.Grant
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func (m *memSecrets) setGrant(t *testing.T, orgID, id string, g oauth.Grant) {
	t.Helper()
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Put(context.Background(), orgID, id, raw)
}

func (m *memSecrets) dump() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	for k, v := range m.vals {
		b.WriteString(k + "=" + string(v) + "\n")
	}
	return b.String()
}

// permAuthz allows the permissions it holds.
type permAuthz map[security.Permission]bool

func (a permAuthz) Require(_ context.Context, _ security.Principal, perm security.Permission, _ string) error {
	if !a[perm] {
		return fmt.Errorf("%w (%s)", security.ErrForbidden, perm.Name())
	}
	return nil
}

func (a permAuthz) Allowed(_ context.Context, _ security.Principal, perm security.Permission) (bool, error) {
	return a[perm], nil
}

var (
	allowAll = permAuthz{security.ProjectRead: true, security.RepositoryConnect: true}
	readOnly = permAuthz{security.ProjectRead: true}
	denyAll  = permAuthz{}
)

// recAudit keeps what was recorded.
type recAudit struct {
	mu      sync.Mutex
	actions []string
	details []map[string]any
}

func (r *recAudit) Record(_ context.Context, action string, _ security.Principal, details map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actions = append(r.actions, action)
	r.details = append(r.details, details)
}

// gcUser is a GitLab account the fake knows, and what it can see.
type gcUser struct {
	id       int64
	name     string
	projects []string // paths with namespace
}

// gcFake plays GitLab for the connection service: OAuth, the user, the
// version, projects and the project listing. A code is "code-<user>", and
// the tokens it hands out are "at-<user>-<n>" and "rt-<user>-<n>".
type gcFake struct {
	mu        sync.Mutex
	users     map[string]gcUser
	version   string
	expiresIn int
	refuse    bool // refresh answers invalid_grant
	refreshes atomic.Int32
	revoked   []string
	// revokeHints are the token_type_hint of each revoke, in order.
	revokeHints []string
	// revokeStatus, when set, answers every revoke.
	revokeStatus int
	// revokeHold, when set, holds each revoke until it's closed.
	revokeHold chan struct{}
	// failList are the users whose project listing GitLab answers 401.
	failList map[string]bool
	forms    []url.Values
	seq      int
	slow     time.Duration // delay on each refresh
	// refreshStatus and refreshBody, when set, answer every refresh.
	refreshStatus int
	refreshBody   string
	// refreshStarted, when set, hears of each refresh as it arrives.
	refreshStarted chan struct{}
	// public are the projects whose visibility is public.
	public map[string]bool
}

func (f *gcFake) userOf(r *http.Request) (gcUser, bool) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	parts := strings.Split(tok, "-")
	if len(parts) != 3 || parts[0] != "at" {
		return gcUser{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[parts[1]]
	return u, ok
}

func (f *gcFake) issue(w http.ResponseWriter, user string) {
	f.mu.Lock()
	f.seq++
	n, exp := f.seq, f.expiresIn
	f.mu.Unlock()
	_, _ = fmt.Fprintf(w, `{"access_token":"at-%s-%d","refresh_token":"rt-%s-%d","expires_in":%d}`, user, n, user, n, exp)
}

func (f *gcFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/oauth/token":
		_ = r.ParseForm()
		f.mu.Lock()
		f.forms = append(f.forms, r.PostForm)
		refuse, slow, status, body, started := f.refuse, f.slow, f.refreshStatus, f.refreshBody, f.refreshStarted
		f.mu.Unlock()
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			f.issue(w, strings.TrimPrefix(r.PostForm.Get("code"), "code-"))
		case "refresh_token":
			f.refreshes.Add(1)
			if started != nil {
				select {
				case started <- struct{}{}:
				default:
				}
			}
			time.Sleep(slow)
			if status != 0 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
				return
			}
			if refuse {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"The provided authorization grant is invalid"}`))
				return
			}
			parts := strings.Split(r.PostForm.Get("refresh_token"), "-")
			f.issue(w, parts[1])
		}
	case r.Method == http.MethodPost && r.URL.Path == "/oauth/revoke":
		_ = r.ParseForm()
		f.mu.Lock()
		hold := f.revokeHold
		f.mu.Unlock()
		if hold != nil {
			<-hold
		}
		f.mu.Lock()
		f.revoked = append(f.revoked, r.PostForm.Get("token"))
		f.revokeHints = append(f.revokeHints, r.PostForm.Get("token_type_hint"))
		status := f.revokeStatus
		f.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(`{}`))
	case r.URL.Path == "/api/v4/user":
		u, ok := f.userOf(r)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":%d,"username":%q,"avatar_url":"https://gitlab.example.com/%s.png"}`, u.id, u.name, u.name)
	case r.URL.Path == "/api/v4/version":
		f.mu.Lock()
		v := f.version
		f.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"version":%q}`, v)
	case r.URL.Path == "/api/v4/projects":
		u, ok := f.userOf(r)
		f.mu.Lock()
		revoked := f.failList[u.name]
		f.mu.Unlock()
		if !ok || revoked {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		out := make([]map[string]any, 0, len(u.projects))
		for _, p := range u.projects {
			ns := p[:strings.LastIndex(p, "/")]
			pr := map[string]any{
				"id": 1, "path_with_namespace": p, "name": strings.ToUpper(p[strings.LastIndex(p, "/")+1:]),
				"default_branch": "main", "namespace": map[string]string{"full_path": ns},
			}
			// GitLab's simple representation has no visibility.
			if !r.URL.Query().Has("simple") {
				pr["visibility"] = "private"
				f.mu.Lock()
				if f.public[p] {
					pr["visibility"] = "public"
				}
				f.mu.Unlock()
			}
			out = append(out, pr)
		}
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(r.URL.EscapedPath(), "/api/v4/projects/"):
		path, _ := url.PathUnescape(strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4/projects/"))
		if r.Header.Get("Authorization") == "" {
			// Anonymously, only a public project shows; GitLab hides the
			// rest behind a 404.
			f.mu.Lock()
			defer f.mu.Unlock()
			for p := range f.public {
				if strings.EqualFold(p, path) {
					_, _ = fmt.Fprintf(w, `{"id":1,"path_with_namespace":%q,"default_branch":"main"}`, p)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		u, ok := f.userOf(r)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		for _, p := range u.projects {
			if strings.EqualFold(p, path) {
				_, _ = fmt.Fprintf(w, `{"id":1,"path_with_namespace":%q,"default_branch":"main"}`, p)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type gcFixture struct {
	svc     *repositories.GitLabConnections
	fake    *gcFake
	store   *memGitLabStore
	secrets *memSecrets
	audit   *recAudit
}

func newGCFixture(t *testing.T, authz repositories.Authorizer, orgHosts bool) *gcFixture {
	t.Helper()
	f := &gcFixture{
		fake: &gcFake{
			version: "17.4.2-ee", expiresIn: 7200, public: map[string]bool{"Grp/a/web": true},
			users: map[string]gcUser{
				"alice": {id: 1, name: "alice", projects: []string{"grp/sub/api", "Grp/a/web"}},
				"bob":   {id: 2, name: "bob", projects: []string{"grp/sub/api", "grp/secret"}},
				"carol": {id: 3, name: "carol", projects: []string{"abc/x"}},
			},
		},
		store:   newMemGitLabStore(),
		secrets: &memSecrets{vals: map[string][]byte{}},
		audit:   &recAudit{},
	}
	srv := httptest.NewServer(f.fake)
	t.Cleanup(srv.Close)
	reg := &repositories.GitLabRegistry{
		Instance: []repositories.GitLabHost{
			{WebURL: "https://" + gcHost, Host: gcHost, ClientID: "app-id"},
			{WebURL: "https://gitlab.com", Host: "gitlab.com", ClientID: "com-app-id"},
			{WebURL: "https://public.example.com", Host: "public.example.com"},
		},
		Store:                  f.store,
		AllowOrganizationHosts: orgHosts,
	}
	f.svc = repositories.NewGitLabConnections(repositories.GitLabConnectionsConfig{
		Store: f.store, Secrets: f.secrets, Authz: authz, Audit: f.audit, Registry: reg,
		InstanceSecrets: map[string]string{gcHost: gcInstanceSecret, "gitlab.com": "com-secret"},
		CallbackURL:     "https://loomplane.example/integrations/gitlab/callback",
		WebhookBaseURL:  "https://hooks.example/",
		Client: func(repositories.GitLabHost) *repositories.GitLab {
			return repositories.NewGitLab(srv.URL, srv.Client())
		},
	})
	return f
}

func gcPrincipal(org, user string) security.Principal {
	return security.Principal{OrgID: org, UserID: user}
}

// connect signs user in to host for p, and returns the connection.
func (f *gcFixture) connect(t *testing.T, p security.Principal, hostID, user string) *repositories.GitLabConnectionView {
	t.Helper()
	start, err := f.svc.StartSignIn(context.Background(), p, repositories.StartGitLabSignInInput{HostID: hostID})
	if err != nil {
		t.Fatalf("StartSignIn: %v", err)
	}
	u, err := url.Parse(start.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.svc.Connect(context.Background(), p, repositories.ConnectGitLabInput{Code: "code-" + user, State: u.Query().Get("state")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// A reconnect's revoke of the grant it replaced runs on its own.
	repositories.WaitGitLabBackground(f.svc)
	return v
}

var gcAPI = repositories.Repository{Host: gcHost, Owner: "grp/sub", Name: "api"}

func TestGitLabSignIn_authorizeURLAsksReadAPIWithPKCE(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	start, err := f.svc.StartSignIn(context.Background(), gcPrincipal("org-a", "user-1"), repositories.StartGitLabSignInInput{HostID: "instance:" + gcHost})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(start.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("scope") != "read_api" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("authorize URL = %s", start.AuthorizeURL)
	}
	if q.Get("client_id") != "app-id" || q.Get("redirect_uri") != "https://loomplane.example/integrations/gitlab/callback" {
		t.Fatalf("authorize URL = %s", start.AuthorizeURL)
	}
	if raw, err := base64URLDecode(q.Get("state")); err != nil || len(raw) != 32 {
		t.Fatalf("state %q is not 32 random bytes: %v", q.Get("state"), err)
	}
	if strings.Contains(start.AuthorizeURL, gcInstanceSecret) {
		t.Fatal("the authorize URL carries the client secret")
	}
}

func TestGitLabSignIn_pendingSignInExpiresIn15Minutes(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	before := time.Now()
	start, err := f.svc.StartSignIn(context.Background(), gcPrincipal("org-a", "user-1"), repositories.StartGitLabSignInInput{HostID: "instance:" + gcHost})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(start.AuthorizeURL)
	in, err := f.store.TakeSignIn(context.Background(), u.Query().Get("state"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if d := in.ExpiresAt.Sub(before); d < 15*time.Minute || d > 15*time.Minute+5*time.Second {
		t.Fatalf("sign-in expires after %s, want 15m", d)
	}
	if in.OrgID != "org-a" || in.UserID != "user-1" || in.Host != gcHost || in.Verifier == "" {
		t.Fatalf("sign-in = %+v", in)
	}
}

func TestGitLabSignIn_hostWithoutAnAppIsNotOffered(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	_, err := f.svc.StartSignIn(context.Background(), gcPrincipal("org-a", "user-1"), repositories.StartGitLabSignInInput{HostID: "instance:public.example.com"})
	if !errors.Is(err, repositories.ErrGitLabHostNotFound) {
		t.Fatalf("StartSignIn on a public-only host = %v, want ErrGitLabHostNotFound", err)
	}
	st, err := f.svc.Status(context.Background(), gcPrincipal("org-a", "user-1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range st.Hosts {
		if h.Host == "public.example.com" {
			t.Fatal("status offers a host nobody can sign in to")
		}
	}
}

func TestGitLabSignIn_anotherOrgsHostIsNotFound(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	h, err := f.svc.AddHost(context.Background(), gcPrincipal("org-a", "user-1"), repositories.AddGitLabHostInput{
		WebURL: "https://git.a.example", ClientID: "a-app", ClientSecret: gcOrgSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.StartSignIn(context.Background(), gcPrincipal("org-b", "user-2"), repositories.StartGitLabSignInInput{HostID: h.ID})
	if !errors.Is(err, repositories.ErrGitLabHostNotFound) {
		t.Fatalf("org-b signing in through org-a's host = %v, want ErrGitLabHostNotFound", err)
	}
	// org-a's own host signs in, with its own application.
	start, err := f.svc.StartSignIn(context.Background(), gcPrincipal("org-a", "user-1"), repositories.StartGitLabSignInInput{HostID: h.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(start.AuthorizeURL, "client_id=a-app") {
		t.Fatalf("authorize URL = %s", start.AuthorizeURL)
	}
}

func TestGitLabConnect_rejectsAnotherOrgsState(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	start, err := f.svc.StartSignIn(context.Background(), gcPrincipal("org-a", "user-1"), repositories.StartGitLabSignInInput{HostID: "instance:" + gcHost})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(start.AuthorizeURL)
	_, err = f.svc.Connect(context.Background(), gcPrincipal("org-b", "user-1"), repositories.ConnectGitLabInput{Code: "code-alice", State: u.Query().Get("state")})
	if !errors.Is(err, repositories.ErrGitLabBadState) {
		t.Fatalf("Connect from org-b = %v, want ErrGitLabBadState", err)
	}
	if strings.Contains(err.Error(), "GitHub") {
		t.Fatalf("the error names GitHub: %v", err)
	}
	for _, org := range []string{"org-a", "org-b"} {
		if cs, _ := f.store.ListConnections(context.Background(), org); len(cs) != 0 {
			t.Fatalf("%s has connections %v", org, cs)
		}
	}
	if d := f.secrets.dump(); d != "" {
		t.Fatalf("secrets saved: %s", d)
	}
	if len(f.fake.forms) != 0 {
		t.Fatal("the code was exchanged")
	}
}

func TestGitLabConnect_rejectsAnotherUsersState(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	start, err := f.svc.StartSignIn(context.Background(), gcPrincipal("org-a", "user-1"), repositories.StartGitLabSignInInput{HostID: "instance:" + gcHost})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(start.AuthorizeURL)
	_, err = f.svc.Connect(context.Background(), gcPrincipal("org-a", "user-2"), repositories.ConnectGitLabInput{Code: "code-alice", State: u.Query().Get("state")})
	if !errors.Is(err, repositories.ErrGitLabBadState) {
		t.Fatalf("Connect by another user = %v, want ErrGitLabBadState", err)
	}
}

func TestGitLabConnect_savesGrantInVaultOnly(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	v := f.connect(t, gcPrincipal("org-a", "user-1"), "instance:"+gcHost, "alice")
	rec := f.store.connection(t, v.ID)
	if rec.OrgID != "org-a" || rec.Host != gcHost || rec.HostID != "" || rec.Kind != repositories.GitLabKindOAuth ||
		rec.GitLabUserID != 1 || rec.Username != "alice" || rec.CreatedBy != "user-1" {
		t.Fatalf("record = %+v", rec)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "at-alice") || strings.Contains(string(raw), "rt-alice") {
		t.Fatalf("the record holds a token: %s", raw)
	}
	g := f.secrets.grant(t, v.ID)
	if !strings.HasPrefix(g.AccessToken, "at-alice-") || !strings.HasPrefix(g.RefreshToken, "rt-alice-") || g.Expiry.IsZero() {
		t.Fatalf("grant = %+v", g)
	}
	// The exchange sent the instance's secret and the PKCE verifier.
	form := f.fake.forms[0]
	if form.Get("client_secret") != gcInstanceSecret || form.Get("code_verifier") == "" {
		t.Fatalf("exchange form = %v", form)
	}
	if len(f.audit.actions) != 1 || f.audit.actions[0] != "gitlab.connect" {
		t.Fatalf("audit = %v", f.audit.actions)
	}
	d := f.audit.details[0]
	if d["host"] != gcHost || d["username"] != "alice" || d["connection_id"] != v.ID {
		t.Fatalf("audit details = %v", d)
	}
}

func TestGitLabConnect_stateIsSpentOnce(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	start, err := f.svc.StartSignIn(context.Background(), p, repositories.StartGitLabSignInInput{HostID: "instance:" + gcHost})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(start.AuthorizeURL)
	in := repositories.ConnectGitLabInput{Code: "code-alice", State: u.Query().Get("state")}
	if _, err := f.svc.Connect(context.Background(), p, in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Connect(context.Background(), p, in); !errors.Is(err, repositories.ErrGitLabBadState) {
		t.Fatalf("second Connect = %v, want ErrGitLabBadState", err)
	}
}

func TestGitLabConnect_sameUserAgainKeepsConnection(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	first := f.connect(t, p, "instance:"+gcHost, "alice")
	_ = f.store.MarkLapsed(context.Background(), "org-a", first.ID, time.Now())
	again := f.connect(t, p, "instance:"+gcHost, "alice")
	if again.ID != first.ID {
		t.Fatalf("reconnecting made %s, want %s", again.ID, first.ID)
	}
	if again.Lapsed {
		t.Fatal("reconnecting left the connection lapsed")
	}
	if cs, _ := f.store.ListConnections(context.Background(), "org-a"); len(cs) != 1 {
		t.Fatalf("org-a has %d connections, want 1", len(cs))
	}
	if g := f.secrets.grant(t, first.ID); g.AccessToken != "at-alice-2" {
		t.Fatalf("grant = %+v, want the new sign-in's", g)
	}
}

func TestGitLabConnect_needsConnectPermission(t *testing.T) {
	f := newGCFixture(t, readOnly, true)
	_, err := f.svc.StartSignIn(context.Background(), gcPrincipal("org-a", "user-1"), repositories.StartGitLabSignInInput{HostID: "instance:" + gcHost})
	if !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("StartSignIn without repository:connect = %v", err)
	}
	_, err = f.svc.Connect(context.Background(), gcPrincipal("org-a", "user-1"), repositories.ConnectGitLabInput{Code: "c", State: "s"})
	if !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("Connect without repository:connect = %v", err)
	}
}

func TestGitLabStatus_neverIncludesSecrets(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	h, err := f.svc.AddHost(context.Background(), p, repositories.AddGitLabHostInput{
		WebURL: "https://Git.A.example/", ClientID: "a-app", ClientSecret: gcOrgSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.WebURL != "https://git.a.example" || h.Host != "git.a.example" || h.Instance || h.CreatedAt == nil {
		t.Fatalf("host view = %+v", h)
	}
	v := f.connect(t, p, "instance:"+gcHost, "alice")
	st, err := f.svc.Status(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	g := f.secrets.grant(t, v.ID)
	for _, secret := range []string{gcOrgSecret, gcInstanceSecret, "com-secret", g.AccessToken, g.RefreshToken} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("status holds %q: %s", secret, raw)
		}
	}
	auditRaw, _ := json.Marshal(f.audit.details)
	for _, secret := range []string{gcOrgSecret, gcInstanceSecret, g.AccessToken, g.RefreshToken} {
		if strings.Contains(string(auditRaw), secret) {
			t.Fatalf("audit holds %q: %s", secret, auditRaw)
		}
	}
	if !st.CanConnect || !st.OrganizationHosts || st.CallbackURL != "https://loomplane.example/integrations/gitlab/callback" {
		t.Fatalf("status = %+v", st)
	}
	ids := make([]string, 0, len(st.Hosts))
	for _, h := range st.Hosts {
		ids = append(ids, h.ID)
	}
	if strings.Join(ids, ",") != "instance:"+gcHost+",instance:gitlab.com,"+h.ID {
		t.Fatalf("host IDs = %v", ids)
	}
	if len(st.Connections) != 1 || st.Connections[0].WebhookURL != "https://hooks.example/api/v1/gitlab/webhook/"+v.ID {
		t.Fatalf("connections = %+v", st.Connections)
	}
	// The org host's secret is in Vault, under host/<id>.
	if raw, err := f.secrets.Reveal(context.Background(), "org-a", "host/"+h.ID); err != nil || string(raw) != gcOrgSecret {
		t.Fatalf("host secret = %q, %v", raw, err)
	}
}

func TestGitLabStatus_oldVersionWarns(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	f.fake.version = "16.11.0"
	p := gcPrincipal("org-a", "user-1")
	v := f.connect(t, p, "instance:"+gcHost, "alice")
	if v.Version != "16.11.0" || v.VersionSupported {
		t.Fatalf("connection = %+v, want version 16.11.0 unsupported", v)
	}
	st, err := f.svc.Status(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Connections) != 1 || st.Connections[0].VersionSupported {
		t.Fatalf("status connections = %+v", st.Connections)
	}
}

func TestGitLabStatus_permissions(t *testing.T) {
	f := newGCFixture(t, readOnly, false)
	st, err := f.svc.Status(context.Background(), gcPrincipal("org-a", "user-1"))
	if err != nil {
		t.Fatal(err)
	}
	if st.CanConnect || st.OrganizationHosts {
		t.Fatalf("status = %+v", st)
	}
	f = newGCFixture(t, denyAll, false)
	if _, err := f.svc.Status(context.Background(), gcPrincipal("org-a", "user-1")); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("Status without project:read = %v", err)
	}
}

func TestGitLabToken_onlyThisOrgsConnections(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	f.connect(t, gcPrincipal("org-b", "user-2"), "instance:"+gcHost, "bob")
	tok, err := f.svc.Token(context.Background(), "org-a", gcAPI)
	if err != nil || tok != "" {
		t.Fatalf("Token(org-a) = %q, %v; want no token", tok, err)
	}
	tok, err = f.svc.Token(context.Background(), "org-b", gcAPI)
	if err != nil || !strings.HasPrefix(tok, "at-bob-") {
		t.Fatalf("Token(org-b) = %q, %v", tok, err)
	}
}

func TestGitLabToken_firstConnectionThatSeesTheProject(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	f.connect(t, p, "instance:"+gcHost, "alice")
	f.connect(t, p, "instance:"+gcHost, "bob")
	// Both see grp/sub/api; alice connected first.
	tok, err := f.svc.Token(context.Background(), "org-a", gcAPI)
	if err != nil || !strings.HasPrefix(tok, "at-alice-") {
		t.Fatalf("Token(api) = %q, %v; want alice's", tok, err)
	}
	// Only bob sees grp/secret.
	tok, err = f.svc.Token(context.Background(), "org-a", repositories.Repository{Host: strings.ToUpper(gcHost), Owner: "grp", Name: "secret"})
	if err != nil || !strings.HasPrefix(tok, "at-bob-") {
		t.Fatalf("Token(secret) = %q, %v; want bob's", tok, err)
	}
	// Nobody sees grp/none, so it reads anonymously.
	tok, err = f.svc.Token(context.Background(), "org-a", repositories.Repository{Host: gcHost, Owner: "grp", Name: "none"})
	if err != nil || tok != "" {
		t.Fatalf("Token(none) = %q, %v; want no token", tok, err)
	}
	// Another host's projects aren't asked of these connections.
	tok, err = f.svc.Token(context.Background(), "org-a", repositories.Repository{Host: "gitlab.com", Owner: "grp/sub", Name: "api"})
	if err != nil || tok != "" {
		t.Fatalf("Token(gitlab.com) = %q, %v; want no token", tok, err)
	}
}

func TestGitLabToken_renewsOnceUnderConcurrency(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	f.fake.expiresIn = 60 // due within five minutes as soon as it's issued
	v := f.connect(t, gcPrincipal("org-a", "user-1"), "instance:"+gcHost, "alice")
	f.fake.mu.Lock()
	f.fake.expiresIn = 7200
	f.fake.slow = 50 * time.Millisecond
	f.fake.mu.Unlock()
	var wg sync.WaitGroup
	toks := make([]string, 8)
	errs := make([]error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			toks[i], errs[i] = f.svc.Token(context.Background(), "org-a", gcAPI)
		}()
	}
	wg.Wait()
	if n := f.fake.refreshes.Load(); n != 1 {
		t.Fatalf("GitLab saw %d refreshes, want 1", n)
	}
	g := f.secrets.grant(t, v.ID)
	for i := range 8 {
		if errs[i] != nil || toks[i] != g.AccessToken {
			t.Fatalf("Token %d = %q, %v; want the renewed %q", i, toks[i], errs[i], g.AccessToken)
		}
	}
	if g.RefreshToken != "rt-alice-2" {
		t.Fatalf("kept refresh token %q, want the new one", g.RefreshToken)
	}
}

func TestGitLabToken_lapsedSignIn(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	f.fake.expiresIn = 60
	v := f.connect(t, gcPrincipal("org-a", "user-1"), "instance:"+gcHost, "alice")
	f.fake.mu.Lock()
	f.fake.refuse = true
	f.fake.mu.Unlock()
	_, err := f.svc.Token(context.Background(), "org-a", gcAPI)
	if !errors.Is(err, repositories.ErrGitLabSignInLapsed) {
		t.Fatalf("Token = %v, want ErrGitLabSignInLapsed", err)
	}
	if errors.Is(err, repositories.ErrNotFound) {
		t.Fatal("a lapsed sign-in reads as not found")
	}
	if !strings.Contains(err.Error(), "alice") || !strings.Contains(err.Error(), gcHost) {
		t.Fatalf("error %q doesn't name the account and host", err)
	}
	if strings.Contains(err.Error(), "rt-alice") || strings.Contains(err.Error(), "at-alice") {
		t.Fatalf("error %q holds a token", err)
	}
	if rec := f.store.connection(t, v.ID); rec.LapsedAt == nil {
		t.Fatal("the connection wasn't marked lapsed")
	}
	// A lapsed connection is skipped without asking GitLab again.
	_, err = f.svc.Token(context.Background(), "org-a", gcAPI)
	if !errors.Is(err, repositories.ErrGitLabSignInLapsed) {
		t.Fatalf("second Token = %v, want ErrGitLabSignInLapsed", err)
	}
	if n := f.fake.refreshes.Load(); n != 1 {
		t.Fatalf("GitLab saw %d refreshes, want 1", n)
	}
	st, err := f.svc.Status(context.Background(), gcPrincipal("org-a", "user-1"))
	if err != nil || len(st.Connections) != 1 || !st.Connections[0].Lapsed {
		t.Fatalf("status = %+v, %v; want the connection lapsed", st, err)
	}
	// With a live connection on the host too, the project reads through it.
	f.fake.mu.Lock()
	f.fake.refuse, f.fake.expiresIn = false, 7200
	f.fake.mu.Unlock()
	f.connect(t, gcPrincipal("org-a", "user-1"), "instance:"+gcHost, "bob")
	tok, err := f.svc.Token(context.Background(), "org-a", gcAPI)
	if err != nil || !strings.HasPrefix(tok, "at-bob-") {
		t.Fatalf("Token with bob connected = %q, %v", tok, err)
	}
}

func TestGitLabToken_lapsedBesideALiveConnection(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	// bob is live but can't see grp/secret; alice could, but her sign-in
	// lapsed.
	f.connect(t, p, "instance:"+gcHost, "bob")
	f.fake.mu.Lock()
	f.fake.users["bob"] = gcUser{id: 2, name: "bob", projects: []string{"grp/sub/api"}}
	f.fake.users["alice"] = gcUser{id: 1, name: "alice", projects: []string{"grp/secret", "Grp/a/web"}}
	f.fake.mu.Unlock()
	alice := f.connect(t, p, "instance:"+gcHost, "alice")
	if err := f.store.MarkLapsed(context.Background(), "org-a", alice.ID, time.Now()); err != nil {
		t.Fatal(err)
	}

	// bob sees grp/sub/api, so the lapsed sign-in doesn't matter.
	tok, err := f.svc.Token(context.Background(), "org-a", gcAPI)
	if err != nil || !strings.HasPrefix(tok, "at-bob-") {
		t.Fatalf("Token(api) = %q, %v; want bob's", tok, err)
	}
	// Only alice could see grp/secret, and it isn't public: say so.
	_, err = f.svc.Token(context.Background(), "org-a", repositories.Repository{Host: gcHost, Owner: "grp", Name: "secret"})
	if !errors.Is(err, repositories.ErrGitLabSignInLapsed) || !strings.Contains(err.Error(), "alice") {
		t.Fatalf("Token(secret) = %v, want alice's lapsed sign-in", err)
	}
	// A public project reads anonymously, lapsed sign-in or not.
	tok, err = f.svc.Token(context.Background(), "org-a", repositories.Repository{Host: gcHost, Owner: "Grp/a", Name: "web"})
	if err != nil || tok != "" {
		t.Fatalf("Token(public web) = %q, %v; want no token", tok, err)
	}
}

func TestGitLabToken_allLapsedStillReadsAPublicProject(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	v := f.connect(t, gcPrincipal("org-a", "user-1"), "instance:"+gcHost, "alice")
	if err := f.store.MarkLapsed(context.Background(), "org-a", v.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	tok, err := f.svc.Token(context.Background(), "org-a", repositories.Repository{Host: gcHost, Owner: "Grp/a", Name: "web"})
	if err != nil || tok != "" {
		t.Fatalf("Token(public web) = %q, %v; want no token", tok, err)
	}
	if _, err = f.svc.Token(context.Background(), "org-a", gcAPI); !errors.Is(err, repositories.ErrGitLabSignInLapsed) {
		t.Fatalf("Token(api) = %v, want ErrGitLabSignInLapsed", err)
	}
}

func TestGitLabDisconnect_revokesAndForgets(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	v := f.connect(t, p, "instance:"+gcHost, "alice")
	g := f.secrets.grant(t, v.ID)
	_ = f.secrets.Put(context.Background(), "org-a", v.ID+"/webhook", []byte("hook-secret"))
	// Another organization can't disconnect it.
	if err := f.svc.Disconnect(context.Background(), gcPrincipal("org-b", "user-2"), v.ID); !errors.Is(err, repositories.ErrGitLabConnectionNotFound) {
		t.Fatalf("Disconnect from org-b = %v, want ErrGitLabConnectionNotFound", err)
	}
	if err := f.svc.Disconnect(context.Background(), p, v.ID); err != nil {
		t.Fatal(err)
	}
	// The refresh token, which GitLab revokes even once the access token
	// has expired, as it has on an idle connection.
	if len(f.fake.revoked) != 1 || f.fake.revoked[0] != g.RefreshToken || f.fake.revokeHints[0] != "refresh_token" {
		t.Fatalf("revoked %v with hints %v, want the refresh token as a refresh_token", f.fake.revoked, f.fake.revokeHints)
	}
	if d := f.secrets.dump(); d != "" {
		t.Fatalf("secrets left: %s", d)
	}
	if _, err := f.store.GetConnection(context.Background(), v.ID); !errors.Is(err, repositories.ErrGitLabConnectionNotFound) {
		t.Fatalf("row still there: %v", err)
	}
	last := len(f.audit.actions) - 1
	if f.audit.actions[last] != "gitlab.disconnect" || f.audit.details[last]["connection_id"] != v.ID {
		t.Fatalf("audit = %v %v", f.audit.actions, f.audit.details)
	}
}

func TestGitLabDisconnect_revokeFailureIsAudited(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	v := f.connect(t, p, "instance:"+gcHost, "alice")
	// The grant points at a host GitLab no longer answers for.
	f.secrets.setGrant(t, "org-a", v.ID, oauth.Grant{AccessToken: "at-alice-9", RefreshToken: "rt-alice-9", Expiry: time.Now().Add(time.Hour)})
	srvDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer srvDown.Close()
	svc := repositories.NewGitLabConnections(repositories.GitLabConnectionsConfig{
		Store: f.store, Secrets: f.secrets, Authz: allowAll, Audit: f.audit,
		Registry:        &repositories.GitLabRegistry{Instance: []repositories.GitLabHost{{WebURL: "https://" + gcHost, Host: gcHost, ClientID: "app-id"}}},
		InstanceSecrets: map[string]string{gcHost: gcInstanceSecret},
		Client: func(repositories.GitLabHost) *repositories.GitLab {
			return repositories.NewGitLab(srvDown.URL, srvDown.Client())
		},
	})
	if err := svc.Disconnect(context.Background(), p, v.ID); err != nil {
		t.Fatalf("Disconnect with GitLab down = %v, want nil", err)
	}
	last := len(f.audit.details) - 1
	if _, ok := f.audit.details[last]["revoke_error"]; !ok {
		t.Fatalf("audit details = %v, want the revoke failure", f.audit.details[last])
	}
	raw, _ := json.Marshal(f.audit.details[last])
	if strings.Contains(string(raw), "at-alice-9") || strings.Contains(string(raw), gcInstanceSecret) {
		t.Fatalf("audit holds a secret: %s", raw)
	}
	if d := f.secrets.dump(); d != "" {
		t.Fatalf("secrets left: %s", d)
	}
}

func TestGitLabAddHost_refusedWhenOff(t *testing.T) {
	f := newGCFixture(t, allowAll, false)
	_, err := f.svc.AddHost(context.Background(), gcPrincipal("org-a", "user-1"), repositories.AddGitLabHostInput{
		WebURL: "https://git.a.example", ClientID: "a-app", ClientSecret: gcOrgSecret,
	})
	if !errors.Is(err, repositories.ErrGitLabHostsOff) {
		t.Fatalf("AddHost with organization hosts off = %v, want ErrGitLabHostsOff", err)
	}
	if hs, _ := f.store.ListHosts(context.Background(), "org-a"); len(hs) != 0 {
		t.Fatalf("hosts saved: %v", hs)
	}
}

func TestGitLabAddHost_needsConnectPermission(t *testing.T) {
	f := newGCFixture(t, readOnly, true)
	_, err := f.svc.AddHost(context.Background(), gcPrincipal("org-a", "user-1"), repositories.AddGitLabHostInput{
		WebURL: "https://git.a.example", ClientID: "a-app", ClientSecret: gcOrgSecret,
	})
	if !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("AddHost without repository:connect = %v", err)
	}
	if err := f.svc.RemoveHost(context.Background(), gcPrincipal("org-a", "user-1"), "glh_1"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("RemoveHost without repository:connect = %v", err)
	}
}

func TestGitLabAddHost_validates(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	for _, tc := range []struct {
		in   repositories.AddGitLabHostInput
		want error
	}{
		{repositories.AddGitLabHostInput{WebURL: "http://git.a.example", ClientID: "x", ClientSecret: "y"}, repositories.ErrGitLabInsecure},
		{repositories.AddGitLabHostInput{WebURL: "https://git.a.example/gitlab", ClientID: "x", ClientSecret: "y"}, repositories.ErrGitLabSubpath},
		{repositories.AddGitLabHostInput{WebURL: "https://GITLAB.example.com:443", ClientID: "x", ClientSecret: "y"}, repositories.ErrGitLabHostExists},
		{repositories.AddGitLabHostInput{WebURL: "https://git.a.example", ClientSecret: "y"}, repositories.ErrGitLabHostIncomplete},
		{repositories.AddGitLabHostInput{WebURL: "https://git.a.example", ClientID: "x"}, repositories.ErrGitLabHostIncomplete},
	} {
		if _, err := f.svc.AddHost(context.Background(), p, tc.in); !errors.Is(err, tc.want) {
			t.Errorf("AddHost(%+v) = %v, want %v", tc.in, err, tc.want)
		}
	}
	if _, err := f.svc.AddHost(context.Background(), p, repositories.AddGitLabHostInput{WebURL: "git.a.example", ClientID: "x", ClientSecret: "y"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AddHost(context.Background(), p, repositories.AddGitLabHostInput{WebURL: "https://git.a.example", ClientID: "x", ClientSecret: "y"}); !errors.Is(err, repositories.ErrGitLabHostExists) {
		t.Fatalf("adding the host twice = %v, want ErrGitLabHostExists", err)
	}
}

func TestGitLabRemoveHost_onlyTheCallersOrg(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	a := gcPrincipal("org-a", "user-1")
	h, err := f.svc.AddHost(context.Background(), a, repositories.AddGitLabHostInput{WebURL: "https://git.a.example", ClientID: "a-app", ClientSecret: gcOrgSecret})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RemoveHost(context.Background(), gcPrincipal("org-b", "user-2"), h.ID); !errors.Is(err, repositories.ErrGitLabHostNotFound) {
		t.Fatalf("RemoveHost from org-b = %v, want ErrGitLabHostNotFound", err)
	}
	if hs, _ := f.store.ListHosts(context.Background(), "org-a"); len(hs) != 1 {
		t.Fatal("org-b removed org-a's host")
	}
	if _, err := f.secrets.Reveal(context.Background(), "org-a", "host/"+h.ID); err != nil {
		t.Fatal("org-b removed org-a's host secret")
	}
	if err := f.svc.RemoveHost(context.Background(), a, "instance:"+gcHost); !errors.Is(err, repositories.ErrGitLabHostNotFound) {
		t.Fatalf("removing an instance host = %v, want ErrGitLabHostNotFound", err)
	}
	if err := f.svc.RemoveHost(context.Background(), a, h.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.secrets.Reveal(context.Background(), "org-a", "host/"+h.ID); err == nil {
		t.Fatal("the host's secret outlived it")
	}
	if got := f.audit.actions; got[0] != "gitlab.host.add" || got[len(got)-1] != "gitlab.host.remove" {
		t.Fatalf("audit = %v", got)
	}
}

func TestGitLabRepositories_groupedOrderAndSkipsLapsed(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	alice := f.connect(t, p, "instance:"+gcHost, "alice")
	bob := f.connect(t, p, "instance:"+gcHost, "bob")
	carol := f.connect(t, p, "instance:gitlab.com", "carol")
	_ = f.store.MarkLapsed(context.Background(), "org-a", bob.ID, time.Now())
	repos, err := f.svc.Repositories(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(repos))
	for _, r := range repos {
		got = append(got, r.Host+" "+r.FullName+" "+r.Connection)
	}
	want := []string{
		"gitlab.com abc/x " + carol.ID,
		gcHost + " Grp/a/web " + alice.ID,
		gcHost + " grp/sub/api " + alice.ID,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("repositories:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	r := repos[2]
	if r.Key != gcHost+"/grp/sub/api" || r.Namespace != "grp/sub" || r.Name != "API" || !r.Private || r.DefaultBranch != "main" {
		t.Fatalf("repository = %+v", r)
	}
	if repos[1].Private {
		t.Fatalf("public project %+v reads as private", repos[1])
	}
	// The same project through two live connections shows once.
	_ = f.connect(t, p, "instance:"+gcHost, "bob")
	repos, err = f.svc.Repositories(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, r := range repos {
		seen[strings.ToLower(r.Key)]++
	}
	if seen[gcHost+"/grp/sub/api"] != 1 || seen[gcHost+"/grp/secret"] != 1 {
		t.Fatalf("repositories = %+v", repos)
	}
	// Another organization sees none of them.
	repos, err = f.svc.Repositories(context.Background(), gcPrincipal("org-b", "user-2"))
	if err != nil || len(repos) != 0 {
		t.Fatalf("org-b repositories = %+v, %v", repos, err)
	}
}

func TestGitLabLookup(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	if src, ok := f.svc.Lookup(context.Background(), strings.ToUpper(gcHost)); !ok || src == nil {
		t.Fatal("Lookup of an instance host found no token source")
	}
	if _, ok := f.svc.Lookup(context.Background(), "unknown.example"); ok {
		t.Fatal("Lookup of an unknown host found a token source")
	}
}

func base64URLDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

func TestGitLabConnect_reconnectKeepsWebhookSet(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	first := f.connect(t, p, "instance:"+gcHost, "alice")
	if first.WebhookSet {
		t.Fatal("a new connection has a webhook")
	}
	f.store.setWebhook(first.ID)
	again := f.connect(t, p, "instance:"+gcHost, "alice")
	if again.ID != first.ID || !again.WebhookSet {
		t.Fatalf("reconnected %+v, want %s with its webhook still set", again, first.ID)
	}
}

func TestGitLabConnect_reconnectDuringRefusedRenewalStaysLive(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	f.fake.expiresIn = 60
	first := f.connect(t, p, "instance:"+gcHost, "alice")
	started := make(chan struct{}, 1)
	f.fake.mu.Lock()
	f.fake.refuse, f.fake.slow, f.fake.refreshStarted = true, 300*time.Millisecond, started
	f.fake.expiresIn = 7200
	f.fake.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.Token(context.Background(), "org-a", gcAPI)
		done <- err
	}()
	// The renewal holds the grant lock while GitLab takes its time to
	// refuse it; alice signs in again meanwhile.
	<-started
	again := f.connect(t, p, "instance:"+gcHost, "alice")
	if err := <-done; !errors.Is(err, repositories.ErrGitLabSignInLapsed) {
		t.Fatalf("the racing Token = %v, want ErrGitLabSignInLapsed", err)
	}
	if again.ID != first.ID {
		t.Fatalf("reconnect made %s, want %s", again.ID, first.ID)
	}
	if rec := f.store.connection(t, first.ID); rec.LapsedAt != nil {
		t.Fatal("the refused renewal of the old sign-in left the reconnected connection lapsed")
	}
	if again.Lapsed {
		t.Fatal("Connect answered with a lapsed connection")
	}
	tok, err := f.svc.Token(context.Background(), "org-a", gcAPI)
	if err != nil || tok != f.secrets.grant(t, first.ID).AccessToken {
		t.Fatalf("Token after reconnecting = %q, %v; want the new grant's", tok, err)
	}
}

func TestGitLabToken_refreshFailureIsNotALapse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"GitLab down", http.StatusBadGateway, `{}`},
		{"app rejected", http.StatusUnauthorized, `{"error":"invalid_client"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGCFixture(t, allowAll, true)
			f.fake.expiresIn = 60 // due, but unexpired
			v := f.connect(t, gcPrincipal("org-a", "user-1"), "instance:"+gcHost, "alice")
			held := f.secrets.grant(t, v.ID)
			f.fake.mu.Lock()
			f.fake.refreshStatus, f.fake.refreshBody = tc.status, tc.body
			f.fake.mu.Unlock()
			tok, err := f.svc.Token(context.Background(), "org-a", gcAPI)
			if err != nil || tok != held.AccessToken {
				t.Fatalf("Token = %q, %v; want the held, unexpired %q", tok, err, held.AccessToken)
			}
			if f.fake.refreshes.Load() == 0 {
				t.Fatal("no refresh was tried")
			}
			if rec := f.store.connection(t, v.ID); rec.LapsedAt != nil {
				t.Fatal("a refresh that failed for another reason than a refused grant lapsed the connection")
			}
			if g := f.secrets.grant(t, v.ID); g.AccessToken != held.AccessToken || g.RefreshToken != held.RefreshToken {
				t.Fatalf("grant = %+v, want the held one kept", g)
			}
		})
	}
}

func TestGitLabDisconnect_revokesWhenTheCallerHasGivenUp(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	v := f.connect(t, p, "instance:"+gcHost, "alice")
	g := f.secrets.grant(t, v.ID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.svc.Disconnect(ctx, p, v.ID); err != nil {
		t.Fatal(err)
	}
	f.fake.mu.Lock()
	revoked := append([]string(nil), f.fake.revoked...)
	hints := append([]string(nil), f.fake.revokeHints...)
	f.fake.mu.Unlock()
	if len(revoked) != 1 || revoked[0] != g.RefreshToken || hints[0] != "refresh_token" {
		t.Fatalf("revoked %v with hints %v, want the refresh token even though the caller had gone", revoked, hints)
	}
}

func TestGitLabDisconnect_revokesTheAccessTokenWithoutARefreshToken(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	v := f.connect(t, p, "instance:"+gcHost, "alice")
	f.secrets.setGrant(t, "org-a", v.ID, oauth.Grant{AccessToken: "at-alice-7", Expiry: time.Now().Add(time.Hour)})
	if err := f.svc.Disconnect(context.Background(), p, v.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.fake.revoked) != 1 || f.fake.revoked[0] != "at-alice-7" || f.fake.revokeHints[0] != "access_token" {
		t.Fatalf("revoked %v with hints %v, want the access token as an access_token", f.fake.revoked, f.fake.revokeHints)
	}
}

func TestGitLabConnect_reconnectRevokesTheReplacedGrant(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	first := f.connect(t, p, "instance:"+gcHost, "alice")
	old := f.secrets.grant(t, first.ID)
	if len(f.fake.revoked) != 0 {
		t.Fatalf("a first sign-in revoked %v", f.fake.revoked)
	}
	again := f.connect(t, p, "instance:"+gcHost, "alice")
	if again.ID != first.ID {
		t.Fatalf("reconnect made %s, want %s", again.ID, first.ID)
	}
	if len(f.fake.revoked) != 1 || f.fake.revoked[0] != old.RefreshToken || f.fake.revokeHints[0] != "refresh_token" {
		t.Fatalf("revoked %v with hints %v, want the replaced refresh token %s", f.fake.revoked, f.fake.revokeHints, old.RefreshToken)
	}
	if g := f.secrets.grant(t, first.ID); g.RefreshToken == old.RefreshToken {
		t.Fatal("the reconnect kept the old grant")
	}
	last := len(f.audit.actions) - 1
	if f.audit.actions[last] != "gitlab.connect" {
		t.Fatalf("audit = %v", f.audit.actions)
	}
	if _, ok := f.audit.details[last]["revoke_error"]; ok {
		t.Fatalf("audit details = %v, want no revoke failure", f.audit.details[last])
	}
}

func TestGitLabConnect_reconnectDoesntWaitForTheRevoke(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	first := f.connect(t, p, "instance:"+gcHost, "alice")
	old := f.secrets.grant(t, first.ID)
	hold := make(chan struct{})
	f.fake.mu.Lock()
	f.fake.revokeHold = hold
	f.fake.mu.Unlock()
	start, err := f.svc.StartSignIn(context.Background(), p, repositories.StartGitLabSignInInput{HostID: "instance:" + gcHost})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(start.AuthorizeURL)
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.Connect(context.Background(), p, repositories.ConnectGitLabInput{Code: "code-alice", State: u.Query().Get("state")})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		close(hold)
		t.Fatal("Connect waited for GitLab to answer the revoke")
	}
	// The new grant is in use before GitLab has answered.
	if g := f.secrets.grant(t, first.ID); g.RefreshToken == old.RefreshToken {
		t.Fatal("the reconnect kept the old grant")
	}
	close(hold)
	repositories.WaitGitLabBackground(f.svc)
	f.fake.mu.Lock()
	revoked := slices.Clone(f.fake.revoked)
	f.fake.mu.Unlock()
	if len(revoked) != 1 || revoked[0] != old.RefreshToken {
		t.Fatalf("revoked %v, want the replaced refresh token", revoked)
	}
}

func TestGitLabConnect_reconnectRevokeFailureIsAudited(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	first := f.connect(t, p, "instance:"+gcHost, "alice")
	old := f.secrets.grant(t, first.ID)
	f.fake.mu.Lock()
	f.fake.revokeStatus = http.StatusInternalServerError
	f.fake.mu.Unlock()
	// The new sign-in is kept whatever GitLab says to the revoke.
	again := f.connect(t, p, "instance:"+gcHost, "alice")
	if again.Lapsed {
		t.Fatal("a failed revoke of the old grant lapsed the new sign-in")
	}
	if g := f.secrets.grant(t, first.ID); g.RefreshToken == old.RefreshToken {
		t.Fatal("the reconnect kept the old grant")
	}
	last := len(f.audit.details) - 1
	if f.audit.actions[last-1] != "gitlab.connect" || f.audit.actions[last] != "gitlab.revoke.failed" {
		t.Fatalf("audit = %v", f.audit.actions)
	}
	if d := f.audit.details[last]; d["revoke_error"] == nil || d["connection_id"] != first.ID || d["username"] != "alice" {
		t.Fatalf("audit details = %v, want the revoke failure for alice's connection", d)
	}
	raw, _ := json.Marshal(f.audit.details[last])
	if strings.Contains(string(raw), old.RefreshToken) || strings.Contains(string(raw), old.AccessToken) {
		t.Fatalf("audit holds a token: %s", raw)
	}
}

func TestGitLabRepositories_skipsAConnectionWhoseListingFails(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	alice := f.connect(t, p, "instance:"+gcHost, "alice")
	carol := f.connect(t, p, "instance:gitlab.com", "carol")
	aliceGrant := f.secrets.grant(t, alice.ID)
	// GitLab revoked alice's token early, so it answers her listing 401.
	f.fake.mu.Lock()
	f.fake.failList = map[string]bool{"alice": true}
	f.fake.mu.Unlock()
	repos, err := f.svc.Repositories(context.Background(), p)
	if err != nil {
		t.Fatalf("Repositories with one bad connection = %v, want the others' projects", err)
	}
	if len(repos) != 1 || repos[0].FullName != "abc/x" || repos[0].Connection != carol.ID {
		t.Fatalf("repositories = %+v, want carol's only", repos)
	}
	last := len(f.audit.actions) - 1
	if f.audit.actions[last] != repositories.AuditGitLabListFailed || f.audit.details[last]["connection_id"] != alice.ID {
		t.Fatalf("audit = %v %v, want alice's listing failure", f.audit.actions, f.audit.details)
	}
	if _, ok := f.audit.details[last]["list_error"]; !ok {
		t.Fatalf("audit details = %v, want the error", f.audit.details[last])
	}
	raw, _ := json.Marshal(f.audit.details[last])
	if strings.Contains(string(raw), aliceGrant.AccessToken) || strings.Contains(string(raw), aliceGrant.RefreshToken) {
		t.Fatalf("audit holds a token: %s", raw)
	}

	// When every connection fails, the last error is returned.
	f.fake.mu.Lock()
	f.fake.failList = map[string]bool{"alice": true, "carol": true}
	f.fake.mu.Unlock()
	if repos, err := f.svc.Repositories(context.Background(), p); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("Repositories with every connection failing = %+v, %v; want ErrNotFound", repos, err)
	}
}

func TestGitLabRemoveHost_vaultFailureKeepsTheHost(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	h, err := f.svc.AddHost(context.Background(), p, repositories.AddGitLabHostInput{WebURL: "https://git.a.example", ClientID: "a-app", ClientSecret: gcOrgSecret})
	if err != nil {
		t.Fatal(err)
	}
	failing := &failingDeletes{memSecrets: f.secrets, err: errors.New("vault is down")}
	svc := repositories.NewGitLabConnections(repositories.GitLabConnectionsConfig{
		Store: f.store, Secrets: failing, Authz: allowAll, Audit: f.audit,
		Registry: &repositories.GitLabRegistry{Store: f.store, AllowOrganizationHosts: true},
	})
	if err := svc.RemoveHost(context.Background(), p, h.ID); err == nil {
		t.Fatal("RemoveHost with Vault down = nil, want its error")
	}
	// The host is still there to remove again, with its secret.
	if hs, _ := f.store.ListHosts(context.Background(), "org-a"); len(hs) != 1 {
		t.Fatalf("hosts = %v, want the host kept", hs)
	}
	if _, err := f.secrets.Reveal(context.Background(), "org-a", "host/"+h.ID); err != nil {
		t.Fatalf("the host's secret: %v", err)
	}
	failing.err = nil
	if err := svc.RemoveHost(context.Background(), p, h.ID); err != nil {
		t.Fatal(err)
	}
	if hs, _ := f.store.ListHosts(context.Background(), "org-a"); len(hs) != 0 {
		t.Fatalf("hosts = %v, want none", hs)
	}
	if _, err := f.secrets.Reveal(context.Background(), "org-a", "host/"+h.ID); !errors.Is(err, repositories.ErrSecretNotFound) {
		t.Fatalf("the host's secret after removing it: %v", err)
	}
}

// flakyReveals fails every Reveal with err while it's set.
type flakyReveals struct {
	*memSecrets
	err error
}

func (f *flakyReveals) Reveal(ctx context.Context, orgID, id string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.memSecrets.Reveal(ctx, orgID, id)
}

func TestGitLabRemoveHost_unreadableSecretKeepsTheHost(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	h, err := f.svc.AddHost(context.Background(), p, repositories.AddGitLabHostInput{WebURL: "https://git.a.example", ClientID: "a-app", ClientSecret: gcOrgSecret})
	if err != nil {
		t.Fatal(err)
	}
	flaky := &flakyReveals{memSecrets: f.secrets, err: errors.New("vault timed out")}
	svc := repositories.NewGitLabConnections(repositories.GitLabConnectionsConfig{
		Store: f.store, Secrets: flaky, Authz: allowAll, Audit: f.audit,
		Registry: &repositories.GitLabRegistry{Store: f.store, AllowOrganizationHosts: true},
	})
	if err := svc.RemoveHost(context.Background(), p, h.ID); err == nil || !strings.Contains(err.Error(), "vault timed out") {
		t.Fatalf("RemoveHost with Reveal failing = %v, want its error", err)
	}
	// Nothing was deleted: not the row, and not the secret.
	if hs, _ := f.store.ListHosts(context.Background(), "org-a"); len(hs) != 1 {
		t.Fatalf("hosts = %v, want the host kept", hs)
	}
	if raw, err := f.secrets.Reveal(context.Background(), "org-a", "host/"+h.ID); err != nil || string(raw) != gcOrgSecret {
		t.Fatalf("the host's secret = %q, %v; want it kept", raw, err)
	}
	// A host whose secret is already gone still removes.
	flaky.err = nil
	if err := f.secrets.Delete(context.Background(), "org-a", "host/"+h.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveHost(context.Background(), p, h.ID); err != nil {
		t.Fatalf("RemoveHost with no secret = %v", err)
	}
}

func TestGitLabRemoveHost_inUseKeepsItsSecret(t *testing.T) {
	f := newGCFixture(t, allowAll, true)
	p := gcPrincipal("org-a", "user-1")
	h, err := f.svc.AddHost(context.Background(), p, repositories.AddGitLabHostInput{WebURL: "https://git.a.example", ClientID: "a-app", ClientSecret: gcOrgSecret})
	if err != nil {
		t.Fatal(err)
	}
	f.connect(t, p, h.ID, "alice")
	if err := f.svc.RemoveHost(context.Background(), p, h.ID); !errors.Is(err, repositories.ErrGitLabHostInUse) {
		t.Fatalf("RemoveHost of a host in use = %v, want ErrGitLabHostInUse", err)
	}
	if hs, _ := f.store.ListHosts(context.Background(), "org-a"); len(hs) != 1 {
		t.Fatalf("hosts = %v, want the host kept", hs)
	}
	if _, err := f.secrets.Reveal(context.Background(), "org-a", "host/"+h.ID); err != nil {
		t.Fatalf("a refused remove lost the host's secret: %v", err)
	}
}

// failingDeletes is memSecrets whose Delete fails with err while it is set.
type failingDeletes struct {
	*memSecrets
	err error
}

func (s *failingDeletes) Delete(ctx context.Context, orgID, id string) error {
	if s.err != nil {
		return s.err
	}
	return s.memSecrets.Delete(ctx, orgID, id)
}
