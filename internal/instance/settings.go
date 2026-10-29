// Package instance stores the settings that belong to a whole Loomplane
// instance rather than to one organization. The table is in migration
// group "loomplane" (database/migrations).
package instance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
)

// waitlistKey is the row that says whether the waitlist is on.
const waitlistKey = "waitlist"

// Settings reads and writes the instance's settings.
type Settings struct {
	db *pgdriver.PgDB
}

// New returns Settings over pg.
func New(pg *pgdriver.PgDB) *Settings {
	return &Settings{db: pg}
}

// Waitlist reports whether the waitlist is on. stored is false when
// nothing has set it yet, and on is false then too.
func (s *Settings) Waitlist(ctx context.Context) (on bool, stored bool, err error) {
	var raw []byte
	err = s.db.QueryRow(ctx, `SELECT value FROM loomplane_instance_settings WHERE key = $1`, waitlistKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("read waitlist setting: %w", err)
	}
	if err = json.Unmarshal(raw, &on); err != nil {
		return false, false, fmt.Errorf("decode waitlist setting: %w", err)
	}
	return on, true, nil
}

// SetWaitlist turns the waitlist on or off, replacing any earlier value.
func (s *Settings) SetWaitlist(ctx context.Context, on bool) error {
	raw, err := json.Marshal(on)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_instance_settings (key, value, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, waitlistKey, raw)
	if err != nil {
		return fmt.Errorf("store waitlist setting: %w", err)
	}
	return nil
}

// EffectiveWaitlist is whether the waitlist applies. An override, such as
// an environment variable, wins when there is one. Otherwise the stored
// setting counts, and only if something stored it.
func EffectiveWaitlist(override *bool, on, stored bool) bool {
	if override != nil {
		return *override
	}
	return stored && on
}
