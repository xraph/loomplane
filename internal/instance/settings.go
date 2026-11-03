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

const (
	// waitlistKey is the row that says whether the waitlist is on.
	waitlistKey = "waitlist"
	// setupKey is the row that says first-run setup has been completed.
	// Nothing clears it.
	setupKey = "setup_completed"
	// cloudSignInOfferKey is the row that says the owner has yet to be
	// offered the chance to set up cloud sign-in. Setup writes it true and
	// ending the offer writes it false.
	cloudSignInOfferKey = "cloud_sign_in_offer"
)

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
func (s *Settings) Waitlist(ctx context.Context) (on, stored bool, err error) {
	return s.flag(ctx, waitlistKey)
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

// SetupCompleted reports whether the instance has a record that first-run
// setup was completed.
func (s *Settings) SetupCompleted(ctx context.Context) (bool, error) {
	done, _, err := s.flag(ctx, setupKey)
	return done, err
}

// CompleteSetup records that first-run setup was completed, sets the
// waitlist and turns the cloud sign-in offer on, all in one write, so none
// is stored without the others.
func (s *Settings) CompleteSetup(ctx context.Context, waitlist bool) error {
	raw, err := json.Marshal(waitlist)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_instance_settings (key, value, updated_at)
VALUES ($1, $2, now()), ($3, 'true', now()), ($4, 'true', now())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, waitlistKey, raw, setupKey, cloudSignInOfferKey)
	if err != nil {
		return fmt.Errorf("record the completed setup: %w", err)
	}
	return nil
}

// CloudSignInOffer reports whether the platform owner should still be
// offered cloud sign-in setup. Instances set up before the offer existed
// never have it.
func (s *Settings) CloudSignInOffer(ctx context.Context) (bool, error) {
	on, _, err := s.flag(ctx, cloudSignInOfferKey)
	return on, err
}

// EndCloudSignInOffer stops offering cloud sign-in setup. Ending it twice
// is harmless.
func (s *Settings) EndCloudSignInOffer(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_instance_settings (key, value, updated_at)
VALUES ($1, 'false', now())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, cloudSignInOfferKey)
	if err != nil {
		return fmt.Errorf("end the cloud sign-in offer: %w", err)
	}
	return nil
}

// MarkSetupCompleted records that first-run setup was completed, for an
// instance set up before setup kept a record. It leaves the waitlist alone.
func (s *Settings) MarkSetupCompleted(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_instance_settings (key, value, updated_at)
VALUES ($1, 'true', now())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, setupKey)
	if err != nil {
		return fmt.Errorf("record the completed setup: %w", err)
	}
	return nil
}

// flag reads a true-or-false setting. stored is false when nothing has set
// it yet, and on is false then too.
func (s *Settings) flag(ctx context.Context, key string) (on, stored bool, err error) {
	var raw []byte
	err = s.db.QueryRow(ctx, `SELECT value FROM loomplane_instance_settings WHERE key = $1`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("read %s setting: %w", key, err)
	}
	if err = json.Unmarshal(raw, &on); err != nil {
		return false, false, fmt.Errorf("decode %s setting: %w", key, err)
	}
	return on, true, nil
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
