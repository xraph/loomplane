package fleets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/pkg/fleet"
)

// AppendEventTx records a decision in its mutation transaction. It never sends callbacks.
// Lock the instance before other event rows when composing this with a mutation.
func AppendEventTx(ctx context.Context, tx driver.Tx, scope FleetScope, d EventDraft) (*fleet.Event, error) {
	if err := validateEventDraft(d); err != nil {
		return nil, err
	}
	if scope.OrgID == "" || scope.ProjectID == "" || scope.FleetID == "" {
		return nil, ErrInvalid
	}
	canonical, err := json.Marshal(d)
	if err != nil {
		return nil, ErrInvalid
	}
	sum := sha256.Sum256(canonical)
	fingerprint := hex.EncodeToString(sum[:])
	var environment string
	var desired int64
	if err = tx.QueryRow(ctx, `SELECT environment_id,desired_generation FROM loomplane_fleet_instances WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND id=$4 FOR UPDATE`, scope.OrgID, scope.ProjectID, scope.FleetID, d.InstanceID).Scan(&environment, &desired); err != nil {
		return nil, storeError(err)
	}
	event := &fleet.Event{Version: 1, ID: d.ID, OrgID: scope.OrgID, FleetID: scope.FleetID, InstanceID: d.InstanceID, EnvironmentID: environment, OperationID: d.OperationID, ReleaseID: d.ReleaseID, Component: d.Component, Generation: d.Generation, Kind: d.Kind, Lifecycle: d.Lifecycle, Scaling: d.Scaling, Release: d.Release}
	var savedFingerprint string
	err = tx.QueryRow(ctx, `SELECT sequence,occurred_at,payload_hash FROM loomplane_fleet_events WHERE org_id=$1 AND fleet_id=$2 AND instance_id=$3 AND id=$4`, scope.OrgID, scope.FleetID, d.InstanceID, d.ID).Scan(&event.Sequence, &event.OccurredAt, &savedFingerprint)
	if err == nil {
		if savedFingerprint != fingerprint {
			return nil, ErrConflict
		}
		event.OccurredAt = event.OccurredAt.UTC()
		return event, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if d.OperationID != "" {
		var operation string
		if err = tx.QueryRow(ctx, `SELECT id FROM loomplane_fleet_operations WHERE org_id=$1 AND fleet_id=$2 AND instance_id=$3 AND id=$4 AND generation=$5`, scope.OrgID, scope.FleetID, d.InstanceID, d.OperationID, d.Generation).Scan(&operation); err != nil {
			return nil, storeError(err)
		}
	}
	if d.Release != nil && d.Release.TargetOperationID != "" {
		var targetOperation string
		if err = tx.QueryRow(ctx, `SELECT id FROM loomplane_fleet_operations WHERE org_id=$1 AND fleet_id=$2 AND instance_id=$3 AND id=$4 AND generation=$5`, scope.OrgID, scope.FleetID, d.InstanceID, d.Release.TargetOperationID, d.Release.TargetGeneration).Scan(&targetOperation); err != nil {
			return nil, storeError(err)
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_event_heads(org_id,fleet_id,instance_id,sequence,generation) VALUES($1,$2,$3,0,0) ON CONFLICT DO NOTHING`, scope.OrgID, scope.FleetID, d.InstanceID); err != nil {
		return nil, err
	}
	var last, generation int64
	if checkErr := tx.QueryRow(ctx, `SELECT sequence,generation FROM loomplane_fleet_event_heads WHERE org_id=$1 AND instance_id=$2 FOR UPDATE`, scope.OrgID, d.InstanceID).Scan(&last, &generation); checkErr != nil {
		return nil, checkErr
	}
	if d.Generation < generation || d.Generation != desired || last == math.MaxInt64 {
		return nil, ErrConflict
	}
	event.Sequence = last + 1
	if checkErr := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&event.OccurredAt); checkErr != nil {
		return nil, checkErr
	}
	event.OccurredAt = event.OccurredAt.UTC()
	payload, err := json.Marshal(event)
	if err != nil {
		return nil, ErrInvalid
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_events(id,org_id,project_id,fleet_id,instance_id,sequence,generation,occurred_at,payload_hash,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, d.ID, scope.OrgID, scope.ProjectID, scope.FleetID, d.InstanceID, event.Sequence, d.Generation, event.OccurredAt, fingerprint, payload); err != nil {
		return nil, storeError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_fleet_event_heads SET sequence=$3,generation=$4 WHERE org_id=$1 AND instance_id=$2`, scope.OrgID, d.InstanceID, event.Sequence, d.Generation); err != nil {
		return nil, err
	}
	return event, nil
}

// EventStore loads committed history inside an authorized scope.
type EventStore interface {
	ListEvents(context.Context, FleetScope, string, int64, int) (*fleet.EventPage, error)
}

// ListEvents returns only the requested instance's sequence range.
func (s *PgStore) ListEvents(ctx context.Context, scope FleetScope, instance string, after int64, limit int) (*fleet.EventPage, error) {
	if after < 0 || limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	rows, err := s.db.Query(ctx, `SELECT e.payload,coalesce(e.delivery_state,''),coalesce(e.delivery_attempts,0),coalesce(e.delivery_error,''),h.latest FROM (SELECT coalesce(max(sequence),0) AS latest FROM loomplane_fleet_events WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND instance_id=$4) h LEFT JOIN LATERAL (SELECT payload,delivery_state,delivery_attempts,delivery_error,sequence FROM loomplane_fleet_events WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND instance_id=$4 AND sequence>$5 AND payload IS NOT NULL ORDER BY sequence LIMIT $6) e ON true ORDER BY e.sequence`, scope.OrgID, scope.ProjectID, scope.FleetID, instance, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &fleet.EventPage{Items: make([]fleet.Event, 0, limit), Acceptance: make([]fleet.EventAcceptance, 0, limit)}
	var latest int64
	for rows.Next() {
		var payload []byte
		var acceptance fleet.EventAcceptance
		if scanErr := rows.Scan(&payload, &acceptance.State, &acceptance.Attempts, &acceptance.ErrorCode, &latest); scanErr != nil {
			return nil, scanErr
		}
		if len(payload) == 0 {
			continue
		}
		var event fleet.Event
		if decodeErr := json.Unmarshal(payload, &event); decodeErr != nil {
			return nil, ErrInvalid
		}
		page.Items = append(page.Items, event)
		acceptance.EventID = event.ID
		page.Acceptance = append(page.Acceptance, acceptance)
	}
	if checkErr := rows.Err(); checkErr != nil {
		return nil, checkErr
	}
	previous := after
	for _, event := range page.Items {
		if event.Sequence-previous > 1 {
			page.ReplayTruncated = true
		}
		previous = event.Sequence
	}
	if len(page.Items) > 0 {
		page.NextCursor = eventCursor(instance, previous)
	} else if latest > after {
		page.ReplayTruncated = true
		page.NextCursor = eventCursor(instance, latest)
	}
	return page, nil
}

// PruneEvents removes detailed payloads after operator-selected retention. Receipts
// remain so replaying a producer ID cannot create a second event after retention.
func (s *PgStore) PruneEvents(ctx context.Context, before time.Time, limit int) (int64, error) {
	if before.IsZero() || before.After(time.Now().Add(-24*time.Hour)) || limit < 1 || limit > 1000 {
		return 0, ErrInvalid
	}
	result, err := s.db.Exec(ctx, `UPDATE loomplane_fleet_events SET payload=NULL,delivery_state=CASE WHEN delivery_state='pending' THEN 'dead_letter' ELSE delivery_state END,delivery_error=CASE WHEN delivery_state='pending' THEN 'event_expired' ELSE delivery_error END,delivery_lease='',delivery_lease_until=NULL WHERE id IN (SELECT id FROM loomplane_fleet_events WHERE occurred_at<$1 AND payload IS NOT NULL ORDER BY occurred_at,id LIMIT $2 FOR UPDATE SKIP LOCKED)`, before, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
