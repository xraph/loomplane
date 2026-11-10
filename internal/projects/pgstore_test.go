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
	for code, want := range map[string]bool{"23001": true, "23503": true, "23514": true, "23505": false, "40001": false} {
		if got := isRetained(fmt.Errorf("exec: %w", &pgconn.PgError{Code: code})); got != want {
			t.Errorf("%s: %v, want %v", code, got, want)
		}
	}
	if isRetained(nil) || isRetained(errors.New("boom")) {
		t.Fatal("a non-database error isn't retained history")
	}
}

func TestRetainedErrorTellsAFrontendFromHistory(t *testing.T) {
	frontendKey := "loomplane_frontend_slots_org_id_project_id_environment_id__fkey"
	for _, tc := range []struct {
		name string
		err  *pgconn.PgError
		want error
	}{
		{"frontend restrict", &pgconn.PgError{Code: "23001", ConstraintName: frontendKey, TableName: "loomplane_frontend_slots"}, ErrFrontendsHeld},
		{"frontend through a cascade", &pgconn.PgError{Code: "23503", ConstraintName: frontendKey}, ErrFrontendsHeld},
		{"frontend table, unnamed key", &pgconn.PgError{Code: "23001", TableName: "loomplane_frontend_uses"}, ErrFrontendsHeld},
		{"cleanup history key", &pgconn.PgError{Code: "23001", ConstraintName: "loomplane_failed_cleanups_deployment_id_fkey"}, ErrHistoryRetained},
		{"cleanup guard", &pgconn.PgError{Code: "23514", TableName: "loomplane_frontend_intents"}, ErrHistoryRetained},
	} {
		raw := fmt.Errorf("exec: %w", tc.err)
		got := retainedError(raw)
		if !errors.Is(got, tc.want) || !errors.Is(got, raw) {
			t.Errorf("%s: %v, want %v wrapping the cause", tc.name, got, tc.want)
		}
	}
	for _, err := range []error{nil, errors.New("boom"), &pgconn.PgError{Code: "40001"}} {
		if got := retainedError(err); got != nil {
			t.Errorf("%v isn't a refused delete: %v", err, got)
		}
	}
}
