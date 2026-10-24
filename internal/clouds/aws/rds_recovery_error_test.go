package aws

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/aws/smithy-go"
)

func TestRDSRecoveryObservationRetainsOnlyUnresolvedNativeReads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		pending bool
	}{
		{"missing", errNotFound, true},
		{"native-missing", &smithy.GenericAPIError{Code: "DBInstanceNotFound"}, true},
		{"server", &smithy.GenericAPIError{Code: "InternalFailure", Fault: smithy.FaultServer}, true},
		{"throttled", &smithy.GenericAPIError{Code: "ThrottlingException"}, true},
		{"transport", &net.OpError{Op: "read", Net: "tcp", Err: errors.New("lost response")}, true},
		{"cancelled", context.Canceled, true},
		{"denied", &smithy.GenericAPIError{Code: "AccessDenied"}, false},
		{"invalid", &smithy.GenericAPIError{Code: "InvalidParameterValue"}, false},
		{"identity", errors.New("copy no longer matches original identity"), false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := dbRecoveryObservationError(tc.err)
			if DBRecoveryReconciliationPending(got) != tc.pending || !errors.Is(got, tc.err) {
				t.Fatal("incorrect native observation classification", got)
			}
		})
	}
}
