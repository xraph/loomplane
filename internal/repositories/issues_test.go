package repositories_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"

	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/repositories/githubtest"
	"github.com/xraph/loomplane/internal/security"
)

const (
	issuesOrg  = "aorg_a"
	acmeID     = int64(42)
	agentLabel = "loomplane-agent"
)

var (
	withoutIssues = map[string]string{"contents": "read", "metadata": "read"}
	withIssues    = map[string]string{"contents": "read", "issues": "write", "metadata": "read"}
)

// permStore is a Store that counts SavePermissions calls.
type permStore struct {
	mu    sync.Mutex
	recs  []*repositories.InstallationRecord
	saves int
}

func (s *permStore) SaveInstallation(_ context.Context, r *repositories.InstallationRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *r
	s.recs = append(s.recs, &c)
	return nil
}

func (s *permStore) ListInstallations(_ context.Context, orgID string) ([]*repositories.InstallationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*repositories.InstallationRecord{}
	for _, r := range s.recs {
		if r.OrgID == orgID {
			c := *r
			c.Permissions = maps.Clone(r.Permissions)
			out = append(out, &c)
		}
	}
	return out, nil
}

func (s *permStore) DeleteInstallation(context.Context, string, int64) error { return nil }

func (s *permStore) SavePermissions(_ context.Context, orgID string, id int64, perms map[string]string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	for _, r := range s.recs {
		if r.OrgID == orgID && r.ID == id {
			r.Permissions = maps.Clone(perms)
			r.PermissionsCheckedAt = &at
		}
	}
	return nil
}

func (s *permStore) saved() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type issuesFixture struct {
	gh    *githubtest.Server
	app   *repositories.App
	store *permStore
	conns *repositories.Connections
	clock *testClock
	authz *security.Authorizer
}

// newIssuesFixture is a workspace with acme's installation connected,
// which has accepted perms on GitHub but whose permissions Loomplane
// hasn't read yet.
func newIssuesFixture(t *testing.T, perms map[string]string) *issuesFixture {
	t.Helper()
	gh := githubtest.New(t)
	gh.AddInstallation(acmeID, "acme", perms)
	app, err := repositories.NewApp(gh.AppConfig())
	if err != nil {
		t.Fatal(err)
	}
	ws := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(ws))
	if err != nil {
		t.Fatal(err)
	}
	f := &issuesFixture{
		gh: gh, app: app, store: &permStore{},
		clock: &testClock{t: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)},
		authz: security.NewAuthorizer(eng, ws, nil),
	}
	f.conns = repositories.NewConnections(f.store, app, f.authz, nil, []byte("0123456789abcdef0123456789abcdef"))
	repositories.SetClock(f.conns, f.clock.now)
	f.connect(t, acmeID, "acme")
	return f
}

func (f *issuesFixture) connect(t *testing.T, id int64, account string) {
	t.Helper()
	err := f.store.SaveInstallation(context.Background(), &repositories.InstallationRecord{
		OrgID: issuesOrg, ID: id, Account: account, AccountType: "Organization", CreatedAt: f.clock.now(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *issuesFixture) issues(t *testing.T) repositories.Issues {
	t.Helper()
	iss, err := f.conns.Issues(context.Background(), issuesOrg, "acme/api")
	if err != nil {
		t.Fatalf("Issues: %v", err)
	}
	return iss
}

func TestInstallationReadsPermissions(t *testing.T) {
	f := newIssuesFixture(t, withIssues)
	inst, err := f.app.Installation(context.Background(), acmeID)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(inst.Permissions, withIssues) {
		t.Fatalf("permissions %v", inst.Permissions)
	}
}

func TestIssuesTokenAsksOnlyForIssues(t *testing.T) {
	f := newIssuesFixture(t, withIssues)
	if _, err := f.app.IssuesToken(context.Background(), acmeID, "api"); err != nil {
		t.Fatal(err)
	}
	reqs := f.gh.TokenRequests()
	if len(reqs) != 1 {
		t.Fatalf("token requests %+v", reqs)
	}
	got := reqs[0]
	if len(got.Repositories) != 1 || got.Repositories[0] != "api" ||
		!maps.Equal(got.Permissions, map[string]string{"issues": "write", "metadata": "read"}) {
		t.Fatalf("the token asked for %+v", got)
	}
}

func TestAccessWithoutApp(t *testing.T) {
	ctx := context.Background()
	conns := repositories.NewConnections(&permStore{}, nil, nil, nil, nil)
	acc, err := conns.Access(ctx, issuesOrg, "acme/api")
	if err != nil || acc.Accepted || !strings.Contains(acc.Reason, "Herald only") || acc.Repository != "acme/api" {
		t.Fatalf("Access = %+v, %v", acc, err)
	}
	if _, err := conns.Issues(ctx, issuesOrg, "acme/api"); !errors.Is(err, repositories.ErrNoInstallation) || !strings.Contains(err.Error(), "Herald only") {
		t.Fatalf("Issues: %v", err)
	}
}

func TestAccessWithoutInstallationOnOwner(t *testing.T) {
	ctx := context.Background()
	f := newIssuesFixture(t, withIssues)
	acc, err := f.conns.Access(ctx, issuesOrg, "globex/api")
	if err != nil || acc.Accepted || acc.Reason != "No GitHub App installation on globex is connected to this workspace" {
		t.Fatalf("Access = %+v, %v", acc, err)
	}
	// Another organization's installation on acme isn't this one's.
	acc, err = f.conns.Access(ctx, "aorg_b", "acme/api")
	if err != nil || acc.Accepted || !strings.Contains(acc.Reason, "acme") {
		t.Fatalf("another organization: %+v, %v", acc, err)
	}
	if _, err := f.conns.Issues(ctx, "aorg_b", "acme/api"); !errors.Is(err, repositories.ErrNoInstallation) {
		t.Fatalf("Issues: %v", err)
	}
}

func TestAccessOnlyOnGitHub(t *testing.T) {
	ctx := context.Background()
	f := newIssuesFixture(t, withIssues)
	for _, key := range []string{"gitlab.com/acme/api", "bitbucket.org/acme/api", "git.example.com:8443/team/sub/api"} {
		acc, err := f.conns.Access(ctx, issuesOrg, key)
		want := "Loomplane opens issues on GitHub repositories only, so reports for " + key + " go to Herald only"
		if err != nil || acc.Accepted || acc.Reason != want {
			t.Fatalf("Access(%q) = %+v, %v", key, acc, err)
		}
		if _, err := f.conns.Issues(ctx, issuesOrg, key); !errors.Is(err, repositories.ErrNoInstallation) || !strings.Contains(err.Error(), want) {
			t.Fatalf("Issues(%q): %v", key, err)
		}
	}
	// GitHub written with its host is still GitHub.
	if acc, err := f.conns.Access(ctx, issuesOrg, "github.com/acme/api"); err != nil || !acc.Accepted || acc.Repository != "acme/api" {
		t.Fatalf("github.com/acme/api: %+v, %v", acc, err)
	}
}

// A repository key Loomplane can't read gets a reason, never an error,
// and Issues says there's no installation, so delivery doesn't retry it
// forever.
func TestAccessToAnUnreadableKey(t *testing.T) {
	ctx := context.Background()
	f := newIssuesFixture(t, withIssues)
	for _, key := range []string{"acme", "acme/", "acme/api/../../etc", "github.com/"} {
		if _, perr := repositories.ParseKey(key); perr == nil {
			t.Fatalf("ParseKey(%q) reads it", key)
		}
		acc, err := f.conns.Access(ctx, issuesOrg, key)
		want := "Loomplane couldn't read the repository " + key + ", so reports go to Herald only"
		if err != nil || acc.Accepted || acc.Reason != want {
			t.Fatalf("Access(%q) = %+v, %v", key, acc, err)
		}
		if _, err := f.conns.Issues(ctx, issuesOrg, key); !errors.Is(err, repositories.ErrNoInstallation) || !strings.Contains(err.Error(), want) {
			t.Fatalf("Issues(%q): %v", key, err)
		}
	}
}

func TestAccessBeforeAcceptance(t *testing.T) {
	ctx := context.Background()
	f := newIssuesFixture(t, withoutIssues)
	acc, err := f.conns.Access(ctx, issuesOrg, "acme/api")
	if err != nil {
		t.Fatal(err)
	}
	want := "acme hasn't accepted the Issues permission yet. An owner of acme can review the App's permissions on GitHub."
	if acc.Accepted || acc.Reason != want || acc.Account != "acme" || acc.InstallationID != acmeID ||
		acc.ManageURL != f.gh.URL()+"/organizations/acme/settings/installations/42" {
		t.Fatalf("Access = %+v", acc)
	}
	_, err = f.conns.Issues(ctx, issuesOrg, "acme/api")
	if !errors.Is(err, repositories.ErrIssuesNotAccepted) || !strings.Contains(err.Error(), want) {
		t.Fatalf("Issues: %v", err)
	}
	for _, r := range f.gh.TokenRequests() {
		t.Fatalf("minted a token before acceptance: %+v", r)
	}
}

func TestAccessNoticesAcceptance(t *testing.T) {
	ctx := context.Background()
	f := newIssuesFixture(t, withoutIssues)
	if acc, err := f.conns.Access(ctx, issuesOrg, "acme/api"); err != nil || acc.Accepted {
		t.Fatalf("before: %+v, %v", acc, err)
	}
	if f.store.saved() != 1 {
		t.Fatalf("saved %d times", f.store.saved())
	}

	f.gh.SetPermissions(acmeID, withIssues)
	f.clock.add(repositories.PermissionsTTL - time.Second)
	if acc, err := f.conns.Access(ctx, issuesOrg, "acme/api"); err != nil || acc.Accepted {
		t.Fatalf("within the TTL: %+v, %v", acc, err)
	}
	if f.store.saved() != 1 {
		t.Fatalf("asked GitHub again within the TTL: saved %d times", f.store.saved())
	}

	f.clock.add(2 * time.Second)
	if acc, err := f.conns.Access(ctx, issuesOrg, "acme/api"); err != nil || !acc.Accepted || acc.Reason != "" {
		t.Fatalf("after the TTL: %+v, %v", acc, err)
	}
	if f.store.saved() != 2 {
		t.Fatalf("saved %d times", f.store.saved())
	}
}

func TestAccessKeepsLastKnownPermissionsWhenGitHubFails(t *testing.T) {
	ctx := context.Background()
	f := newIssuesFixture(t, withoutIssues)
	checked := f.clock.now().Add(-10 * time.Minute)
	if err := f.store.SavePermissions(ctx, issuesOrg, acmeID, withIssues, checked); err != nil {
		t.Fatal(err)
	}
	f.gh.FailNext("/app/installations/42", http.StatusInternalServerError)
	acc, err := f.conns.Access(ctx, issuesOrg, "acme/api")
	if err != nil || !acc.Accepted {
		t.Fatalf("Access = %+v, %v", acc, err)
	}
	if f.store.saved() != 1 {
		t.Fatalf("saved the failure: %d", f.store.saved())
	}

	// With nothing ever read, there's nothing to fall back on.
	f.gh.AddInstallation(43, "globex", withIssues)
	f.connect(t, 43, "globex")
	f.gh.FailNext("/app/installations/43", http.StatusInternalServerError)
	if _, err := f.conns.Access(ctx, issuesOrg, "globex/api"); err == nil {
		t.Fatal("a GitHub failure with no stored permissions passed")
	}
}

func TestIssuesCannotSeeTheRepository(t *testing.T) {
	f := newIssuesFixture(t, withIssues)
	f.gh.FailNext("/app/installations/42/access_tokens", http.StatusUnprocessableEntity)
	_, err := f.conns.Issues(context.Background(), issuesOrg, "acme/api")
	if !errors.Is(err, repositories.ErrNoInstallation) || errors.Is(err, repositories.ErrNotFound) || !strings.Contains(err.Error(), "acme/api") {
		t.Fatalf("Issues: %v", err)
	}
}

func TestIssueLifecycle(t *testing.T) {
	ctx := context.Background()
	f := newIssuesFixture(t, withIssues)
	iss := f.issues(t)

	for range 2 {
		if err := iss.EnsureLabel(ctx, agentLabel, "6f42c1", "Reports from Loomplane's agent"); err != nil {
			t.Fatalf("EnsureLabel: %v", err)
		}
	}
	marker := "<!-- loomplane-report:rpt_1 -->"
	created, err := iss.Create(ctx, repositories.IssueInput{Title: "api failed to deploy", Body: "What happened.\n\n" + marker, Labels: []string{agentLabel}})
	if err != nil || created.Number != 1 || created.State != "open" || created.URL != f.gh.URL()+"/acme/api/issues/1" {
		t.Fatalf("Create = %+v, %v", created, err)
	}
	found, err := iss.Find(ctx, agentLabel, marker)
	if err != nil || found == nil || found.Number != created.Number {
		t.Fatalf("Find = %+v, %v", found, err)
	}
	none, err := iss.Find(ctx, agentLabel, "<!-- loomplane-report:rpt_2 -->")
	if err != nil || none != nil {
		t.Fatalf("Find another marker = %+v, %v", none, err)
	}

	title := "api failed to deploy, twice"
	edited, err := iss.Edit(ctx, created.Number, repositories.IssueEdit{Title: &title})
	if err != nil || edited.State != "open" {
		t.Fatalf("Edit title = %+v, %v", edited, err)
	}
	closed, err := iss.Edit(ctx, created.Number, repositories.IssueEdit{State: "closed", StateReason: "completed"})
	if err != nil || closed.State != "closed" {
		t.Fatalf("Edit state = %+v, %v", closed, err)
	}
	if err := iss.Comment(ctx, created.Number, "It deployed."); err != nil {
		t.Fatalf("Comment: %v", err)
	}

	got := f.gh.Issues("acme/api")
	if len(got) != 1 {
		t.Fatalf("issues %+v", got)
	}
	is := got[0]
	if is.Title != title || is.Body != "What happened.\n\n"+marker || is.State != "closed" || is.StateReason != "completed" ||
		len(is.Labels) != 1 || is.Labels[0] != agentLabel || len(is.Comments) != 1 || is.Comments[0] != "It deployed." {
		t.Fatalf("issue %+v", is)
	}
	for _, r := range f.gh.Requests() {
		if strings.Contains(r, "/repos/") && !strings.Contains(r, "/repos/acme/api/") {
			t.Fatalf("a request outside the repository: %s", r)
		}
	}
	if found, err := iss.Find(ctx, agentLabel, marker); err != nil || found == nil || found.State != "closed" {
		t.Fatalf("Find a closed issue = %+v, %v", found, err)
	}
}

func TestIssueErrorsAreReadByStatus(t *testing.T) {
	ctx := context.Background()
	f := newIssuesFixture(t, withIssues)
	iss := f.issues(t)
	created, err := iss.Create(ctx, repositories.IssueInput{Title: "t", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := iss.Edit(ctx, 99, repositories.IssueEdit{State: "closed"}); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("Edit a missing issue: %v", err)
	}
	path := fmt.Sprintf("/repos/acme/api/issues/%d", created.Number)
	f.gh.FailNext(path, http.StatusGone)
	if _, err := iss.Edit(ctx, created.Number, repositories.IssueEdit{State: "closed"}); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("Edit a deleted issue: %v", err)
	}
	// A refusal isn't a missing issue: reading it as one would open a
	// duplicate.
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity, http.StatusInternalServerError} {
		f.gh.FailNext(path, status)
		if _, err := iss.Edit(ctx, created.Number, repositories.IssueEdit{State: "closed"}); err == nil || errors.Is(err, repositories.ErrNotFound) {
			t.Fatalf("Edit answered %d: %v", status, err)
		}
	}
	f.gh.FailNext("/repos/acme/api/labels", http.StatusUnprocessableEntity)
	if err := iss.EnsureLabel(ctx, agentLabel, "6f42c1", ""); err == nil || errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("EnsureLabel refused: %v", err)
	}
	f.gh.FailNext("/repos/acme/api/issues", http.StatusForbidden)
	if _, err := iss.Find(ctx, agentLabel, "x"); err == nil || errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("Find refused: %v", err)
	}
}

func TestFindPagesThroughIssues(t *testing.T) {
	ctx := context.Background()
	f := newIssuesFixture(t, withIssues)
	iss := f.issues(t)
	const marker = "<!-- loomplane-report:rpt_old -->"
	// GitHub lists newest first, so issue 11 of 150 is the 140th listed,
	// on the second page.
	for i := 1; i <= 150; i++ {
		body := fmt.Sprintf("report %d", i)
		if i == 11 {
			body += "\n\n" + marker
		}
		if _, err := iss.Create(ctx, repositories.IssueInput{Title: fmt.Sprintf("report %d", i), Body: body, Labels: []string{agentLabel}}); err != nil {
			t.Fatal(err)
		}
	}
	found, err := iss.Find(ctx, agentLabel, marker)
	if err != nil || found == nil || found.Number != 11 {
		t.Fatalf("Find = %+v, %v", found, err)
	}
	var lists []string
	for _, r := range f.gh.Requests() {
		if strings.HasPrefix(r, "GET /repos/acme/api/issues?") {
			lists = append(lists, r)
		}
	}
	if len(lists) != 2 || !strings.Contains(lists[0], "labels="+agentLabel) || !strings.Contains(lists[0], "state=all") ||
		!strings.Contains(lists[0], "per_page=100") || !strings.Contains(lists[1], "page=2") {
		t.Fatalf("list requests %v", lists)
	}
}

func TestFindSkipsPullRequests(t *testing.T) {
	ctx := context.Background()
	f := newIssuesFixture(t, withIssues)
	iss := f.issues(t)
	const marker = "<!-- loomplane-report:rpt_1 -->"
	f.gh.AddPullRequest("acme/api", "a fix", "Quotes the report.\n\n"+marker, agentLabel)
	if found, err := iss.Find(ctx, agentLabel, marker); err != nil || found != nil {
		t.Fatalf("Find found a pull request: %+v, %v", found, err)
	}
}

// Someone who can label issues can't plant a marker for Loomplane to
// take over, as a person or through another bot: only the App's own
// issues are found.
func TestFindSkipsIssuesAPersonOpened(t *testing.T) {
	ctx := context.Background()
	f := newIssuesFixture(t, withIssues)
	iss := f.issues(t)
	const marker = "<!-- loomplane-report:rpt_1 -->"
	f.gh.AddIssue("acme/api", githubtest.IssueRecord{Title: "planted", Body: "Take this over.\n\n" + marker, Labels: []string{agentLabel}})
	// Another App's bot, such as a workflow's, doesn't count either.
	f.gh.AddIssue("acme/api", githubtest.IssueRecord{Title: "from a workflow", Body: marker, Labels: []string{agentLabel},
		Author: "github-actions[bot]", AuthorType: "Bot"})
	if found, err := iss.Find(ctx, agentLabel, marker); err != nil || found != nil {
		t.Fatalf("Find found a person's issue: %+v, %v", found, err)
	}
	created, err := iss.Create(ctx, repositories.IssueInput{Title: "api failed", Body: marker, Labels: []string{agentLabel}})
	if err != nil {
		t.Fatal(err)
	}
	if found, err := iss.Find(ctx, agentLabel, marker); err != nil || found == nil || found.Number != created.Number {
		t.Fatalf("Find = %+v, %v; want the App's issue %d", found, err, created.Number)
	}
	if got := f.gh.Issues("acme/api"); got[2].Author != githubtest.AppSlug+"[bot]" || got[2].AuthorType != "Bot" {
		t.Fatalf("the App's issue is authored by %s (%s)", got[2].Author, got[2].AuthorType)
	}
}

func TestIssuesStatusListsEveryInstallation(t *testing.T) {
	ctx := context.Background()
	f := newIssuesFixture(t, withIssues)
	f.gh.AddInstallation(43, "globex", withoutIssues)
	f.connect(t, 43, "globex")
	if err := f.authz.Assign(ctx, issuesOrg, "ausr_dev", security.RoleDeveloper, "test"); err != nil {
		t.Fatal(err)
	}
	dev := security.Principal{OrgID: issuesOrg, UserID: "ausr_dev"}

	st, err := f.conns.IssuesStatus(ctx, dev)
	if err != nil || !st.Configured || len(st.Installations) != 2 {
		t.Fatalf("IssuesStatus = %+v, %v", st, err)
	}
	byAccount := map[string]repositories.InstallationIssues{}
	for _, in := range st.Installations {
		byAccount[in.Account] = in
	}
	acme, globex := byAccount["acme"], byAccount["globex"]
	if !acme.Accepted || acme.ManageURL != f.gh.URL()+"/organizations/acme/settings/installations/42" || acme.CheckedAt == nil || acme.AccountType != "Organization" {
		t.Fatalf("acme %+v", acme)
	}
	if globex.Accepted || globex.ManageURL == "" || globex.CheckedAt == nil {
		t.Fatalf("globex %+v", globex)
	}

	if _, err = f.conns.IssuesStatus(ctx, security.Principal{OrgID: issuesOrg, UserID: "ausr_nobody"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("someone without a role: %v", err)
	}

	none := repositories.NewConnections(&permStore{}, nil, f.authz, nil, nil)
	st, err = none.IssuesStatus(ctx, dev)
	if err != nil || st.Configured || st.Installations == nil || len(st.Installations) != 0 {
		t.Fatalf("without an App: %+v, %v", st, err)
	}
}
