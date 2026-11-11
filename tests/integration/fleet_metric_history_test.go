//go:build integration

package integration

import (
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

func metricFixtureSnapshot(t *testing.T, c *fleets.MetricClaim) *fleet.MetricSnapshot {
	t.Helper()
	now := time.Now().UTC()
	summary, err := fleets.AggregateMetrics(fleet.MetricWindow{Start: now.Add(-5 * time.Minute), End: now}, now, fleets.MetricBatch{Expected: 1, Samples: []fleets.MetricSample{{Component: "api", Reason: "source_unavailable"}}})
	if err != nil {
		t.Fatal(err)
	}
	return &fleet.MetricSnapshot{InstanceID: c.Scope.InstanceID, Hour: now.Truncate(time.Hour), CapturedAt: now, Generation: c.Generation, OwnershipEpoch: c.OwnershipEpoch, Semantics: "snapshot", Summary: *summary}
}
func TestFleetMetricHistoryClaimFenceAndPagination(t *testing.T) {
	f := newFleetFixture(t, 2)
	first, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("first"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.store.ClaimMetricSample(t.Context())
	if err != nil || c == nil || c.Scope.InstanceID != first.Instance.ID {
		t.Fatalf("claim %+v %v", c, err)
	}
	duplicate, err := f.store.ClaimMetricSample(t.Context())
	if err != nil || duplicate != nil {
		t.Fatalf("duplicate %+v %v", duplicate, err)
	}
	h := metricFixtureSnapshot(t, c)
	if err = f.store.FinishMetricSample(t.Context(), *c, h); err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishMetricSample(t.Context(), *c, h); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("late completion %v", err)
	}

	if _, err = f.db.Exec(t.Context(), `UPDATE loomplane_fleet_metric_schedule SET next_at=clock_timestamp() WHERE instance_id=$1`, first.Instance.ID); err != nil {
		t.Fatal(err)
	}
	sameHour, err := f.store.ClaimMetricSample(t.Context())
	if err != nil || sameHour == nil {
		t.Fatalf("same-hour claim %+v %v", sameHour, err)
	}
	replacement := *h
	replacement.CapturedAt = h.CapturedAt.Add(time.Microsecond)
	replacement.Summary.AsOf = replacement.CapturedAt
	if err = f.store.FinishMetricSample(t.Context(), *sameHour, &replacement); err != nil {
		t.Fatal(err)
	}
	var snapshots int
	if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_fleet_metric_history WHERE instance_id=$1`, first.Instance.ID).Scan(&snapshots); err != nil || snapshots != 1 {
		t.Fatalf("same-hour snapshots=%d error=%v", snapshots, err)
	}
	p := security.Principal{OrgID: "org", UserID: "usr"}
	page, err := f.service.MetricHistory(t.Context(), p, first.Instance.ID, "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].Summary.CPUUtilization != nil || page.Items[0].Semantics != "snapshot" {
		t.Fatalf("page %+v %v", page, err)
	}
	empty, err := f.service.MetricHistory(t.Context(), p, first.Instance.ID, page.NextCursor, 1)
	if err != nil || len(empty.Items) != 0 {
		t.Fatalf("continuation %+v %v", empty, err)
	}
	second, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("second"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.MetricHistory(t.Context(), p, second.Instance.ID, page.NextCursor, 1); !errors.Is(err, fleets.ErrInvalid) {
		t.Fatalf("foreign cursor %v", err)
	}
	if _, err = f.service.MetricHistory(t.Context(), security.Principal{OrgID: "other", UserID: "usr"}, first.Instance.ID, "", 1); !errors.Is(err, fleets.ErrNotFound) {
		t.Fatalf("scope %v", err)
	}
}
func TestFleetMetricHistoryLeaseRecoveryGenerationAndRetention(t *testing.T) {
	f := newFleetFixture(t, 1)
	result, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("first"))
	if err != nil {
		t.Fatal(err)
	}
	old, err := f.store.ClaimMetricSample(t.Context())
	if err != nil || old == nil {
		t.Fatalf("claim %+v %v", old, err)
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE loomplane_fleet_metric_schedule SET lease_until=clock_timestamp()-interval '1 second' WHERE instance_id=$1`, result.Instance.ID); err != nil {
		t.Fatal(err)
	}
	next, err := f.store.ClaimMetricSample(t.Context())
	if err != nil || next == nil || next.Lease == old.Lease {
		t.Fatalf("recovery %+v %v", next, err)
	}
	if err = f.store.FinishMetricSample(t.Context(), *old, metricFixtureSnapshot(t, old)); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("late worker %v", err)
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE loomplane_fleet_instances SET desired_generation=desired_generation+1 WHERE id=$1`, result.Instance.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishMetricSample(t.Context(), *next, metricFixtureSnapshot(t, next)); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("generation fence %v", err)
	}
	if err = f.store.FailMetricSample(t.Context(), *next); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE loomplane_fleet_metric_schedule SET next_at=clock_timestamp() WHERE instance_id=$1`, result.Instance.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := f.store.ClaimMetricSample(t.Context())
	if err != nil || fresh == nil {
		t.Fatalf("fresh %+v %v", fresh, err)
	}

	if _, err = f.db.Exec(t.Context(), `UPDATE loomplane_fleet_instances SET ownership_epoch=ownership_epoch+1 WHERE id=$1`, result.Instance.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishMetricSample(t.Context(), *fresh, metricFixtureSnapshot(t, fresh)); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("ownership epoch fence %v", err)
	}
	if err = f.store.FailMetricSample(t.Context(), *fresh); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE loomplane_fleet_metric_schedule SET next_at=clock_timestamp() WHERE instance_id=$1`, result.Instance.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err = f.store.ClaimMetricSample(t.Context())
	if err != nil || fresh == nil || fresh.OwnershipEpoch != 2 {
		t.Fatalf("new epoch claim %+v %v", fresh, err)
	}
	if err = f.store.FinishMetricSample(t.Context(), *fresh, metricFixtureSnapshot(t, fresh)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE loomplane_fleet_metric_history SET hour=hour-interval '91 days',captured_at=captured_at-interval '91 days' WHERE instance_id=$1`, result.Instance.ID); err != nil {
		t.Fatal(err)
	}
	removed, err := f.store.PruneMetricHistory(t.Context(), time.Now().Add(-90*24*time.Hour), 100)
	if err != nil || removed != 1 {
		t.Fatalf("prune %d %v", removed, err)
	}
}
