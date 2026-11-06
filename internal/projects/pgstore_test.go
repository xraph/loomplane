package projects

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestTeamWriteErrorMapsAForeignKeyViolation(t *testing.T) {
	fk := fmt.Errorf("exec: %w", &pgconn.PgError{Code: "23503"})
	if err := teamWriteError(fk); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("a team deleted mid-write: %v, want ErrTeamNotFound", err)
	}
	other := &pgconn.PgError{Code: "40001"}
	if err := teamWriteError(other); err == nil || errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("another database error: %v", err)
	}
	if err := teamWriteError(nil); err != nil {
		t.Fatalf("nil: %v", err)
	}
}

func TestIsRetainedMatchesWhatKeepsHistory(t *testing.T) {
	for code, want := range map[string]bool{"23503": true, "23514": true, "23505": false, "40001": false} {
		if got := isRetained(fmt.Errorf("exec: %w", &pgconn.PgError{Code: code})); got != want {
			t.Errorf("%s: %v, want %v", code, got, want)
		}
	}
	if isRetained(nil) || isRetained(errors.New("boom")) {
		t.Fatal("a non-database error isn't retained history")
	}
}
