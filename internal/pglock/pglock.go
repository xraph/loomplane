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

// For is Do on pg, for code that takes named locks without holding the
// database itself, such as a provider client.
func For(pg *pgdriver.PgDB) func(ctx context.Context, name string, fn func(context.Context) error) error {
	return func(ctx context.Context, name string, fn func(context.Context) error) error {
		return Do(ctx, pg, name, fn)
	}
}
