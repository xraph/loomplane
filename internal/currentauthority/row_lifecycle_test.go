package currentauthority

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/scan"
	"github.com/xraph/grove/schema"
)

func TestPostgresCompletedRowsAllowErrAndDeferredClose(t *testing.T) {
	for _, mode := range []string{"empty", "multiple", "explicit close", "grove empty", "grove multiple"} {
		t.Run(mode, func(t *testing.T) {
			f := newPGFixture(t)
			b := testBinding()
			var retained driver.Rows
			err := f.coordinator.Run(context.Background(), b, func(ctx context.Context, a *Attempt) error {
				query := "SELECT n FROM generate_series(1,3) n"
				want := 3
				if mode == "empty" || mode == "grove empty" {
					query = "SELECT 1 AS n WHERE false"
					want = 0
				}
				rows, err := a.Tx().Query(ctx, query)
				if err != nil {
					return err
				}
				retained = rows
				defer func() {
					if err := rows.Close(); err != nil {
						t.Errorf("deferred Close: %v", err)
					}
				}()
				switch mode {
				case "explicit close":
					if err := rows.Close(); err != nil {
						return err
					}
					if err := rows.Err(); err != nil {
						return fmt.Errorf("Err after Close: %w", err)
					}
					if err := rows.Close(); err != nil {
						return fmt.Errorf("repeated Close: %w", err)
					}
				case "grove empty", "grove multiple":
					type row struct {
						N int32 `grove:"n"`
					}
					table, err := schema.NewTable((*row)(nil))
					if err != nil {
						return err
					}
					var values []row
					if err := scan.ScanRows(rows, &values, table); err != nil {
						return err
					}
					if len(values) != want {
						return fmt.Errorf("Grove rows=%d, want %d", len(values), want)
					}
					for i, value := range values {
						if value.N != int32(i+1) {
							return fmt.Errorf("Grove value=%d", value.N)
						}
					}
				default:
					count := 0
					for rows.Next() {
						var n int32
						if err := rows.Scan(&n); err != nil {
							return err
						}
						count++
						if n != int32(count) {
							return fmt.Errorf("value=%d", n)
						}
					}
					if err := rows.Err(); err != nil {
						return fmt.Errorf("Err after EOF: %w", err)
					}
					if count != want {
						return fmt.Errorf("rows=%d, want %d", count, want)
					}
				}
				return f.stage(ctx, a, b)
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if f.count(t) != 1 {
				t.Fatal("successful rows did not commit candidate")
			}
			if err := retained.Err(); !errors.Is(err, ErrExpired) {
				t.Fatalf("retained Err: %v", err)
			}
			if err := retained.Close(); !errors.Is(err, ErrExpired) {
				t.Fatalf("retained Close: %v", err)
			}
		})
	}
}

func TestPostgresFailedRowsRetainBackendError(t *testing.T) {
	f := newPGFixture(t)
	b := testBinding()
	err := f.coordinator.Run(context.Background(), b, func(ctx context.Context, a *Attempt) error {
		if err := f.stage(ctx, a, b); err != nil {
			return err
		}
		rows, err := a.Tx().Query(ctx, "SELECT 1 / n FROM (VALUES (1),(0)) input(n)")
		if err != nil {
			return fmt.Errorf("expected deferred query error: %w", err)
		}
		for rows.Next() {
			var n int32
			if err := rows.Scan(&n); err != nil {
				return err
			}
		}
		for _, err := range []error{rows.Err(), rows.Close(), rows.Err()} {
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "22012" {
				return fmt.Errorf("backend error missing: %w", err)
			}
			if errors.Is(err, context.Canceled) {
				return fmt.Errorf("internal cancellation replaced backend error: %w", err)
			}
		}
		return nil
	})
	if !errors.Is(err, ErrPoisoned) {
		t.Fatalf("swallowed row failure: %v", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("internal cancellation escaped: %v", err)
	}
	if f.count(t) != 0 {
		t.Fatal("failed rows committed candidate")
	}
	f.cleanAttempt(t)
}
