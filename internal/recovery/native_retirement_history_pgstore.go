package recovery

import "context"

// NativeRetirementHistory exposes committed transitions without private source
// configuration or deletion checkpoints. Each cursor belongs to this operation.
func (s *PgStore) NativeRetirementHistory(ctx context.Context, org, id string, in RetirementHistoryInput) (*RetirementHistory, error) {
	if _, err := s.GetNativeRetirement(ctx, org, id); err != nil {
		return nil, err
	}
	limit, before, err := in.bounds()
	if err != nil {
		return nil, err
	}
	if before != 0 {
		var owned bool
		if scanErr := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_native_retirement_events WHERE org_id=$1 AND retirement_id=$2 AND id=$3)`, org, id, before).Scan(&owned); scanErr != nil {
			return nil, scanErr
		}
		if !owned {
			return nil, ErrNotFound
		}
	}
	rows, err := s.db.Query(ctx, `SELECT id::text,kind,status,actor,error,created_at FROM loomplane_native_retirement_events WHERE org_id=$1 AND retirement_id=$2 AND ($3::bigint=0 OR id<$3) ORDER BY id DESC LIMIT $4`, org, id, before, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &RetirementHistory{Events: []RetirementEvent{}}
	for rows.Next() {
		var event RetirementEvent
		if scanErr := rows.Scan(&event.Cursor, &event.Kind, &event.Status, &event.Actor, &event.Error, &event.CreatedAt); scanErr != nil {
			return nil, scanErr
		}
		page.Events = append(page.Events, event)
	}
	if readErr := rows.Err(); readErr != nil {
		return nil, readErr
	}
	if len(page.Events) > limit {
		page.Events = page.Events[:limit]
		page.NextBefore = page.Events[limit-1].Cursor
	}
	return page, nil
}

func (s *PgStore) NativePointRetired(ctx context.Context, org, source, point string) (bool, error) {
	return nativePointRetired(ctx, s.db, org, source, point)
}

// The source lock held by copy admission serializes this dependency check with
// deletion admission. A retired final backup also fences its retained PITR logs.
func nativePointRetired(ctx context.Context, db nativeRetentionReader, org, source, point string) (bool, error) {
	var retired bool
	err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_native_retirements d WHERE d.org_id=$1 AND d.source_id=$2 AND
 (d.point_id=$3 OR d.point->>'provider_id' IN (SELECT p.body->'pitr'->>'source_backup' FROM loomplane_native_points p WHERE p.org_id=$1 AND p.source_id=$2 AND p.id=$3 AND p.body->>'kind'='pitr')))`, org, source, point).Scan(&retired)
	return retired, err
}
