package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/xraph/grove/driver"
	"github.com/xraph/relay/dlq"

	"github.com/xraph/relay/delivery"
	relayid "github.com/xraph/relay/id"
)

// ErrFleetDeliveryLease rejects a completion from an expired delivery claim.
var ErrFleetDeliveryLease = errors.New("fleet callback claim expired")

// Dequeue uses Relay's queue and sender. Fleet rows gain recoverable two-minute
// claims; ordinary notification rows keep Relay's existing dequeue behavior.
func (s *encryptedRelayStore) Dequeue(ctx context.Context, limit int) ([]*delivery.Delivery, error) {
	if s.fleetPG == nil {
		return s.Store.Dequeue(ctx, limit)
	}
	if limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	rows, err := s.fleetPG.Query(ctx, `UPDATE relay_deliveries d SET
 attempt_count=CASE WHEN d.state='delivering' THEN d.attempt_count+1 ELSE d.attempt_count END,
 state=CASE WHEN d.state='delivering' AND d.attempt_count+1>=d.max_attempts THEN 'failed' ELSE 'delivering' END,
 completed_at=CASE WHEN d.state='delivering' AND d.attempt_count+1>=d.max_attempts THEN clock_timestamp() ELSE d.completed_at END,
 last_error=CASE WHEN d.state='delivering' THEN 'delivery interrupted' ELSE d.last_error END,
 updated_at=greatest(clock_timestamp(),d.updated_at+interval '1 microsecond')
 WHERE d.id IN (SELECT q.id FROM relay_deliveries q WHERE (q.state='pending' AND q.next_attempt_at<=clock_timestamp()) OR (q.tenant_id LIKE 'fleet:%' AND q.state='delivering' AND q.updated_at<clock_timestamp()-interval '2 minutes') ORDER BY q.next_attempt_at,q.id LIMIT $1 FOR UPDATE SKIP LOCKED)
 RETURNING d.id,d.state,d.updated_at`, limit)
	if err != nil {
		return nil, err
	}
	type claim struct {
		id, state string
		at        time.Time
	}
	claims := make([]claim, 0, limit)
	for rows.Next() {
		var c claim
		if err = rows.Scan(&c.id, &c.state, &c.at); err != nil {
			rows.Close()
			return nil, err
		}
		claims = append(claims, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]*delivery.Delivery, 0, len(claims))
	for _, c := range claims {
		if c.state != "delivering" {
			continue
		}
		parsed, parseErr := relayid.ParseDeliveryID(c.id)
		if parseErr != nil {
			return nil, ErrInvalid
		}
		d, getErr := s.GetDelivery(ctx, parsed)
		if getErr != nil {
			return nil, getErr
		}
		// The exact returned database token travels on this delivery object.
		if !d.UpdatedAt.Equal(c.at) || d.State != delivery.StateDelivering {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *encryptedRelayStore) finishFleetDelivery(ctx context.Context, d *delivery.Delivery) error {
	if s.fleetPG == nil {
		return s.Store.UpdateDelivery(ctx, d)
	}
	result, err := s.fleetPG.Exec(ctx, `UPDATE relay_deliveries SET state=$3,attempt_count=$4,next_attempt_at=$5,last_error=$6,last_status_code=$7,last_response='',last_latency_ms=$8,completed_at=$9,updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$1 AND updated_at=$2 AND state='delivering' AND updated_at>=clock_timestamp()-interval '2 minutes'`, d.ID.String(), d.UpdatedAt, string(d.State), d.AttemptCount, d.NextAttemptAt, d.LastError, d.LastStatusCode, d.LastLatencyMs, d.CompletedAt)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrFleetDeliveryLease
	}
	return nil
}

// Record writes share retention's event-before-delivery lock order. The claim
// check and insert commit together, so pruning cannot be followed by a stale copy.
func (s *encryptedRelayStore) withFleetAttempt(ctx context.Context, d *delivery.Delivery, attempt int, write func(driver.Tx, json.RawMessage) error) (err error) {
	tx, err := s.fleetPG.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				err = errors.Join(err, rollbackErr)
			}
		}
	}()
	var payload json.RawMessage
	if err = tx.QueryRow(ctx, `SELECT data FROM relay_events WHERE id=$1 AND tenant_id=$2 AND data->>'retained' IS DISTINCT FROM 'true' FOR UPDATE`, d.EventID.String(), d.TenantID).Scan(&payload); err != nil {
		return ErrFleetDeliveryLease
	}
	var current bool
	if checkErr := tx.QueryRow(ctx, `SELECT state='delivering' AND attempt_count+1=$2 AND updated_at>=clock_timestamp()-interval '2 minutes' FROM relay_deliveries WHERE id=$1 AND event_id=$3 AND tenant_id=$4 FOR UPDATE`, d.ID.String(), attempt, d.EventID.String(), d.TenantID).Scan(&current); checkErr != nil {
		return checkErr
	}
	if !current {
		return ErrFleetDeliveryLease
	}
	if checkErr := write(tx, payload); checkErr != nil {
		return checkErr
	}
	return tx.Commit()
}

func (s *encryptedRelayStore) recordFleetAttempt(ctx context.Context, d *delivery.Delivery, a *delivery.Attempt) error {
	if s.fleetPG == nil {
		return s.Store.RecordAttempt(ctx, a)
	}
	return s.withFleetAttempt(ctx, d, a.AttemptNum, func(tx driver.Tx, _ json.RawMessage) error {
		_, err := tx.Exec(ctx, `INSERT INTO relay_delivery_attempts (id,delivery_id,attempt_num,status_code,error,response,latency_ms,outcome,next_attempt_at,attempted_at) VALUES ($1,$2,$3,$4,$5,'',$6,$7,$8,$9)`, a.ID.String(), a.DeliveryID.String(), a.AttemptNum, a.StatusCode, a.Error, a.LatencyMs, string(a.Outcome), a.NextAttemptAt, a.AttemptedAt)
		return err
	})
}
func (s *encryptedRelayStore) pushFleetDeadLetter(ctx context.Context, e *dlq.Entry) error {
	d, err := s.GetDelivery(ctx, e.DeliveryID)
	if err != nil {
		return err
	}
	if d.EventID != e.EventID || d.EndpointID != e.EndpointID || d.TenantID != e.TenantID {
		return ErrInvalid
	}
	return s.withFleetAttempt(ctx, d, e.AttemptCount, func(tx driver.Tx, payload json.RawMessage) error {
		_, err := tx.Exec(ctx, `INSERT INTO relay_dlq (id,delivery_id,event_id,endpoint_id,tenant_id,event_type,url,payload,error,attempt_count,last_status_code,replayed_at,failed_at,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,'',$7,$8,$9,$10,$11,$12,clock_timestamp(),clock_timestamp())`, e.ID.String(), d.ID.String(), d.EventID.String(), d.EndpointID.String(), d.TenantID, d.EventType, payload, e.Error, e.AttemptCount, e.LastStatusCode, e.ReplayedAt, e.FailedAt)
		return err
	})
}
