//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/xraph/dispatch"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/engine"
	dispatchpg "github.com/xraph/dispatch/store/postgres"
	"github.com/xraph/grove"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
)

func TestClusterScalingWakeRechecksCohort(t *testing.T) {
	managedCleanupEffectsRealAPI(t, false, "application", "cohort-wake")
}
func checkCohortWake(t *testing.T, f *poolFixture, p *planning.Plan, runner *deployments.Runner) {
	t.Helper()
	if err := ops.Migrate(t.Context(), f.sql.Config().ConnString(), []*migrate.Group{dispatchpg.Migrations}); err != nil {
		t.Fatal(err)
	}
	db, err := grove.Open(f.db)
	if err != nil {
		t.Fatal(err)
	}
	ds := dispatchpg.New(db)
	dispatcher, err := dispatch.New(dispatch.WithStore(ds))
	if err != nil {
		t.Fatal(err)
	}
	workflows := orchestration.NewDeployments()
	eng, err := engine.Build(dispatcher, engine.WithDurableWorkflows(workflows.Options("legacy", 1)))
	if err != nil {
		t.Fatal(err)
	}
	st := deployments.NewPgStore(f.db)

	workflows.Bind(workflowPlanner{}, runner, func() *engine.Engine { return eng })
	rs := orchestration.NewPgRunStore(f.db)
	workflows.BindRunStore(rs)
	options := workflows.LegacyCleanupOptions("managed-wait", 1)
	worker, err := drt.NewWorker(ds, options)
	if err != nil {
		t.Fatal(err)
	}
	workflows.BindWorker(orchestration.CurrentBuild, worker)
	if err = workflows.Start(t.Context(), "org", p.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	pumpWorkflow(t, worker)
	saved, err := f.plans.GetPlan(t.Context(), "org", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = workflows.Decide(t.Context(), p.ID, "approve", orchestration.Decision{Approve: true, Revision: saved.Revision, By: "usr"}); err != nil {
		t.Fatal(err)
	}
	pumpWorkflow(t, worker)
	dep, err := st.ForPlan(t.Context(), "org", p.ID)
	if err != nil || dep.Status != deployments.StatusPaused {
		t.Fatal("cohort did not pause execution", dep, err)
	}
	route, err := rs.ForDeployment(t.Context(), "org", dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	worker, err = drt.NewWorker(ds, options)
	if err != nil {
		t.Fatal(err)
	}
	workflows.BindWorker(orchestration.CurrentBuild, worker)
	inspect := func() int64 {
		t.Helper()
		v, e := ds.GetExecution(t.Context(), route.Request.Key)
		if e != nil {
			t.Fatal(e)
		}
		return v.LastSequence
	}
	before := inspect()
	for _, target := range []string{"wrong", "cloud", "cloud"} {
		if err = workflows.WakeCapacityCloud(t.Context(), "org", target, "cluster-capacity:cluster:review"); err != nil {
			t.Fatal(err)
		}
		after := inspect()
		if target == "wrong" && after != before || target == "cloud" && after != before+1 {
			t.Fatal("non-deduplicated capacity wake", target, before, after)
		}
	}
	pumpWorkflow(t, worker)
	dep, err = st.ForPlan(t.Context(), "org", p.ID)
	if err != nil || dep.Status != deployments.StatusPaused {
		t.Fatal("wake bypassed immutable demand", dep, err)
	}
	var groups, holds, nodes int
	if err = f.sql.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM loomplane_cluster_demand_cohorts WHERE closed_at IS NULL),(SELECT count(*) FROM loomplane_cluster_workload_holds),(SELECT count(*) FROM loomplane_cluster_pool_growth)`).Scan(&groups, &holds, &nodes); err != nil || groups != 1 || holds != 0 || nodes != 0 {
		t.Fatal(groups, holds, nodes, err)
	}
}
