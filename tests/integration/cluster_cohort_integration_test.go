//go:build integration

package integration

import (
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/deployments"
)

func TestClusterScalingCancelledDemand(t *testing.T) {
	f, svc, plan, result, _ := managedDemandFixture(t)
	svc.BindManagedRuntime(ledgerRuntimeFixture{})
	cfg := result.Actions[0].Service
	cfg.Scale.Max = 90
	if err := svc.PrepareClusterWorkloadDemands(t.Context(), plan, result); err != nil {
		t.Fatal(err)
	}
	saveManagedDemandPlan(t, f, plan, result)
	st := deployments.NewPgStore(f.db)
	dep := capacityDeployment(t, st, plan)
	var cohort string
	for range 2 {
		decision, err := svc.ReconcileClusterDemand(t.Context(), plan.OrgID, "cloud", cfg.ClusterDemand.Input.ID)
		if err != nil || decision.Block == nil || decision.Block.Reason != "workload_capacity" || decision.ClusterCohortID == "" {
			t.Fatal("missing pending review", decision, err)
		}
		if cohort != "" && decision.ClusterCohortID != cohort {
			t.Fatal("duplicate cohort")
		}
		cohort = decision.ClusterCohortID
	}
	var block *cloudinfra.CapacityAdmissionError
	if err := svc.ResolveCapacityIntents(t.Context(), plan.OrgID, plan.ID); !errors.As(err, &block) {
		t.Fatal("workflow did not pause", err)
	}
	var open, members, held int
	read := func() {
		t.Helper()
		if err := f.sql.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM loomplane_cluster_demand_cohorts WHERE closed_at IS NULL),(SELECT count(*) FROM loomplane_cluster_demand_members),(SELECT count(*) FROM loomplane_cluster_workload_holds)`).Scan(&open, &members, &held); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if open != 1 || members != 1 || held != 0 {
		t.Fatal(open, members, held)
	}
	// Hold membership open as an uncoupled running-deployment cancellation
	// arrives. Waiting on an actual PostgreSQL lock keeps the race deterministic.
	tx, e := f.db.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback() }()
	var pid int
	if e = tx.QueryRow(t.Context(), `SELECT pg_backend_pid() FROM loomplane_lock_cluster_demand($1,$2)`, plan.OrgID, plan.ID).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		done <- st.Finish(t.Context(), plan.OrgID, dep.ID, deployments.StatusCancelled, "test cancellation", time.Now())
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if e = f.sql.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancellation did not fence demand attachment")
		}
		time.Sleep(10 * time.Millisecond)
	}
	joined, e := cloudinfra.JoinReviewedClusterDemand(t.Context(), tx, cfg.ClusterDemand.Input, cfg.ClusterDemand.Profile.PoolID, cfg.ClusterDemand.Profile.Digest)
	if e != nil || joined != cohort {
		t.Fatal("changed concurrent membership", joined, e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	read()
	if open != 0 || members != 0 || held != 0 {
		t.Fatal("cancelled cohort still active", open, members, held)
	}
	decision, err := svc.ReconcileClusterDemand(t.Context(), plan.OrgID, "cloud", cfg.ClusterDemand.Input.ID)
	if err != nil || decision.Block == nil || decision.Block.Reason != "cluster_demand_cancelled" {
		t.Fatal(decision, err)
	}
	for _, wrong := range []struct{ org, cloud, id string }{{"other", "cloud", cfg.ClusterDemand.Input.ID}, {plan.OrgID, "wrong", cfg.ClusterDemand.Input.ID}, {plan.OrgID, "cloud", "no-demand"}} {
		if _, err := svc.ReconcileClusterDemand(t.Context(), wrong.org, wrong.cloud, wrong.id); err == nil {
			t.Fatal("unapproved scope")
		}
	}
}
