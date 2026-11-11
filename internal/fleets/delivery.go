package fleets

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

// ErrEventDelivery reports a retryable acceptance failure without provider text.
var ErrEventDelivery = errors.New("fleet event delivery failed")

// FleetEventDelivery accepts an event into the existing durable notification system.
type FleetEventDelivery interface {
	DeliverFleetEvent(context.Context, string, fleet.Event) error
}

// EventDeliveryItem pins the committed event and the current outbox lease.
type EventDeliveryItem struct {
	OrgID, Lease string
	Event        fleet.Event
}

// EventDeliveryStore coordinates bounded acceptance retries. Relay owns HTTP attempts.
type EventDeliveryStore interface {
	PendingEventDeliveries(context.Context, int) ([]string, error)
	ClaimEventDelivery(context.Context, string) (*EventDeliveryItem, error)
	FinishEventDelivery(context.Context, string, string, bool, string) error
}

// DeliverEvent submits one durable event. A lost acknowledgement is safe to retry.
func (s *Service) DeliverEvent(ctx context.Context, id string) error {
	if s.delivery == nil {
		return ErrEventDelivery
	}
	store, ok := s.store.(EventDeliveryStore)
	if !ok {
		return ErrEventDelivery
	}
	item, err := store.ClaimEventDelivery(ctx, id)
	if err != nil {
		return err
	}
	if item == nil {
		return nil
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	err = s.delivery.DeliverFleetEvent(callCtx, item.OrgID, item.Event)
	cancel()
	reason := ""
	if err != nil {
		reason = "acceptance_failed"
	}
	if saveErr := store.FinishEventDelivery(ctx, id, item.Lease, err == nil, reason); saveErr != nil {
		return saveErr
	}
	if err != nil {
		return ErrEventDelivery
	}
	return nil
}

// DrainEventDeliveries reconciles at most ten pending acceptance attempts per poll.
func (s *Service) DrainEventDeliveries(ctx context.Context) error {
	if s.delivery == nil {
		return nil
	}
	store, ok := s.store.(EventDeliveryStore)
	if !ok {
		return nil
	}
	ids, err := store.PendingEventDeliveries(ctx, 10)
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = s.DeliverEvent(ctx, id); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *PgStore) PendingEventDeliveries(ctx context.Context, limit int) ([]string, error) {
	if limit < 1 || limit > 10 {
		return nil, ErrInvalid
	}
	if _, err := s.db.Exec(ctx, `UPDATE loomplane_fleet_events SET delivery_state='dead_letter',delivery_error='lease_expired',delivery_lease='',delivery_lease_until=NULL WHERE id IN (SELECT id FROM loomplane_fleet_events WHERE delivery_state='pending' AND delivery_attempts=10 AND (delivery_lease_until IS NULL OR delivery_lease_until<clock_timestamp()) ORDER BY next_delivery_at,id LIMIT 100 FOR UPDATE SKIP LOCKED)`); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT id FROM loomplane_fleet_events WHERE delivery_state='pending' AND payload IS NOT NULL AND delivery_attempts<10 AND next_delivery_at<=clock_timestamp() AND (delivery_lease_until IS NULL OR delivery_lease_until<clock_timestamp()) ORDER BY next_delivery_at,id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if checkErr := rows.Scan(&id); checkErr != nil {
			return nil, checkErr
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s *PgStore) ClaimEventDelivery(ctx context.Context, id string) (*EventDeliveryItem, error) {
	item := &EventDeliveryItem{Lease: uuid.NewString()}
	var payload []byte
	err := s.db.QueryRow(ctx, `UPDATE loomplane_fleet_events SET delivery_attempts=delivery_attempts+1,delivery_lease=$2,delivery_lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1 AND delivery_state='pending' AND payload IS NOT NULL AND delivery_attempts<10 AND next_delivery_at<=clock_timestamp() AND (delivery_lease_until IS NULL OR delivery_lease_until<clock_timestamp()) RETURNING org_id,payload`, id, item.Lease).Scan(&item.OrgID, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(payload, &item.Event); err != nil {
		return nil, ErrInvalid
	}
	return item, nil
}
func (s *PgStore) FinishEventDelivery(ctx context.Context, id, lease string, accepted bool, reason string) error {
	if (!accepted && reason != "acceptance_failed") || (accepted && reason != "") {
		return ErrInvalid
	}
	result, err := s.db.Exec(ctx, `UPDATE loomplane_fleet_events SET delivery_state=CASE WHEN $3 THEN 'accepted' WHEN delivery_attempts>=10 THEN 'dead_letter' ELSE 'pending' END,delivery_error=$4,delivery_lease='',delivery_lease_until=NULL,next_delivery_at=clock_timestamp()+least(3600,power(2,delivery_attempts)::int)*interval '1 second' WHERE id=$1 AND delivery_lease=$2 AND delivery_state='pending' AND delivery_lease_until>clock_timestamp()`, id, lease, accepted, reason)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

// RetryEventDelivery restarts acceptance of a retained dead letter after an operator fixes its receiver.
func (s *Service) RetryEventDelivery(ctx context.Context, p security.Principal, instanceID, eventID string) error {
	if p.IsServiceAccount() || p.KeyID != "" || p.ReadOnly {
		return security.ErrForbidden
	}
	instance, err := s.GetInstance(ctx, p, instanceID)
	if err != nil {
		return err
	}
	f, err := s.GetFleet(ctx, p, instance.FleetID)
	if err != nil {
		return err
	}
	if checkErr := s.authz.Require(ctx, p, security.MemberManage, ""); checkErr != nil {
		return checkErr
	}
	if checkErr := s.authz.Require(ctx, p, security.ProjectUpdate, f.ProjectID); checkErr != nil {
		return checkErr
	}
	store, ok := s.store.(interface {
		RetryEventAcceptance(context.Context, FleetScope, string, string) error
	})
	if !ok {
		return ErrInvalid
	}
	if checkErr := store.RetryEventAcceptance(ctx, FleetScope{OrgID: p.OrgID, ProjectID: f.ProjectID, FleetID: f.ID}, instance.ID, eventID); checkErr != nil {
		return checkErr
	}
	s.record(ctx, p, f.ProjectID, "fleet.event.retry", eventID)
	return nil
}

func (s *PgStore) RetryEventAcceptance(ctx context.Context, scope FleetScope, instanceID, eventID string) error {
	result, err := s.db.Exec(ctx, `UPDATE loomplane_fleet_events SET delivery_state='pending',delivery_attempts=0,delivery_error='',delivery_lease='',delivery_lease_until=NULL,next_delivery_at=clock_timestamp() WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND instance_id=$4 AND id=$5 AND delivery_state='dead_letter' AND payload IS NOT NULL`, scope.OrgID, scope.ProjectID, scope.FleetID, instanceID, eventID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

// PruneEventHistory applies the operator retention cutoff to detailed journal payloads.
func (s *Service) PruneEventHistory(ctx context.Context, before time.Time) error {
	store, ok := s.store.(interface {
		PruneEvents(context.Context, time.Time, int) (int64, error)
	})
	if !ok {
		return ErrInvalid
	}
	_, err := store.PruneEvents(ctx, before, 1000)
	return err
}
