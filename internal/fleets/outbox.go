package fleets

import (
	"context"
	"errors"
)

// OperationDispatch identifies committed work. It contains no request secrets.
type OperationDispatch struct{ OrgID, OperationID string }

type OperationOutbox interface {
	PendingOperations(context.Context, int) ([]OperationDispatch, error)
	MarkOperationDispatched(context.Context, string, string) error
	DeferOperationDispatch(context.Context, string, string) error
}

// OperationDispatcher must deduplicate workflow starts by operation ID.
type OperationDispatcher interface {
	RunFleetInstance(context.Context, string) error
}

// DispatchPendingOperations retries unknown start outcomes with the same ID.
// An item stays pending until both Dispatch and the acknowledgement succeed.
func (s *Service) DispatchPendingOperations(ctx context.Context, workflow OperationDispatcher, limit int) error {
	if workflow == nil || limit < 1 || limit > 100 {
		return ErrInvalid
	}
	store, ok := s.store.(OperationOutbox)
	if !ok {
		return ErrInvalid
	}
	pending, err := store.PendingOperations(ctx, limit)
	if err != nil {
		return err
	}
	var failures []error
	for _, item := range pending {
		if err = ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if err = workflow.RunFleetInstance(ctx, item.OperationID); err != nil {
			failures = append(failures, err, store.DeferOperationDispatch(ctx, item.OrgID, item.OperationID))
			continue
		}
		if err = store.MarkOperationDispatched(ctx, item.OrgID, item.OperationID); err != nil {
			failures = append(failures, err, store.DeferOperationDispatch(ctx, item.OrgID, item.OperationID))
		}
	}
	return errors.Join(failures...)
}

func (s *PgStore) PendingOperations(ctx context.Context, limit int) ([]OperationDispatch, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	rows, err := s.db.Query(ctx, `SELECT o.org_id,q.operation_id FROM loomplane_fleet_operation_outbox q JOIN loomplane_fleet_operations o ON o.id=q.operation_id WHERE q.published_at IS NULL AND q.next_at<=now() ORDER BY q.next_at,q.operation_id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]OperationDispatch, 0, limit)
	for rows.Next() {
		var item OperationDispatch
		if checkErr := rows.Scan(&item.OrgID, &item.OperationID); checkErr != nil {
			return nil, checkErr
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *PgStore) MarkOperationDispatched(ctx context.Context, org, id string) error {
	result, err := s.db.Exec(ctx, `UPDATE loomplane_fleet_operation_outbox q SET published_at=coalesce(published_at,now()) FROM loomplane_fleet_operations o WHERE q.operation_id=o.id AND o.org_id=$1 AND o.id=$2`, org, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	return nil
}

// DeferOperationDispatch lets later accepted requests pass an unavailable start.
func (s *PgStore) DeferOperationDispatch(ctx context.Context, org, id string) error {
	_, err := s.db.Exec(ctx, `UPDATE loomplane_fleet_operation_outbox q SET next_at=now()+interval '30 seconds' FROM loomplane_fleet_operations o WHERE q.operation_id=o.id AND o.org_id=$1 AND o.id=$2 AND q.published_at IS NULL`, org, id)
	return err
}
