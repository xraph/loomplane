package discovery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

// memHooks keeps push webhooks the way the database does: one per
// organization and repository, whatever its case.
type memHooks struct {
	mu    sync.Mutex
	hooks map[string]*repositories.BitbucketHook
	saves int
}

func newMemHooks() *memHooks { return &memHooks{hooks: map[string]*repositories.BitbucketHook{}} }

func (m *memHooks) SaveBitbucketHook(_ context.Context, h *repositories.BitbucketHook) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.hooks {
		if o.OrgID == h.OrgID && strings.EqualFold(o.Repository, h.Repository) {
			return repositories.ErrHookExists
		}
	}
	m.saves++
	c := *h
	m.hooks[h.HookID] = &c
	return nil
}

func (m *memHooks) GetBitbucketHook(_ context.Context, id string) (*repositories.BitbucketHook, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hooks[id]
	if !ok {
		return nil, repositories.ErrHookNotFound
	}
	c := *h
	return &c, nil
}

func (m *memHooks) UpdateBitbucketHook(_ context.Context, h *repositories.BitbucketHook) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.hooks[h.HookID]
	if !ok {
		return repositories.ErrHookNotFound
	}
	o.ConnectionID, o.BitbucketHookUUID, o.Status, o.Reason, o.UpdatedAt = h.ConnectionID, h.BitbucketHookUUID, h.Status, h.Reason, h.UpdatedAt
	return nil
}

func (m *memHooks) BitbucketHookFor(_ context.Context, orgID, repository string) (*repositories.BitbucketHook, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range m.hooks {
		if h.OrgID == orgID && strings.EqualFold(h.Repository, repository) {
			c := *h
			return &c, nil
		}
	}
	return nil, repositories.ErrHookNotFound
}

func (m *memHooks) BitbucketHooksIn(_ context.Context, orgID, connectionID string) ([]*repositories.BitbucketHook, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*repositories.BitbucketHook{}
	for _, h := range m.hooks {
		if h.OrgID == orgID && h.ConnectionID == connectionID {
			c := *h
			out = append(out, &c)
		}
	}
	return out, nil
}

func (m *memHooks) DeleteBitbucketHook(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.hooks[id]; !ok {
		return repositories.ErrHookNotFound
	}
	delete(m.hooks, id)
	return nil
}

func (m *memHooks) all() []*repositories.BitbucketHook {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*repositories.BitbucketHook, 0, len(m.hooks))
	for _, h := range m.hooks {
		c := *h
		out = append(out, &c)
	}
	return out
}

// fakeHookConns is each organization's connection to the acme workspace.
type fakeHookConns struct {
	conns map[string]*repositories.BitbucketConnection // by org
	token map[string]string                            // by org
	asked []string
}

func (f *fakeHookConns) ConnectionFor(_ context.Context, orgID, workspace string) (*repositories.BitbucketConnection, string, error) {
	f.asked = append(f.asked, orgID+"/"+workspace)
	c, ok := f.conns[orgID]
	if !ok || !strings.EqualFold(c.WorkspaceSlug, workspace) {
		return nil, "", repositories.ErrWorkspaceNotFound
	}
	return c, f.token[orgID], nil
}

type createdHook struct{ token, repo, url, secret, uuid string }

// fakeHookAPI plays Bitbucket's webhook endpoints.
type fakeHookAPI struct {
	created   []createdHook
	deleted   []string // token/uuid
	createErr error
	deleteErr error
}

func (f *fakeHookAPI) CreateHook(_ context.Context, token string, repo repositories.Repository, url, secret string) (string, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	uuid := fmt.Sprintf("{hook-%d}", len(f.created)+1)
	f.created = append(f.created, createdHook{token: token, repo: repo.FullName(), url: url, secret: secret, uuid: uuid})
	return uuid, nil
}

func (f *fakeHookAPI) DeleteHook(_ context.Context, token string, _ repositories.Repository, uuid string) error {
	f.deleted = append(f.deleted, token+"/"+uuid)
	return f.deleteErr
}

type hookAudit struct{ events []string }

func (a *hookAudit) RecordHook(_ context.Context, action, orgID, repository, hookID string) {
	a.events = append(a.events, action+" "+orgID+" "+repository+" "+hookID)
}

type hookFixture struct {
	w       *BitbucketWebhooks
	hooks   *memHooks
	conns   *fakeHookConns
	api     *fakeHookAPI
	audit   *hookAudit
	sources []*Source
	started []string
	def     string // the repository's default branch
	defs    []string
	warned  []string
}

func newHookFixture(t *testing.T) *hookFixture {
	t.Helper()
	f := &hookFixture{
		hooks: newMemHooks(),
		conns: &fakeHookConns{
			conns: map[string]*repositories.BitbucketConnection{
				"org_a": {ID: "bbws_a", OrgID: "org_a", WorkspaceSlug: "acme", Status: repositories.BitbucketReady},
				"org_b": {ID: "bbws_b", OrgID: "org_b", WorkspaceSlug: "acme", Status: repositories.BitbucketReady},
			},
			token: map[string]string{"org_a": "token-a", "org_b": "token-b"},
		},
		api:   &fakeHookAPI{},
		audit: &hookAudit{},
		def:   "trunk",
	}
	f.w = &BitbucketWebhooks{
		Key:       []byte("0123456789abcdef0123456789abcdef"),
		PublicURL: "https://loomplane.example.com/",
		Hooks:     f.hooks,
		Conns:     f.conns,
		API:       f.api,
		Audit:     f.audit,
		Sources: func(_ context.Context, repository string) ([]*Source, error) {
			out := []*Source{}
			for _, s := range f.sources {
				if s.RemovedAt == nil && strings.EqualFold(s.Repository, repository) {
					out = append(out, s)
				}
			}
			return out, nil
		},
		Start: func(_ context.Context, s *Source) (*Analysis, error) {
			f.started = append(f.started, s.ID)
			return &Analysis{ID: "ana_" + s.ID}, nil
		},
		DefaultBranch: func(_ context.Context, repo repositories.Repository, token string) (string, error) {
			f.defs = append(f.defs, repo.FullName()+"@"+token)
			return f.def, nil
		},
		Warn: func(msg string, err error) { f.warned = append(f.warned, msg+": "+err.Error()) },
	}
	return f
}

var acmeAPI = repositories.Repository{Host: repositories.HostBitbucket, Owner: "acme", Name: "api"}

func (f *hookFixture) add(id, orgID, repository, ref string) *Source {
	s := &Source{ID: id, OrgID: orgID, ProjectID: "proj_" + id, Alias: "api", Repository: repository, Ref: ref}
	f.sources = append(f.sources, s)
	f.w.SourceHooks().Added(context.Background(), orgID, s)
	return s
}

func (f *hookFixture) remove(s *Source) {
	now := time.Now()
	s.RemovedAt = &now
	f.w.SourceHooks().Removed(context.Background(), s.OrgID, s)
}

func signBitbucket(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func TestBitbucketWebhooks_ensureOncePerOrgAndRepository(t *testing.T) {
	ctx := context.Background()
	f := newHookFixture(t)
	f.add("src_1", "org_a", "bitbucket.org/acme/api", "")
	f.add("src_2", "org_a", "bitbucket.org/Acme/API", "")

	if len(f.api.created) != 1 {
		t.Fatalf("created %d hooks, want 1: %+v", len(f.api.created), f.api.created)
	}
	rows := f.hooks.all()
	if len(rows) != 1 {
		t.Fatalf("rows %+v", rows)
	}
	h, c := rows[0], f.api.created[0]
	if h.Status != repositories.HookOn || h.BitbucketHookUUID != c.uuid || h.ConnectionID != "bbws_a" || h.OrgID != "org_a" || h.Repository != "bitbucket.org/acme/api" {
		t.Fatalf("row %+v", h)
	}
	if c.url != "https://loomplane.example.com/api/v1/bitbucket/webhook/"+h.HookID || c.secret != f.w.Secret(h.HookID) || c.token != "token-a" || c.repo != "acme/api" {
		t.Fatalf("created %+v", c)
	}
	// The secret is HMAC-SHA256 of the hook ID under the key, in hex.
	m := hmac.New(sha256.New, f.w.Key)
	m.Write([]byte(h.HookID))
	if c.secret != hex.EncodeToString(m.Sum(nil)) {
		t.Fatalf("secret %q isn't the hook ID's HMAC", c.secret)
	}
	if !slices.Equal(f.audit.events, []string{"bitbucket.webhook.add org_a bitbucket.org/acme/api " + h.HookID}) {
		t.Fatalf("audit %v", f.audit.events)
	}
	for _, e := range f.audit.events {
		if strings.Contains(e, c.secret) || strings.Contains(e, "token-a") {
			t.Fatalf("the audit holds a secret: %s", e)
		}
	}
	// Another organization on the same repository gets its own hook.
	f.add("src_3", "org_b", "bitbucket.org/acme/api", "")
	if len(f.api.created) != 2 || f.api.created[1].token != "token-b" {
		t.Fatalf("org_b's hook %+v", f.api.created)
	}
	if st := f.w.State(ctx, "org_a", f.sources[0]); st == nil || !st.On {
		t.Fatalf("state %+v", st)
	}
	// GitHub sources have no say.
	if st := f.w.State(ctx, "org_a", &Source{Repository: "acme/api"}); st != nil {
		t.Fatalf("a GitHub source's state %+v", st)
	}
	f.w.SourceHooks().Added(ctx, "org_a", &Source{ID: "src_gh", OrgID: "org_a", Repository: "acme/api"})
	if len(f.api.created) != 2 {
		t.Fatal("a GitHub source added a Bitbucket hook")
	}
}

func TestBitbucketWebhooks_ensureWithoutAdmin(t *testing.T) {
	ctx := context.Background()
	f := newHookFixture(t)
	f.api.createErr = fmt.Errorf("add: %w", repositories.ErrHookNotAdmin)
	got := f.w.Ensure(ctx, "org_a", acmeAPI)
	if got.On || got.Reason != repositories.ErrHookNotAdmin.Error() {
		t.Fatalf("state %+v", got)
	}
	rows := f.hooks.all()
	if len(rows) != 1 || rows[0].Status != repositories.HookOff || rows[0].Reason != got.Reason || rows[0].ConnectionID != "bbws_a" {
		t.Fatalf("rows %+v", rows)
	}
	id := rows[0].HookID
	// An off row is no delivery address.
	if _, err := f.w.Deliver(ctx, id, "repo:push", signBitbucket(f.w.Secret(id), []byte(`{}`)), []byte(`{}`)); !errors.Is(err, repositories.ErrHookNotFound) {
		t.Fatalf("delivery to an off hook: %v", err)
	}
	// An admin turns it on: the same row, now on.
	f.api.createErr = nil
	if got = f.w.Ensure(ctx, "org_a", acmeAPI); !got.On {
		t.Fatalf("turning it on: %+v", got)
	}
	rows = f.hooks.all()
	if len(rows) != 1 || rows[0].HookID != id || rows[0].Status != repositories.HookOn || rows[0].Reason != "" || f.hooks.saves != 1 {
		t.Fatalf("rows after turning on %+v (saves %d)", rows, f.hooks.saves)
	}
	if f.api.created[0].url != "https://loomplane.example.com/api/v1/bitbucket/webhook/"+id {
		t.Fatalf("url %s", f.api.created[0].url)
	}
	// Once on, it isn't added again.
	f.w.Ensure(ctx, "org_a", acmeAPI)
	if len(f.api.created) != 1 {
		t.Fatalf("created %d", len(f.api.created))
	}
}

func TestBitbucketWebhooks_ensureWithoutAdminStillAttaches(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)
	h := newHookFixture(t)
	h.api.createErr = repositories.ErrHookNotAdmin
	h.conns.conns[org] = &repositories.BitbucketConnection{ID: "bbws_o", OrgID: org, WorkspaceSlug: "acme", Status: repositories.BitbucketReady}
	h.conns.token[org] = "token-o"
	h.w.Sources = f.store.byRepository
	f.svc.SetSourceHooks(h.w.SourceHooks())

	src, err := f.svc.AddSource(ctx, dev, "proj_bare", AddSourceInput{Repository: "https://bitbucket.org/acme/api"})
	if err != nil {
		t.Fatalf("the attach failed: %v", err)
	}
	if src.Repository != "bitbucket.org/acme/api" {
		t.Fatalf("source %+v", src)
	}
	list, err := f.svc.Sources(ctx, dev, "proj_bare")
	if err != nil || len(list) != 1 {
		t.Fatalf("Sources: %v %+v", err, list)
	}
	if list[0].Push == nil || list[0].Push.On || list[0].Push.Reason != repositories.ErrHookNotAdmin.Error() {
		t.Fatalf("push %+v", list[0].Push)
	}
	// A GitHub source's listing carries no push state.
	if _, err = f.svc.AddSource(ctx, dev, "proj_bare", AddSourceInput{Repository: "acme/site", Alias: "web"}); err != nil {
		t.Fatal(err)
	}
	list, _ = f.svc.Sources(ctx, dev, "proj_bare")
	for _, s := range list {
		if (s.Alias == "web") != (s.Push == nil) {
			t.Fatalf("%s push %+v", s.Alias, s.Push)
		}
	}
	// Removing the only Bitbucket source releases its row.
	if err = f.svc.RemoveSource(ctx, dev, "proj_bare", "api"); err != nil {
		t.Fatal(err)
	}
	if rows := h.hooks.all(); len(rows) != 0 {
		t.Fatalf("rows after removing %+v", rows)
	}
}

func TestBitbucketWebhooks_ensureOnLocalPublicURL(t *testing.T) {
	ctx := context.Background()
	f := newHookFixture(t)
	f.w.PublicURL = "http://localhost:8780"
	got := f.w.Ensure(ctx, "org_a", acmeAPI)
	if got.On || !strings.Contains(got.Reason, "needs an https public_url") {
		t.Fatalf("state %+v", got)
	}
	if len(f.api.created) != 0 || len(f.conns.asked) != 0 {
		t.Fatalf("Bitbucket was asked: created %v, connections %v", f.api.created, f.conns.asked)
	}
	if rows := f.hooks.all(); len(rows) != 1 || rows[0].Status != repositories.HookOff {
		t.Fatalf("rows %+v", rows)
	}
}

func TestBitbucketWebhooks_ensureWithoutAConnection(t *testing.T) {
	ctx := context.Background()
	f := newHookFixture(t)
	got := f.w.Ensure(ctx, "org_c", acmeAPI)
	if got.On || got.Reason == "" || len(f.api.created) != 0 {
		t.Fatalf("state %+v, created %v", got, f.api.created)
	}
	// One that needs connecting again has no token to add with.
	f.conns.token["org_a"] = ""
	if got = f.w.Ensure(ctx, "org_a", acmeAPI); got.On || got.Reason == "" || len(f.api.created) != 0 {
		t.Fatalf("state %+v", got)
	}
}

func TestBitbucketWebhooks_verify(t *testing.T) {
	f := newHookFixture(t)
	body := []byte(`{"push":{}}`)
	good := signBitbucket(f.w.Secret("bbhook_1"), body)
	if err := f.w.Verify("bbhook_1", good, body); err != nil {
		t.Fatalf("the right signature: %v", err)
	}
	sha1 := "sha1=" + strings.TrimPrefix(good, "sha256=")
	for name, header := range map[string]string{
		"missing":         "",
		"sha1":            sha1,
		"no prefix":       strings.TrimPrefix(good, "sha256="),
		"wrong digest":    signBitbucket("another secret", body),
		"another hook's":  signBitbucket(f.w.Secret("bbhook_2"), body),
		"not hex":         "sha256=zz",
		"truncated":       good[:len(good)-2],
		"empty after =":   "sha256=",
		"the raw secret":  "sha256=" + f.w.Secret("bbhook_1"),
		"other body's":    signBitbucket(f.w.Secret("bbhook_1"), []byte(`{}`)),
		"upper-case algo": "SHA256=" + strings.TrimPrefix(good, "sha256="),
	} {
		if err := f.w.Verify("bbhook_1", header, body); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("%s: %v, want ErrBadSignature", name, err)
		}
	}
	// No key, no deliveries.
	f.w.Key = nil
	if err := f.w.Verify("bbhook_1", good, body); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("without a key: %v", err)
	}
}

func pushPayload(fullName string, changes ...string) []byte {
	return fmt.Appendf(nil, `{"repository":{"full_name":%q,"uuid":"{repo-uuid}"},"push":{"changes":[%s]}}`, fullName, strings.Join(changes, ","))
}

func branchChange(kind, name string) string {
	return fmt.Sprintf(`{"new":{"type":%q,"name":%q,"target":{"hash":"0123456789abcdef0123456789abcdef01234567"}},"old":null}`, kind, name)
}

const deletedBranch = `{"new":null,"old":{"type":"branch","name":"trunk","target":{"hash":"0123456789abcdef0123456789abcdef01234567"}}}`

func TestBitbucketWebhooks_handle(t *testing.T) {
	ctx := context.Background()
	f := newHookFixture(t)
	f.add("src_a", "org_a", "bitbucket.org/acme/api", "")
	f.add("src_a_trunk", "org_a", "bitbucket.org/acme/api", "trunk")
	f.add("src_a_main", "org_a", "bitbucket.org/acme/api", "main")
	f.add("src_b", "org_b", "bitbucket.org/acme/api", "")
	f.add("src_other", "org_a", "bitbucket.org/acme/web", "")
	hookA, err := f.hooks.BitbucketHookFor(ctx, "org_a", "bitbucket.org/acme/api")
	if err != nil {
		t.Fatal(err)
	}
	deliver := func(event string, body []byte) (int, error) {
		f.started, f.defs = nil, nil
		return f.w.Deliver(ctx, hookA.HookID, event, signBitbucket(f.w.Secret(hookA.HookID), body), body)
	}

	// A push to the default branch, trunk, re-analyzes org_a's sources that
	// track it, and never org_b's on the same repository.
	n, err := deliver("repo:push", pushPayload("Acme/API", branchChange("branch", "trunk")))
	if err != nil || n != 2 {
		t.Fatalf("push to trunk: %d, %v", n, err)
	}
	slices.Sort(f.started)
	if !slices.Equal(f.started, []string{"src_a", "src_a_trunk"}) {
		t.Fatalf("started %v", f.started)
	}
	if !slices.Equal(f.defs, []string{"acme/api@token-a"}) {
		t.Fatalf("default branch looked up %v", f.defs)
	}

	// A push to main, which isn't the default, re-analyzes nothing: a source
	// tracking a branch other than the default isn't re-analyzed on push.
	if n, err = deliver("repo:push", pushPayload("acme/api", branchChange("branch", "main"))); err != nil || n != 0 || len(f.started) != 0 {
		t.Fatalf("push to main: %d, %v, started %v", n, err, f.started)
	}
	// With the default renamed to main, it's the other way round.
	f.def = "main"
	if n, err = deliver("repo:push", pushPayload("acme/api", branchChange("branch", "main"))); err != nil || n != 2 {
		t.Fatalf("push to main as default: %d, %v", n, err)
	}
	slices.Sort(f.started)
	if !slices.Equal(f.started, []string{"src_a", "src_a_main"}) {
		t.Fatalf("started %v", f.started)
	}
	f.def = "trunk"

	// Several changes in one push look the default branch up once.
	if n, err = deliver("repo:push", pushPayload("acme/api", branchChange("branch", "feature"), branchChange("branch", "trunk"), branchChange("branch", "trunk"))); err != nil || n != 2 || len(f.defs) != 1 {
		t.Fatalf("several changes: %d, %v, looked up %v", n, err, f.defs)
	}

	for name, body := range map[string][]byte{
		"a tag":            pushPayload("acme/api", branchChange("tag", "trunk")),
		"a deleted branch": pushPayload("acme/api", deletedBranch),
		"no changes":       pushPayload("acme/api"),
		"another repo":     pushPayload("acme/web", branchChange("branch", "trunk")),
	} {
		if n, err = deliver("repo:push", body); err != nil || n != 0 || len(f.started) != 0 {
			t.Fatalf("%s: %d, %v, started %v", name, n, err, f.started)
		}
		if len(f.defs) != 0 {
			t.Fatalf("%s looked the default branch up", name)
		}
	}
	for _, event := range []string{"repo:refs_changed", "pullrequest:created", ""} {
		if n, err = deliver(event, pushPayload("acme/api", branchChange("branch", "trunk"))); err != nil || n != 0 || len(f.started) != 0 {
			t.Fatalf("%q: %d, %v", event, n, err)
		}
	}
	// An unknown hook is 404 whatever it's signed with; a bad signature 401.
	if _, err = f.w.Deliver(ctx, "bbhook_nope", "repo:push", signBitbucket(f.w.Secret("bbhook_nope"), []byte(`{}`)), []byte(`{}`)); !errors.Is(err, repositories.ErrHookNotFound) {
		t.Fatalf("unknown hook: %v", err)
	}
	hookB, _ := f.hooks.BitbucketHookFor(ctx, "org_b", "bitbucket.org/acme/api")
	body := pushPayload("acme/api", branchChange("branch", "trunk"))
	if _, err = f.w.Deliver(ctx, hookA.HookID, "repo:push", signBitbucket(f.w.Secret(hookB.HookID), body), body); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("org_b's secret on org_a's hook: %v", err)
	}
	// A signed body that isn't JSON is the sender's mistake.
	if _, err = deliver("repo:push", []byte(`not json`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("not JSON: %v", err)
	}
}

func TestBitbucketWebhooks_releaseOnLastSource(t *testing.T) {
	f := newHookFixture(t)
	one := f.add("src_1", "org_a", "bitbucket.org/acme/api", "")
	two := f.add("src_2", "org_a", "bitbucket.org/Acme/Api", "")
	other := f.add("src_3", "org_b", "bitbucket.org/acme/api", "")
	f.remove(one)
	if len(f.api.deleted) != 0 || len(f.hooks.all()) != 2 {
		t.Fatalf("removing one of two: deleted %v, rows %d", f.api.deleted, len(f.hooks.all()))
	}
	hook, _ := f.hooks.BitbucketHookFor(context.Background(), "org_a", "bitbucket.org/acme/api")
	f.remove(two)
	if !slices.Equal(f.api.deleted, []string{"token-a/" + hook.BitbucketHookUUID}) {
		t.Fatalf("deleted %v", f.api.deleted)
	}
	rows := f.hooks.all()
	if len(rows) != 1 || rows[0].OrgID != "org_b" {
		t.Fatalf("rows %+v", rows)
	}
	if f.audit.events[len(f.audit.events)-1] != "bitbucket.webhook.remove org_a bitbucket.org/acme/api "+hook.HookID {
		t.Fatalf("audit %v", f.audit.events)
	}
	_ = other
}

func TestBitbucketWebhooks_releaseAllWhenBitbucketRefuses(t *testing.T) {
	f := newHookFixture(t)
	f.add("src_1", "org_a", "bitbucket.org/acme/api", "")
	f.add("src_2", "org_a", "bitbucket.org/acme/web", "")
	f.add("src_3", "org_b", "bitbucket.org/acme/api", "")
	f.api.deleteErr = repositories.ErrHookNotAdmin
	f.w.ReleaseAll(context.Background(), "org_a", "bbws_a")
	if len(f.api.deleted) != 2 {
		t.Fatalf("deleted %v", f.api.deleted)
	}
	rows := f.hooks.all()
	if len(rows) != 1 || rows[0].OrgID != "org_b" {
		t.Fatalf("rows left %+v", rows)
	}
	if len(f.warned) != 2 {
		t.Fatalf("warned %v", f.warned)
	}
	for _, w := range f.warned {
		if strings.Contains(w, "token-a") {
			t.Fatalf("a warning holds the token: %s", w)
		}
	}
}

func TestBitbucketWebhooks_enable(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	admin := f.as(t, "ausr_admin", security.RoleAdmin)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)
	h := newHookFixture(t)
	h.conns.conns[org] = &repositories.BitbucketConnection{ID: "bbws_o", OrgID: org, WorkspaceSlug: "acme", Status: repositories.BitbucketReady}
	h.conns.token[org] = "token-o"
	h.w.Sources = f.store.byRepository
	h.w.Authz = f.authz
	f.svc.SetSourceHooks(h.w.SourceHooks())
	h.api.createErr = repositories.ErrHookNotAdmin
	if _, err := f.svc.AddSource(ctx, admin, "proj_bare", AddSourceInput{Repository: "bitbucket.org/acme/api"}); err != nil {
		t.Fatal(err)
	}
	h.api.createErr = nil

	if _, err := h.w.Enable(ctx, dev, "bitbucket.org/acme/api"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a developer turning it on: %v", err)
	}
	if _, err := h.w.Enable(ctx, admin, "bitbucket.org/acme/web"); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("a repository with no source: %v", err)
	}
	if _, err := h.w.Enable(ctx, admin, "acme/api"); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("a GitHub repository: %v", err)
	}
	st, err := h.w.Enable(ctx, admin, "https://bitbucket.org/Acme/API")
	if err != nil || !st.On {
		t.Fatalf("turning it on: %+v, %v", st, err)
	}
	if len(h.api.created) != 1 || h.api.created[0].token != "token-o" {
		t.Fatalf("created %+v", h.api.created)
	}
}

// byRepository is the store's SourcesByRepository: live sources in any
// organization, whatever the repository's case.
func (m *memStore) byRepository(_ context.Context, repository string) ([]*Source, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Source{}
	for _, s := range m.sources {
		if s.RemovedAt == nil && strings.EqualFold(s.Repository, repository) {
			c := *s
			out = append(out, &c)
		}
	}
	return out, nil
}
