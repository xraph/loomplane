// Package pglock makes Loomplane replicas take turns at one-off work, such
// as seeding rows on first start, with a Postgres advisory lock.
package pglock

import (
	"context"
	"errors"
	"fmt"

	"github.com/xraph/grove/drivers/pgdriver"
)

// StartupSeed is the lock every first-start seed takes, so replicas that
// boot together seed one at a time.
const StartupSeed = "loomplane-startup-seed"

// FirstRunSetup is the lock first-run setup holds while it checks that the
// instance has no users and creates the first one, so two setups can't
// both find it empty. Sign-up takes it shared (TryShared), so it never
// judges an instance that setup is half way through.
const FirstRunSetup = "loomplane-first-run-setup"

// Do runs fn while this process holds the advisory lock called name. A
// replica that asks while another holds it waits its turn, so two replicas
// starting together can't both look for a row, find nothing, and create it.
//
// The lock lives in a transaction of its own that writes nothing: fn does
// its work through its own connections and isn't part of it. When the
// transaction ends, or its connection dies with a crashed process, Postgres
// lets the lock go, so a replica that dies mid-seed never leaves it held.
func Do(ctx context.Context, pg *pgdriver.PgDB, name string, fn func(context.Context) error) error {
	if pg == nil || name == "" || fn == nil {
		return errors.New("pglock: Do needs a database, a lock name and a function")
	}
	tx, err := pg.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pglock: begin %s: %w", name, err)
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, name); err != nil {
		return errors.Join(fmt.Errorf("pglock: lock %s: %w", name, err), tx.Rollback())
	}
	if err = fn(ctx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err = tx.Rollback(); err != nil {
		return fmt.Errorf("pglock: unlock %s: %w", name, err)
	}
	return nil
}

// TryShared runs fn holding the advisory lock called name in shared mode,
// and reports whether it ran. Any number of TryShared callers hold it at
// once, but never while Do holds it. When Do does, TryShared returns false
// at once rather than wait: a caller that waited would keep a connection
// from Do's own work.
//
// The lock lives in a transaction of its own that writes nothing, as Do's
// does, and goes when fn returns.
func TryShared(ctx context.Context, pg *pgdriver.PgDB, name string, fn func(context.Context) error) (bool, error) {
	if pg == nil || name == "" || fn == nil {
		return false, errors.New("pglock: TryShared needs a database, a lock name and a function")
	}
	tx, err := pg.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("pglock: begin %s: %w", name, err)
	}
	var held bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock_shared(hashtextextended($1, 0))`, name).Scan(&held); err != nil {
		return false, errors.Join(fmt.Errorf("pglock: lock %s shared: %w", name, err), tx.Rollback())
	}
	if !held {
		return false, tx.Rollback()
	}
	if err = fn(ctx); err != nil {
		return true, errors.Join(err, tx.Rollback())
	}
	if err = tx.Rollback(); err != nil {
		return true, fmt.Errorf("pglock: unlock %s: %w", name, err)
	}
	return true, nil
}
