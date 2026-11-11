package cloudinfra

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/grove/driver"
)

type namespaceJournalCapture struct {
	driver.Tx
	input, result []byte
	floor         time.Time
	nanos         int16
}

func (tx *namespaceJournalCapture) Exec(_ context.Context, query string, args ...any) (driver.Result, error) {
	if strings.Contains(query, `"loomplane_fleet_namespace_attempts"`) {
		tx.floor = args[17].(time.Time)
		tx.nanos = args[18].(int16)
		tx.input = append([]byte(nil), args[19].([]byte)...)
		tx.result = append([]byte(nil), args[20].([]byte)...)
	}
	return namespaceJournalResult{}, nil
}
func (*namespaceJournalCapture) QueryRow(context.Context, string, ...any) driver.Row {
	return namespaceJournalCount{}
}

type namespaceJournalResult struct{}

func (namespaceJournalResult) RowsAffected() (int64, error) { return 1, nil }
func (namespaceJournalResult) LastInsertId() (int64, error) { return 0, nil }

type namespaceJournalCount struct{}

func (namespaceJournalCount) Scan(out ...any) error {
	*out[0].(*int) = 0
	return nil
}

func TestNamespaceAuthorizationWrittenInstantRoundTrip(t *testing.T) {
	for _, zone := range []*time.Location{time.UTC, time.FixedZone("west", -6*60*60), time.FixedZone("east", 5*60*60+45*60)} {
		t.Run(zone.String(), func(t *testing.T) {
			in, digest, err := freezeNamespaceAction(namespaceActionFixture())
			if err != nil {
				t.Fatal(err)
			}
			at := in.Operation.UpdatedAt.In(zone)
			tx := &namespaceJournalCapture{}
			written, err := writeNamespaceAction(t.Context(), tx, "fixture", in, digest, at)
			if err != nil {
				t.Fatal(err)
			}
			replayed, err := decodeNamespaceAttempt(in, digest, tx.input, tx.result, tx.floor, tx.nanos)
			if err != nil {
				t.Fatalf("exact decision instant did not round-trip: %v", err)
			}
			if !namespaceEqual(replayed, written) || !replayed.Operation.UpdatedAt.Equal(at) || replayed.Operation.UpdatedAt.Nanosecond() != at.Nanosecond() {
				t.Fatal("journal replay changed the result or decision instant")
			}
		})
	}
}

type namespaceReservationFailure struct {
	namespaceJournalCapture
	sqlErr, rowsErr error
	rows            int64
}

func (tx *namespaceReservationFailure) Exec(ctx context.Context, query string, args ...any) (driver.Result, error) {
	if strings.Contains(query, `"loomplane_fleet_namespaces"`) {
		return namespaceFailureResult{rows: tx.rows, err: tx.rowsErr}, tx.sqlErr
	}
	return tx.namespaceJournalCapture.Exec(ctx, query, args...)
}

type namespaceFailureResult struct {
	rows int64
	err  error
}

func (r namespaceFailureResult) RowsAffected() (int64, error) { return r.rows, r.err }
func (namespaceFailureResult) LastInsertId() (int64, error)   { return 0, nil }
func TestNamespaceReservationWriteFailurePropagates(t *testing.T) {
	sqlFailure := errors.New("fixture namespace insert failed")
	rowsFailure := errors.New("fixture affected rows unavailable")
	for _, tc := range []struct {
		name                  string
		sqlErr, rowsErr, want error
		rows                  int64
	}{
		{name: "SQL error", sqlErr: sqlFailure, want: sqlFailure},
		{name: "affected rows error", rowsErr: rowsFailure, want: rowsFailure},
		{name: "no inserted row", want: ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, digest, freezeErr := freezeNamespaceAction(namespaceActionFixture())
			if freezeErr != nil {
				t.Fatal(freezeErr)
			}
			tx := &namespaceReservationFailure{sqlErr: tc.sqlErr, rowsErr: tc.rowsErr, rows: tc.rows}
			result, writeErr := writeNamespaceAction(t.Context(), tx, "fixture", in, digest, in.Operation.UpdatedAt)
			if result != nil || !errors.Is(writeErr, tc.want) {
				t.Fatalf("reservation failure was swallowed: result=%v error=%v", result != nil, writeErr)
			}
		})
	}
}
