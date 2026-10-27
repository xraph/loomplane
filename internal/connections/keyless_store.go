package connections

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// PutReservation implements AppStore.
func (s *PgStore) PutReservation(ctx context.Context, r *Reservation) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_keyless_reservations (id, org_id, user_id, cloud, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		r.ID, r.OrgID, r.UserID, r.Cloud, r.ExpiresAt, r.CreatedAt)
	if err != nil {
		return fmt.Errorf("save keyless reservation: %w", err)
	}
	return nil
}

// TakeReservation implements AppStore with one DELETE, so two creates with
// the same ID can't both win.
func (s *PgStore) TakeReservation(ctx context.Context, id string) (*Reservation, error) {
	var r Reservation
	err := s.db.QueryRow(ctx, `DELETE FROM loomplane_keyless_reservations WHERE id = $1
		RETURNING id, org_id, user_id, cloud, expires_at, created_at`, id).
		Scan(&r.ID, &r.OrgID, &r.UserID, &r.Cloud, &r.ExpiresAt, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("take keyless reservation: %w", err)
	}
	r.ExpiresAt, r.CreatedAt = r.ExpiresAt.UTC(), r.CreatedAt.UTC()
	return &r, nil
}

// SweepReservations implements AppStore.
func (s *PgStore) SweepReservations(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM loomplane_keyless_reservations WHERE expires_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("sweep keyless reservations: %w", err)
	}
	return tag.RowsAffected()
}
