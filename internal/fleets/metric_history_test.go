package fleets

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/fleet"
)

func TestFleetMetricHistoryCursorScopeAndBounds(t *testing.T) {
	hour := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	cursor := metricHistoryCursor("instance", hour)
	got, limit, err := metricHistoryPageInput("instance", cursor, 0)
	if err != nil || !got.Equal(hour) || limit != 50 {
		t.Fatalf("cursor %v %d %v", got, limit, err)
	}
	for _, test := range []struct {
		id, cursor string
		limit      int
	}{{"other", cursor, 1}, {"instance", cursor + "x", 1}, {"instance", "", 201}, {"instance", "", -1}} {
		if _, _, err := metricHistoryPageInput(test.id, test.cursor, test.limit); err == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
}

type samplerStore struct {
	claim            *MetricClaim
	finished, failed int
	history          *fleet.MetricSnapshot
}

func (s *samplerStore) ClaimMetricSample(context.Context) (*MetricClaim, error) { return s.claim, nil }
func (s *samplerStore) FinishMetricSample(_ context.Context, _ MetricClaim, h *fleet.MetricSnapshot) error {
	s.finished++
	s.history = h
	return nil
}
func (s *samplerStore) FailMetricSample(context.Context, MetricClaim) error { s.failed++; return nil }
func (s *samplerStore) PruneMetricHistory(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

type samplerSource struct {
	batch MetricBatch
	err   error
	scope MetricScope
}

func (s *samplerSource) ReadMetrics(_ context.Context, scope MetricScope, _ fleet.MetricWindow) (MetricBatch, error) {
	s.scope = scope
	return s.batch, s.err
}
func TestFleetMetricSamplerPreservesUnknownAndActualSnapshot(t *testing.T) {
	now := time.Now().UTC()
	claim := &MetricClaim{Scope: MetricScope{OrgID: "org", FleetID: "fleet", InstanceID: "instance", EnvironmentID: "environment"}, Generation: 2, OwnershipEpoch: 3, Lease: "lease"}
	store := &samplerStore{claim: claim}
	source := &samplerSource{batch: MetricBatch{Expected: 1, Samples: []MetricSample{{Component: "api", Reason: "source_unavailable"}}}}
	sampler := NewMetricSampler(store, source, 90)
	sampler.now = func() time.Time { return now }
	if err := sampler.SampleNext(t.Context()); err != nil {
		t.Fatal(err)
	}
	h := store.history
	if store.finished != 1 || store.failed != 0 || h == nil || h.Generation != 2 || h.OwnershipEpoch != 3 || h.Semantics != "snapshot" || !h.CapturedAt.Equal(now) || !h.Hour.Equal(now.Truncate(time.Hour)) || h.Summary.CPUUsageMillicores != nil || !h.Summary.Partial || source.scope != claim.Scope {
		t.Fatalf("snapshot %+v", h)
	}
}
func TestFleetMetricSamplerFailureAdvancesSchedule(t *testing.T) {
	store := &samplerStore{claim: &MetricClaim{Scope: MetricScope{OrgID: "org", FleetID: "fleet", InstanceID: "instance", EnvironmentID: "environment"}, Generation: 1, OwnershipEpoch: 1, Lease: "lease"}}
	source := &samplerSource{err: errors.New("synthetic source failure")}
	sampler := NewMetricSampler(store, source, 90)
	if err := sampler.SampleNext(t.Context()); err == nil {
		t.Fatal("failure hidden")
	}
	if store.failed != 1 || store.finished != 0 {
		t.Fatal("failed source was not rescheduled")
	}
}

type retentionPageStore struct {
	samplerStore
	counts []int64
}

func (s *retentionPageStore) PruneMetricHistory(context.Context, time.Time, int) (int64, error) {
	n := s.counts[0]
	s.counts = s.counts[1:]
	return n, nil
}
func TestFleetMetricRetentionDrainsBoundedPages(t *testing.T) {
	store := &retentionPageStore{counts: []int64{1000, 1000, 17}}
	sampler := NewMetricSampler(store, &samplerSource{}, 90)
	for i, want := range []bool{false, false, true} {
		done, err := sampler.PrunePage(t.Context())
		if err != nil || done != want {
			t.Fatalf("page %d done=%v error=%v", i, done, err)
		}
	}
	if len(store.counts) != 0 {
		t.Fatal("retention pages were skipped")
	}
}
