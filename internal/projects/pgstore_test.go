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
