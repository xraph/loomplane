package currentauthority

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestFleetSchemaFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name        string
		encoded     string
		failure     error
		unsupported bool
	}{
		{name: "catalog mismatch", encoded: "{}", unsupported: true},
		{name: "malformed catalog", encoded: "{"},
		{name: "deadline", failure: context.DeadlineExceeded},
		{name: "transport", failure: io.ErrUnexpectedEOF},
		{name: "unavailable transport", failure: errors.Join(ErrUnavailable, io.ErrUnexpectedEOF)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := func(context.Context, string, ...any) profileScanner {
				return profileScanFunc(func(dest ...any) error {
					if tc.failure != nil {
						return tc.failure
					}
					*dest[0].(*[]byte) = []byte(tc.encoded)
					return nil
				})
			}
			err := inspectFleetSchema(t.Context(), query, "qualified", 7)
			if !errors.Is(err, ErrUnavailable) || errors.Is(err, errFleetSchemaUnsupported) != tc.unsupported {
				t.Fatalf("classification=%v, unsupported=%v", err, tc.unsupported)
			}
			if tc.failure != nil && !errors.Is(err, tc.failure) {
				t.Fatalf("lost query failure: %v", err)
			}
		})
	}
}
