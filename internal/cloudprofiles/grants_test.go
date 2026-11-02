package cloudprofiles

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// A lock wait that hits lock_timeout names the lock and keeps the
// Postgres error. Anything else passes through untouched.
func TestLockWaitError(t *testing.T) {
	timeout := fmt.Errorf("exec: %w", &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"})
	err := LockWaitError(timeout, "the organization lock")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("lost the Postgres error: %v", err)
	}
	if !strings.Contains(err.Error(), "waited 1m0s for the organization lock") {
		t.Fatalf("message: %v", err)
	}
	other := &pgconn.PgError{Code: "40P01"}
	if got := LockWaitError(other, "the organization lock"); got != other { //nolint:errorlint // the same value must come back
		t.Fatalf("other error changed: %v", got)
	}
	if LockWaitError(nil, "the organization lock") != nil {
		t.Fatal("nil became an error")
	}
}
