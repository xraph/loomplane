//go:build integration

package integration

import (
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/releasehealth"
)

// A verdict's scope, and so its realtime event's, is its deployment's: the
// caller can't name another project, record into another org's deployment,
// or write a second row for the same judgement under another ID.
func TestCanaryVerdictTakesItsDeploymentsScope(t *testing.T) {
	f := newPoolFixture(t)
	plan := futurePlan(t, f, "verdict-plan", "env")
	acceptFuture(t, f, plan.ID)
	st := deployments.NewPgStore(f.db)
	ctx := t.Context()
	if _, err := st.CreateDeployment(ctx, &deployments.Deployment{ID: "dep_v", OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, Environment: "production", PlanID: plan.ID, Status: deployments.StatusRunning, StartedAt: time.Now(), StartedBy: "usr"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	verdict := func(org, id, reason string) *deployments.CanaryVerdict {
		return &deployments.CanaryVerdict{ID: id, OrgID: org, ProjectID: "forged", DeploymentID: "dep_v", ActionID: "service:api", Service: "api", Step: 1, Weight: 50,
			Verdict: releasehealth.Fail, Reason: reason, Thresholds: releasehealth.DefaultThresholds(), WindowStart: now.Add(-time.Minute), WindowEnd: now, DecidedAt: now}
	}
	first := verdict(plan.OrgID, "", "first")
	if err := st.RecordVerdict(ctx, first); err != nil {
		t.Fatal(err)
	}
	if first.ID != "dep_v/service:api/1/0" || first.ProjectID != plan.ProjectID {
		t.Fatalf("recorded as %q in project %q", first.ID, first.ProjectID)
	}
	if err := st.RecordVerdict(ctx, verdict(plan.OrgID, "another-id", "retried")); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordVerdict(ctx, verdict("other-org", "", "foreign")); !errors.Is(err, deployments.ErrNotFound) {
		t.Fatalf("a verdict for another org's deployment: %v", err)
	}
	vs, err := st.Verdicts(ctx, plan.OrgID, "dep_v")
	if err != nil || len(vs) != 1 || vs[0].Reason != "first" || vs[0].ProjectID != plan.ProjectID {
		t.Fatalf("verdicts: %+v %v", vs, err)
	}
	var rows int
	if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_canary_verdicts`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("verdict rows: %d %v", rows, err)
	}
	var org, project string
	if err = f.sql.QueryRow(ctx, `SELECT org_id, project_id FROM loomplane_events WHERE type = 'canary_verdict.updated'`).Scan(&org, &project); err != nil || org != plan.OrgID || project != plan.ProjectID {
		t.Fatalf("event scope %q %q: %v", org, project, err)
	}
}
