package agentknowledge_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/knowledge"
	cortexweave "github.com/xraph/cortex/knowledge/weave"
	"github.com/xraph/warden"
	"github.com/xraph/warden/assignment"
	wardenid "github.com/xraph/warden/id"
	"github.com/xraph/warden/store/memory"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/agentknowledge"
	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/security"
)

const (
	accessOrg = "aorg_access"
	// onP1 holds deployment:read on p1 alone, through a team.
	onP1 = "usr_p1"
)

// projectGrantAuthz is the real Authorizer over an in-memory Warden, with
// the agent's role and onP1 a viewer on p1 through a team, at p1's own
// namespace, and nothing at the root.
func projectGrantAuthz(t *testing.T, p1 string) *security.Authorizer {
	t.Helper()
	ctx := context.Background()
	store := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	a := security.NewAuthorizer(eng, store, func(context.Context, string, string) (string, bool, error) { return "", false, nil })
	if err = a.AssignAgent(ctx, accessOrg); err != nil {
		t.Fatal(err)
	}
	viewer, err := store.GetRoleBySlug(ctx, accessOrg, "", security.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	ns, ok := security.ProjectNamespace(p1)
	if !ok {
		t.Fatalf("%s has no namespace", p1)
	}
	if err = store.CreateAssignment(ctx, &assignment.Assignment{
		ID: wardenid.NewAssignmentID(), TenantID: accessOrg, NamespacePath: ns, RoleID: viewer.ID,
		SubjectKind: string(warden.SubjectUser), SubjectID: onP1, GrantedBy: security.SyncGrantedBy,
		Metadata: map[string]any{"sources": []string{"team_payments"}},
	}); err != nil {
		t.Fatal(err)
	}
	return a
}

func projectReport(id, projectID, text string) agentreports.KnowledgeDocument {
	return agentreports.KnowledgeDocument{
		SourceKey: agentreports.SourcePrefix + id, OrgID: accessOrg, ProjectID: projectID,
		Title: "The api deployment to production failed", Text: text, UpdatedAt: reportsBase,
		Metadata: map[string]string{"project_id": projectID, "status": "resolved"},
	}
}

// A knowledge search, the way cortex's knowledge_search makes it, gives a
// person's run only the past reports of projects they read deployments
// on: someone with deployment:read on p1 alone, through a team, gets p1's
// report and never p2's, nor one that names no project, which needs the
// whole workspace. The docs reach everyone, the agent alone reads every
// report, and a run with no principal reads none.
func TestKnowledgeSearchKeepsReportsToTheirProject(t *testing.T) {
	f := newFixture(t, dims)
	ctx := context.Background()
	p1, p2 := typeid.MustGenerate("proj").String(), typeid.MustGenerate("proj").String()
	if _, err := f.svc.IngestDocs(ctx); err != nil {
		t.Fatal(err)
	}
	o := agentknowledge.ReportObserver{Knowledge: f.svc}
	for _, doc := range []agentreports.KnowledgeDocument{
		projectReport("arep_p1", p1, "Project one's deployment failed: the image wasn't found."),
		projectReport("arep_p2", p2, "Project two's deployment failed: its database password expired."),
		projectReport("arep_none", "", "A deployment failed somewhere in the workspace."),
	} {
		if err := o.ReportChanged(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	authz := projectGrantAuthz(t, p1)
	search := cortexweave.New(f.weave, cortexweave.WithShared(cortexweave.SharedCollection{
		Tenant: agentknowledge.DocsTenant, Collection: agentknowledge.DocsCollection,
	}))
	found := func(p any) (reports []string, docs int) {
		t.Helper()
		sctx := cortex.WithScope(ctx, agentscope.Scope{OrgID: accessOrg}.Cortex())
		if p != nil {
			sctx = cortex.WithPrincipal(sctx, p)
		}
		chunks, err := search.Retrieve(sctx, "Why did the deployment fail?", &knowledge.RetrieveParams{TopK: 20})
		if err != nil {
			t.Fatal(err)
		}
		if got := len(chunks); got < 7 {
			t.Fatalf("the search found %d chunks, want both reports and the docs", got)
		}
		chunks, err = agentknowledge.Readable(sctx, authz, chunks)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range chunks {
			if c.Metadata["tenant_id"] == agentknowledge.DocsTenant {
				docs++
				continue
			}
			reports = append(reports, c.Metadata["source_key"])
		}
		return reports, docs
	}

	member := security.Principal{Kind: security.KindUser, UserID: onP1, OrgID: accessOrg}
	got, docs := found(security.AgentPrincipal(accessOrg, &member))
	if !slices.Equal(got, []string{"report/arep_p1"}) || docs == 0 {
		t.Fatalf("p1's member read reports %v and %d docs chunks, want p1's report and the docs", got, docs)
	}
	got, _ = found(security.AgentPrincipal(accessOrg, nil))
	slices.Sort(got)
	if !slices.Equal(got, []string{"report/arep_none", "report/arep_p1", "report/arep_p2"}) {
		t.Fatalf("the agent alone read %v, want every report", got)
	}
	if got, docs = found(nil); len(got) != 0 || docs == 0 {
		t.Fatalf("a run with no principal read %v and %d docs chunks", got, docs)
	}
}

// fakeProjectAuthz allows deployment:read on the projects it lists.
type fakeProjectAuthz struct {
	projects []string
	err      error
}

func (a fakeProjectAuthz) AllowedOn(_ context.Context, _ security.Principal, perm security.Permission, projectID string) (bool, error) {
	if a.err != nil {
		return false, a.err
	}
	return perm == security.DeploymentRead && slices.Contains(a.projects, projectID), nil
}

// Readable decides a chunk by what weave hands back: its own metadata.
// A check that fails, or no authorizer, drops a report's chunk; one that
// names no project needs deployment:read on the whole workspace; a chunk
// that names no project and isn't a report's passes.
func TestReadableFailsClosed(t *testing.T) {
	ctx := cortex.WithPrincipal(cortex.WithScope(context.Background(), agentscope.Scope{OrgID: accessOrg}.Cortex()),
		security.AgentPrincipal(accessOrg, nil))
	chunks := []knowledge.ScoredChunk{
		{Content: "docs", Metadata: map[string]string{"source_key": "docs/index"}},
		{Content: "runbook", Metadata: map[string]string{"kind": "runbook", "source_key": "runbook/restart"}},
		{Content: "p1", Metadata: map[string]string{"kind": "report", "project_id": "proj_1", "source_key": "report/a"}},
		{Content: "p2", Metadata: map[string]string{"kind": "report", "project_id": "proj_2", "source_key": "report/b"}},
		{Content: "no project", Metadata: map[string]string{"kind": "report", "source_key": "report/c"}},
		{Content: "unstamped", Metadata: map[string]string{"source_key": "report/d"}},
		{Content: "runbook on p2", Metadata: map[string]string{"kind": "runbook", "project_id": "proj_2"}},
	}
	contents := func(cs []knowledge.ScoredChunk) []string {
		out := make([]string, 0, len(cs))
		for _, c := range cs {
			out = append(out, c.Content)
		}
		return out
	}
	got, err := agentknowledge.Readable(ctx, fakeProjectAuthz{projects: []string{"proj_1"}}, chunks)
	if err != nil || !slices.Equal(contents(got), []string{"docs", "runbook", "p1"}) {
		t.Fatalf("Readable = %v, %v", contents(got), err)
	}
	// deployment:read on the whole workspace reads a report with no
	// project, but not one whose project it isn't granted on.
	got, err = agentknowledge.Readable(ctx, fakeProjectAuthz{projects: []string{""}}, chunks)
	if err != nil || !slices.Equal(contents(got), []string{"docs", "runbook", "no project", "unstamped"}) {
		t.Fatalf("Readable for the workspace = %v, %v", contents(got), err)
	}
	boom := errors.New("warden is down")
	got, err = agentknowledge.Readable(ctx, fakeProjectAuthz{err: boom}, chunks)
	if !errors.Is(err, boom) || !slices.Equal(contents(got), []string{"docs", "runbook"}) {
		t.Fatalf("Readable with a failing check = %v, %v", contents(got), err)
	}
	if got, err = agentknowledge.Readable(ctx, nil, chunks); err != nil || !slices.Equal(contents(got), []string{"docs", "runbook"}) {
		t.Fatalf("Readable with no authorizer = %v, %v", contents(got), err)
	}
	// A run scoped to one project reads that project's reports alone, even
	// where the person may read others. The docs and workspace-wide
	// documents still pass.
	scoped := cortex.WithPrincipal(cortex.WithScope(context.Background(), agentscope.Scope{OrgID: accessOrg, ProjectID: "proj_1"}.Cortex()),
		security.AgentPrincipal(accessOrg, nil))
	got, err = agentknowledge.Readable(scoped, fakeProjectAuthz{projects: []string{"", "proj_1", "proj_2"}}, chunks)
	if err != nil || !slices.Equal(contents(got), []string{"docs", "runbook", "p1"}) {
		t.Fatalf("Readable in a project's run = %v, %v", contents(got), err)
	}
	// So does a person's chat scoped to proj_1, though the person may read
	// proj_2's reports too.
	chat := cortex.WithPrincipal(cortex.WithScope(context.Background(), agentscope.Scope{OrgID: accessOrg, ProjectID: "proj_1"}.Cortex()),
		security.AgentPrincipal(accessOrg, &security.Principal{OrgID: accessOrg, UserID: onP1}))
	got, err = agentknowledge.Readable(chat, fakeProjectAuthz{projects: []string{"proj_1", "proj_2"}}, chunks)
	if err != nil || !slices.Equal(contents(got), []string{"docs", "runbook", "p1"}) {
		t.Fatalf("Readable in a person's project chat = %v, %v", contents(got), err)
	}
	// A person's principal on another workspace's scope reads no report.
	other := cortex.WithPrincipal(cortex.WithScope(context.Background(), agentscope.Scope{OrgID: "aorg_other"}.Cortex()),
		security.AgentPrincipal(accessOrg, nil))
	if got, _ = agentknowledge.Readable(other, fakeProjectAuthz{projects: []string{"proj_1"}}, chunks); !slices.Equal(contents(got), []string{"docs", "runbook"}) {
		t.Fatalf("Readable across workspaces = %v", contents(got))
	}
}
