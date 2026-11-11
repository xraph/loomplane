package fleets

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

// EventDraft is internal producer input. IDs must remain stable across retries.
type EventDraft struct {
	ID          string                `json:"id"`
	InstanceID  string                `json:"instance_id"`
	OperationID string                `json:"operation_id,omitempty"`
	ReleaseID   string                `json:"release_id,omitempty"`
	Component   string                `json:"component,omitempty"`
	Generation  int64                 `json:"generation"`
	Kind        string                `json:"kind"`
	Lifecycle   *fleet.LifecycleEvent `json:"lifecycle,omitempty"`
	Scaling     *fleet.ScalingEvent   `json:"scaling,omitempty"`
	Release     *fleet.ReleaseEvent   `json:"release,omitempty"`
}

var eventIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_:-]{0,127}$`)
var eventComponentPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

func eventCursor(id string, seq int64) string {
	sum := sha256.Sum256([]byte(id))
	return base64.RawURLEncoding.EncodeToString([]byte("1:" + hex.EncodeToString(sum[:]) + ":" + strconv.FormatInt(seq, 10)))
}
func eventPageInput(id, after string, limit int) (sequence int64, pageSize int, inputErr error) {
	if limit < 0 || limit > 200 || len(after) > 160 {
		return 0, 0, ErrInvalid
	}
	if limit == 0 {
		limit = 50
	}
	if after == "" {
		return 0, limit, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(after)
	if err != nil {
		return 0, 0, ErrInvalid
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 3 || parts[0] != "1" {
		return 0, 0, ErrInvalid
	}
	seq, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || seq < 1 || eventCursor(id, seq) != after {
		return 0, 0, ErrInvalid
	}
	return seq, limit, nil
}
func validateEventDraft(d EventDraft) error {
	if !eventIDPattern.MatchString(d.ID) || !eventIDPattern.MatchString(d.InstanceID) || d.Generation < 1 {
		return ErrInvalid
	}
	for _, id := range []string{d.OperationID, d.ReleaseID} {
		if id != "" && !eventIDPattern.MatchString(id) {
			return ErrInvalid
		}
	}
	if d.Component != "" && !eventComponentPattern.MatchString(d.Component) {
		return ErrInvalid
	}
	count := 0
	if d.Lifecycle != nil {
		count++
	}
	if d.Scaling != nil {
		count++
	}
	if d.Release != nil {
		count++
	}
	if count != 1 {
		return ErrInvalid
	}
	switch d.Kind {
	case "lifecycle":
		e := d.Lifecycle
		if e == nil || !slices.Contains([]string{"accepted", "waiting_approval", "waiting_capacity", "queued", "running", "verifying", "ready", "failed", "blocked", "suspended", "resuming", "retiring", "retired", "deleted", "completed", "canceled", "provisioning", "degraded", "cancelled", "suspending", "retained", "deleting"}, e.State) || !slices.Contains([]string{"", "approval_required", "capacity_required", "qualification_required", "operation_failed", "access_revoked", "retention_active", "legal_hold", "health_unknown", "health_stale", "health_unhealthy", "generation_conflict", "dependency_unavailable", "request_accepted", "request_cancelled", "deadline_exceeded", "authority_revoked", "capability_unavailable", "child_failed", "stale_observation", "bootstrap_failed"}, e.Reason) {
			return ErrInvalid
		}
	case "scaling":
		e := d.Scaling
		if e == nil || !eventComponentPattern.MatchString(e.Service) || e.PolicyRevision < 1 || e.PreviousReplicas < 0 || e.DesiredReplicas < 0 || e.ObservedAt.IsZero() || !slices.Contains([]string{"manual", "hpa"}, e.Writer) || !slices.Contains([]string{"manual_requested", "automatic_restored", "metric_scale_up", "metric_scale_down", "within_bounds", "metric_unknown", "metric_stale", "cooldown", "stabilizing", "capacity_wait", "capacity_denied", "writer_transfer_pending", "qualification_required"}, e.Reason) {
			return ErrInvalid
		}
		if e.CloudID != "" && !eventIDPattern.MatchString(e.CloudID) {
			return ErrInvalid
		}
	case "release":
		e := d.Release
		if e != nil && (e.TargetGeneration < 0 || (e.TargetOperationID != "" && (!eventIDPattern.MatchString(e.TargetOperationID) || e.TargetGeneration < 1))) {
			return ErrInvalid
		}
		if e == nil || !eventIDPattern.MatchString(e.CampaignID) || e.Wave < 0 || !slices.Contains([]string{"pending", "launching", "running", "verifying", "succeeded", "failed", "paused"}, e.State) || !slices.Contains([]string{"", "health_unknown", "health_stale", "health_unhealthy", "generation_conflict", "operation_failed", "waiting_approval", "waiting_capacity", "compatibility_blocked", "authority_unavailable", "operator_paused", "operation_unknown", "unavailable_limit", "operation_conflict", "action_invalid"}, e.Reason) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// Events rechecks current permissions before replaying any part of an instance journal.
func (s *Service) Events(ctx context.Context, p security.Principal, id, after string, limit int) (*fleet.EventPage, error) {
	instance, err := s.GetInstance(ctx, p, id)
	if err != nil {
		return nil, err
	}
	seq, size, err := eventPageInput(id, after, limit)
	if err != nil {
		return nil, err
	}
	f, err := s.GetFleet(ctx, p, instance.FleetID)
	if err != nil {
		return nil, err
	}
	store, ok := s.store.(EventStore)
	if !ok {
		return nil, ErrInvalid
	}
	return store.ListEvents(ctx, FleetScope{OrgID: p.OrgID, ProjectID: f.ProjectID, FleetID: f.ID}, instance.ID, seq, size)
}
