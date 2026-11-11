package fleets

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type MetricClaim struct {
	Scope                      MetricScope
	Generation, OwnershipEpoch int64
	Lease                      string
}
type MetricSampleStore interface {
	ClaimMetricSample(context.Context) (*MetricClaim, error)
	FinishMetricSample(context.Context, MetricClaim, *fleet.MetricSnapshot) error
	FailMetricSample(context.Context, MetricClaim) error
	PruneMetricHistory(context.Context, time.Time, int) (int64, error)
}
type MetricSampler struct {
	store         MetricSampleStore
	source        MetricSource
	retentionDays int
	now           func() time.Time
}

func NewMetricSampler(store MetricSampleStore, source MetricSource, retentionDays int) *MetricSampler {
	return &MetricSampler{store: store, source: source, retentionDays: retentionDays, now: time.Now}
}

// SampleNext claims at most one instance and preserves missing data as unknown.
func (s *MetricSampler) SampleNext(ctx context.Context) error {
	if s == nil || s.store == nil || s.source == nil || s.now == nil || s.retentionDays < 0 || s.retentionDays > 3650 {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	claim, err := s.store.ClaimMetricSample(ctx)
	if err != nil || claim == nil {
		return err
	}
	failed := func(cause error) error { return errors.Join(cause, s.store.FailMetricSample(ctx, *claim)) }
	if claim.Scope.OrgID == "" || claim.Scope.FleetID == "" || claim.Scope.InstanceID == "" || claim.Scope.EnvironmentID == "" || claim.Generation < 1 || claim.OwnershipEpoch < 1 || claim.Lease == "" {
		return failed(ErrInvalid)
	}
	now := s.now().UTC()
	window := fleet.MetricWindow{Start: now.Add(-5 * time.Minute), End: now}
	batch, err := s.source.ReadMetrics(ctx, claim.Scope, window)
	if err != nil {
		return failed(errors.New("fleet metric sample unavailable"))
	}
	captured := s.now().UTC()
	summary, err := AggregateMetrics(window, captured, batch)
	if err != nil {
		return failed(ErrInvalid)
	}
	snapshot := &fleet.MetricSnapshot{InstanceID: claim.Scope.InstanceID, Hour: captured.Truncate(time.Hour), CapturedAt: captured, Generation: claim.Generation, OwnershipEpoch: claim.OwnershipEpoch, Semantics: "snapshot", Summary: *summary}
	if err = s.store.FinishMetricSample(ctx, *claim, snapshot); err != nil {
		return failed(err)
	}
	return nil
}
func (s *MetricSampler) Prune(ctx context.Context) error { _, err := s.PrunePage(ctx); return err }
func metricHistoryCursor(id string, hour time.Time) string {
	sum := sha256.Sum256([]byte(id))
	return base64.RawURLEncoding.EncodeToString([]byte("1:" + hex.EncodeToString(sum[:]) + ":" + strconv.FormatInt(hour.Unix(), 10)))
}
func metricHistoryPageInput(id, after string, limit int) (time.Time, int, error) {
	if id == "" || len(after) > 160 || limit < 0 || limit > 200 {
		return time.Time{}, 0, ErrInvalid
	}
	if limit == 0 {
		limit = 50
	}
	if after == "" {
		return time.Time{}, limit, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(after)
	if err != nil {
		return time.Time{}, 0, ErrInvalid
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 3 || parts[0] != "1" {
		return time.Time{}, 0, ErrInvalid
	}
	seconds, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || seconds < 0 || seconds > 253402300799 || seconds%3600 != 0 {
		return time.Time{}, 0, ErrInvalid
	}
	hour := time.Unix(seconds, 0).UTC()
	if metricHistoryCursor(id, hour) != after {
		return time.Time{}, 0, ErrInvalid
	}
	return hour, limit, nil
}

type MetricHistoryStore interface {
	ListMetricHistory(context.Context, FleetScope, string, time.Time, int) (*fleet.MetricHistoryPage, error)
}

// MetricHistory rechecks current access before returning retained observations.
func (s *Service) MetricHistory(ctx context.Context, p security.Principal, id, after string, limit int) (*fleet.MetricHistoryPage, error) {
	instance, err := s.GetInstance(ctx, p, id)
	if err != nil {
		return nil, err
	}
	before, size, err := metricHistoryPageInput(id, after, limit)
	if err != nil {
		return nil, err
	}
	f, err := s.GetFleet(ctx, p, instance.FleetID)
	if err != nil {
		return nil, err
	}
	store, ok := s.store.(MetricHistoryStore)
	if !ok {
		return nil, ErrInvalid
	}
	return store.ListMetricHistory(ctx, FleetScope{OrgID: p.OrgID, ProjectID: f.ProjectID, FleetID: f.ID}, id, before, size)
}

// PrunePage reports whether its bounded page exhausted the retention backlog.
func (s *MetricSampler) PrunePage(ctx context.Context) (bool, error) {
	if s == nil || s.store == nil || s.now == nil || s.retentionDays < 0 || s.retentionDays > 3650 {
		return false, ErrInvalid
	}
	days := s.retentionDays
	if days == 0 {
		days = 90
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	n, err := s.store.PruneMetricHistory(ctx, s.now().UTC().Add(-time.Duration(days)*24*time.Hour), 1000)
	return n < 1000 && err == nil, err
}
