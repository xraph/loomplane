//go:build integration

package integration

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/keysmith"
	keyid "github.com/xraph/keysmith/id"
	"github.com/xraph/keysmith/key"
	keymemory "github.com/xraph/keysmith/store/memory"
	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"

	"github.com/xraph/loomplane/internal/apikeys"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

type cleanupIssuerFixture struct {
	*poolFixture
	store     *planning.ManagedCleanupStore
	auth      *security.Authorizer
	member    *atomic.Bool
	scope     kube.ManagedScope
	selection planning.ManagedCleanupSelection
	request   kube.ManagedRequest
	ctx       context.Context
}

func newCleanupIssuerFixture(t *testing.T, retire bool, keyIDs ...string) *cleanupIssuerFixture {
	t.Helper()
	return prepareCleanupIssuerFixture(t, retire, nil, keyIDs...)
}

func prepareCleanupIssuerFixture(t *testing.T, retire bool, prepare func(*poolFixture, *planning.Service, *planning.Plan, *planning.Result, kube.ManagedScope), keyIDs ...string) *cleanupIssuerFixture {
	t.Helper()
	f, svc, plan, result, scope := cleanupTargetFixture(t)
	application := kube.NewControllerStore(f.db).WithClusterCapacity(true)
	serviceWrite, err := application.Issue(t.Context(), scope, kube.ManagedRequest{Version: "v1", Resource: "services", Namespace: scope.Namespace, Name: scope.Instance, Method: "POST"})
	if err != nil {
		t.Fatal(err)
	}
	if err = application.Resolve(t.Context(), serviceWrite, scope, kube.ManagedOutcome{State: "accepted", UID: "service-uid", ResourceVersion: "1", StatusCode: 201}); err != nil {
		t.Fatal(err)
	}
	if prepare != nil {
		prepare(f, svc, plan, result, scope)
	}
	if err = svc.PrepareManagedCleanupTargets(t.Context(), plan, result); err != nil {
		t.Fatal(err)
	}
	if retire {
		result.Actions[0].Op = planning.Update
		if checkErr := svc.PrepareManagedCleanupTargets(t.Context(), plan, result); checkErr != nil {
			t.Fatal(checkErr)
		}
	}
	if _, checkErr := f.sql.Exec(t.Context(), `UPDATE loomplane_deployments SET status='succeeded' WHERE id='cleanup-old-deployment'`); checkErr != nil {
		t.Fatal(checkErr)
	}
	saveCleanupReview(t, f, plan, result)
	principal := security.Principal{OrgID: "org", UserID: "reviewer"}
	if len(keyIDs) > 0 {
		principal.KeyID = keyIDs[0]
	}
	ctx := security.WithPrincipal(t.Context(), principal)
	if ok, checkErr := f.plans.Decide(ctx, "org", plan.ID, "approved", "reviewer", "", time.Now()); checkErr != nil || !ok {
		t.Fatalf("approval: %v %v", ok, checkErr)
	}
	if _, checkErr := f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES('cleanup-deployment','org','proj','env','production',$1,'cleanup','running','reviewer',now())`, plan.ID); checkErr != nil {
		t.Fatal(checkErr)
	}
	roles := memory.New()
	engine, err := warden.NewEngine(warden.WithStore(roles))
	if err != nil {
		t.Fatal(err)
	}
	member := &atomic.Bool{}
	member.Store(true)
	auth := security.NewAuthorizer(engine, roles, func(_ context.Context, org, user string) (string, bool, error) {
		return security.MemberOwner, member.Load() && org == "org" && user == "reviewer", nil
	})
	if err = auth.Assign(t.Context(), "org", "reviewer", security.RoleOwner, "test"); err != nil {
		t.Fatal(err)
	}
	return &cleanupIssuerFixture{poolFixture: f, store: planning.NewManagedCleanupStore(f.db, auth, nil), auth: auth, member: member, scope: scope, selection: planning.ManagedCleanupSelection{Org: "org", Plan: plan.ID, Action: "service:api", Deployment: "cleanup-deployment"}, request: kube.ManagedRequest{Group: "apps", Version: "v1", Resource: "deployments", Namespace: "apps", Name: "old-instance", UID: "deployment-uid", Method: "DELETE"}, ctx: ctx}
}

func (f *cleanupIssuerFixture) open(t *testing.T) *planning.ManagedCleanupGrant {
	t.Helper()
	g, err := f.store.Open(f.ctx, f.selection)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestManagedCleanupIssuerRecovery(t *testing.T) {
	f := newCleanupIssuerFixture(t, false)
	g := f.open(t)
	if g.SchemaVersion != 1 || g.Revision != 1 || g.CreatedAt.IsZero() {
		t.Fatalf("grant metadata: %+v", g)
	}
	application := kube.NewControllerStore(f.db).WithClusterCapacity(true)
	if _, checkErr := application.Issue(t.Context(), f.scope, f.request); checkErr == nil {
		t.Fatal("application scope reopened")
	}
	if _, checkErr := f.sql.Exec(t.Context(), `DELETE FROM loomplane_cloud_assignments WHERE org_id='org' AND project_id='proj' AND environment_id='env'`); checkErr != nil {
		t.Fatal(checkErr)
	}
	j := f.store.Journal("org", g.ID)
	f.request.ResourceVersion = "checked-version"
	unknown, err := j.Issue(f.ctx, f.scope, f.request)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"", "newer-version"} {
		changed := f.request
		changed.ResourceVersion = version
		if _, err = j.Issue(f.ctx, f.scope, changed); err == nil {
			t.Fatal("unknown cleanup allowed changed resourceVersion")
		}
	}
	other := f.request
	other.Resource = "services"
	other.Group = ""
	other.UID = "service-uid"
	if _, err = j.Issue(f.ctx, f.scope, other); err == nil {
		t.Fatal("unknown cleanup allowed a different target")
	}
	retry, err := j.Issue(f.ctx, f.scope, f.request)
	if err != nil || unknown == retry {
		t.Fatalf("retry: %s %s %v", unknown, retry, err)
	}
	accepted := kube.ManagedOutcome{State: "accepted", UID: f.request.UID, StatusCode: 200}
	if err = j.Resolve(t.Context(), retry, f.scope, accepted); err != nil {
		t.Fatal(err)
	}
	missing, err := j.Issue(f.ctx, f.scope, f.request)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Resolve(t.Context(), missing, f.scope, kube.ManagedOutcome{State: "rejected", StatusCode: 404}); err != nil {
		t.Fatal(err)
	}
	entries, err := application.ManagedIssuances(t.Context(), "org", f.scope.Release)
	if err != nil {
		t.Fatal(err)
	}
	unresolved := 0
	for _, entry := range entries {
		if entry.Outcome == nil {
			unresolved++
		}
	}
	if unresolved != 1 {
		t.Fatalf("retry erased unknown: %+v", entries)
	}
	var link string
	var revision int64
	if err = f.sql.QueryRow(t.Context(), `SELECT cleanup_grant_id,cleanup_grant_revision FROM loomplane_managed_workload_issuances WHERE id=$1`, unknown).Scan(&link, &revision); err != nil || link != g.ID || revision != g.Revision {
		t.Fatalf("link: %s %d %v", link, revision, err)
	}
	if _, err = f.sql.Exec(t.Context(), `DELETE FROM loomplane_bindings WHERE id='cleanup-binding'`); err != nil {
		t.Fatal(err)
	}
	again, err := f.store.Open(f.ctx, f.selection)
	if err != nil || again.ID != g.ID {
		t.Fatalf("reopen after binding removal: %+v %v", again, err)
	}
	if err = f.store.Revoke(t.Context(), "org", g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = j.Issue(f.ctx, f.scope, f.request); err == nil {
		t.Fatal("revoked grant issued")
	}
	if _, err = f.store.Open(f.ctx, f.selection); err == nil {
		t.Fatal("revoked grant reopened")
	}
	if err = j.Resolve(t.Context(), unknown, f.scope, accepted); err != nil {
		t.Fatalf("late accepted: %v", err)
	}
	if err = f.store.Journal("org", "foreign-grant").Resolve(t.Context(), unknown, f.scope, accepted); err == nil {
		t.Fatal("wrong grant resolved")
	}
	if err = f.store.Journal("foreign", g.ID).Resolve(t.Context(), unknown, f.scope, accepted); err == nil {
		t.Fatal("foreign tenant resolved")
	}
	for _, query := range []string{`UPDATE loomplane_managed_workload_issuances SET cleanup_grant_revision=NULL WHERE cleanup_grant_id IS NOT NULL`, `UPDATE loomplane_managed_cleanup_grants SET revoked_at=NULL,revision=revision+1`, `DELETE FROM loomplane_managed_cleanup_grants`, `UPDATE loomplane_managed_cleanup_grants SET deployment_id='other',revision=revision+1`} {
		if _, err = f.sql.Exec(t.Context(), query); err == nil {
			t.Fatal("grant identity changed")
		}
	}
}

func TestManagedCleanupIssuerCurrentAuthorization(t *testing.T) {
	for _, change := range []string{"membership", "role", "old-actor", "foreign", "readonly", "connection", "trust", "attachment", "grant", "target", "deployment", "epoch", "scope", "uid", "create", "newer-binding", "newer-authority", "unresolved-application"} {
		t.Run(change, func(t *testing.T) {
			f := newCleanupIssuerFixture(t, false)
			g := f.open(t)
			scope, request, ctx := f.scope, f.request, f.ctx
			switch change {
			case "membership":
				f.member.Store(false)
			case "role":
				if checkErr := f.auth.Assign(t.Context(), "org", "reviewer", security.RoleViewer, "test"); checkErr != nil {
					t.Fatal(checkErr)
				}
			case "old-actor":
				ctx = security.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "usr"})
			case "foreign":
				ctx = security.WithPrincipal(t.Context(), security.Principal{OrgID: "other", UserID: "reviewer"})
			case "readonly":
				ctx = security.WithPrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "reviewer", ReadOnly: true})
			case "scope":
				scope.Epoch++
			case "uid":
				request.UID = "forged"
			case "create":
				request.Method = "POST"
			}
			queries := map[string]string{
				"connection":             `UPDATE loomplane_cloud_connections SET generation=generation+1 WHERE id='cluster-runtime'`,
				"trust":                  `UPDATE loomplane_cloud_connections SET details=jsonb_set(details,'{cluster_ca}','"changed"') WHERE id='cluster-runtime'`,
				"attachment":             `UPDATE loomplane_runtime_attachments SET revision=revision+1 WHERE id='attachment'`,
				"grant":                  `UPDATE loomplane_cloud_project_grants SET revoked=true,revision=revision+1 WHERE project_id='proj'`,
				"target":                 `DELETE FROM loomplane_cloud_targets WHERE id='cluster-target'`,
				"deployment":             `UPDATE loomplane_deployments SET status='cancelled',finished_at=now() WHERE id='cleanup-deployment'`,
				"epoch":                  `UPDATE loomplane_managed_workload_authority SET epoch=epoch+1 WHERE org_id='org'`,
				"newer-binding":          `UPDATE loomplane_bindings SET plan_id='cleanup-new-plan' WHERE id='cleanup-binding'`,
				"newer-authority":        `INSERT INTO loomplane_managed_workload_authority(org_id,release_id,epoch,body) SELECT org_id,'newer-release',epoch,body FROM loomplane_managed_workload_authority WHERE org_id='org'`,
				"unresolved-application": `INSERT INTO loomplane_managed_workload_issuances(id,org_id,release_id,epoch,scope,request) SELECT 'unknown-late-app',org_id,release_id,epoch,scope,request FROM loomplane_managed_workload_issuances WHERE cleanup_grant_id IS NULL LIMIT 1`,
			}
			if change == "target" {
				if _, checkErr := f.sql.Exec(t.Context(), `DELETE FROM loomplane_cloud_assignments WHERE org_id='org'`); checkErr != nil {
					t.Fatal(checkErr)
				}
				if _, checkErr := f.sql.Exec(t.Context(), `DELETE FROM loomplane_bindings WHERE id='cleanup-binding'`); checkErr != nil {
					t.Fatal(checkErr)
				}
			}
			if query := queries[change]; query != "" {
				if _, checkErr := f.sql.Exec(t.Context(), query); checkErr != nil {
					t.Fatal(checkErr)
				}
			}
			if _, checkErr := f.store.Journal("org", g.ID).Issue(ctx, scope, request); checkErr == nil {
				t.Fatal("invalid issuance accepted")
			}
		})
	}
}

func TestManagedCleanupIssuerSelectionAndRetirement(t *testing.T) {
	f := newCleanupIssuerFixture(t, true)
	for _, sel := range []planning.ManagedCleanupSelection{{Org: "foreign", Plan: f.selection.Plan, Action: f.selection.Action, Deployment: f.selection.Deployment}, {Org: "org", Plan: f.selection.Plan, Action: f.selection.Action, Deployment: "cleanup-old-deployment"}, {Org: "org", Plan: f.selection.Plan, Action: "other", Deployment: f.selection.Deployment}, {Org: "org", Plan: f.selection.Plan, Action: f.selection.Action, Deployment: f.selection.Deployment, TargetIndex: 1}} {
		if _, checkErr := f.store.Open(f.ctx, sel); checkErr == nil {
			t.Fatal("invalid selector accepted")
		}
	}
	g := f.open(t)
	if _, checkErr := f.store.Journal("org", g.ID).Issue(f.ctx, f.scope, f.request); !errors.Is(checkErr, planning.ErrManagedCleanupUnavailable) {
		t.Fatalf("shared controller retirement: %v", checkErr)
	}
	f.request.Resource = "pods"
	f.request.Group = ""
	f.request.UID = "caller-asserted-pod"
	if _, checkErr := f.store.Journal("org", g.ID).Issue(f.ctx, f.scope, f.request); !errors.Is(checkErr, planning.ErrManagedCleanupUnavailable) {
		t.Fatalf("unproved descendant: %v", checkErr)
	}
}

func TestManagedCleanupIssuerKeyScope(t *testing.T) {
	keys := keymemory.New()
	engine, err := keysmith.NewEngine(keysmith.WithStore(keys))
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour)
	k := &key.Key{ID: keyid.NewKeyID(), AppID: "app", TenantID: "org", CreatedBy: "reviewer", State: key.StateActive, ExpiresAt: &expires, Metadata: map[string]any{"access": "write"}}
	if err = keys.Keys().Create(t.Context(), k); err != nil {
		t.Fatal(err)
	}
	f := newCleanupIssuerFixture(t, false, k.ID.String())
	if _, err = f.store.Open(f.ctx, f.selection); err == nil {
		t.Fatal("missing key verifier accepted")
	}
	f.store = planning.NewManagedCleanupStore(f.db, f.auth, apikeys.New(engine, "app", nil, nil))
	g := f.open(t)
	j := f.store.Journal("org", g.ID)
	for _, mutate := range []func(*key.Key){func(k *key.Key) { k.State = key.StateRevoked }, func(k *key.Key) { k.CreatedBy = "usr" }, func(k *key.Key) { k.Metadata = map[string]any{"access": "read"} }} {
		changed := *k
		mutate(&changed)
		if err = keys.Keys().Update(t.Context(), &changed); err != nil {
			t.Fatal(err)
		}
		if _, err = j.Issue(f.ctx, f.scope, f.request); err == nil {
			t.Fatal("changed saved key accepted")
		}
	}
}

func TestManagedCleanupIssuerRevocationRace(t *testing.T) {
	f := newCleanupIssuerFixture(t, false)
	g := f.open(t)
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('cloud-organization/org',0))`); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, issueErr := f.store.Journal("org", g.ID).Issue(f.ctx, f.scope, f.request)
		finished <- issueErr
	}()
	if _, err = tx.Exec(t.Context(), `UPDATE loomplane_managed_cleanup_grants SET revoked_at=clock_timestamp(),revision=revision+1 WHERE org_id='org' AND id=$1`, g.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-finished:
		if err == nil {
			t.Fatal("issuance crossed revocation")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("issuance did not leave organization lock")
	}
	var count int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_managed_workload_issuances WHERE cleanup_grant_id=$1`, g.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("race sent issuance: %d %v", count, err)
	}
}

type unprovedCleanupDescendant struct{}

func (unprovedCleanupDescendant) ReadCleanupDescendant(_ context.Context, target planning.ManagedCleanupTarget, request kube.ManagedRequest) (planning.ManagedCleanupDescendantEvidence, error) {
	return planning.ManagedCleanupDescendantEvidence{Scope: target.Scope, Request: request, Release: target.Scope.Release, PodUID: request.UID}, nil
}
func TestManagedCleanupIssuerDescendantUnavailable(t *testing.T) {
	f := newCleanupIssuerFixture(t, true)
	g := f.open(t)
	if _, checkErr := f.store.InspectDescendant(f.ctx, "org", g.ID, f.request, nil); !errors.Is(checkErr, planning.ErrManagedCleanupUnavailable) {
		t.Fatalf("nil reader: %v", checkErr)
	}
	f.request.Resource = "pods"
	f.request.Group = ""
	f.request.Name = "claimed-pod"
	f.request.UID = "claimed-uid"
	if _, checkErr := f.store.InspectDescendant(f.ctx, "org", g.ID, f.request, unprovedCleanupDescendant{}); checkErr == nil {
		t.Fatal("caller UID became proof")
	}
}

type countingCleanupDescendant struct{ calls *atomic.Int64 }

func (r countingCleanupDescendant) ReadCleanupDescendant(_ context.Context, _ planning.ManagedCleanupTarget, _ kube.ManagedRequest) (planning.ManagedCleanupDescendantEvidence, error) {
	r.calls.Add(1)
	return planning.ManagedCleanupDescendantEvidence{}, nil
}
func TestManagedCleanupIssuerReadAuthorization(t *testing.T) {
	f := newCleanupIssuerFixture(t, true)
	g := f.open(t)
	calls := &atomic.Int64{}
	f.member.Store(false)
	if _, checkErr := f.store.InspectDescendant(f.ctx, "org", g.ID, f.request, countingCleanupDescendant{calls: calls}); checkErr == nil {
		t.Fatal("revoked actor inspected")
	}
	if calls.Load() != 0 {
		t.Fatal("runtime reader called before authorization")
	}
	f.member.Store(true)
	if checkErr := f.store.Revoke(t.Context(), "org", g.ID); checkErr != nil {
		t.Fatal(checkErr)
	}
	if _, checkErr := f.store.InspectDescendant(f.ctx, "org", g.ID, f.request, countingCleanupDescendant{calls: calls}); checkErr == nil {
		t.Fatal("revoked grant inspected")
	}
	if calls.Load() != 0 {
		t.Fatal("revoked grant called runtime reader")
	}
}
