package aws

import (
	"context"
	"errors"
	"net"

	"github.com/aws/smithy-go"
)

type dbRecoveryMutationError struct {
	cause    error
	rejected bool
}

func (e dbRecoveryMutationError) Error() string { return "RDS recovery request failed" }
func (e dbRecoveryMutationError) Unwrap() error { return e.cause }

func dbRecoveryWriteError(err error) error {
	rejected := refused(err)
	var native smithy.APIError
	if errors.As(err, &native) && (native.ErrorCode() == "InvalidDBInstanceState" || native.ErrorCode() == "InvalidDBInstanceStateFault") {
		rejected = true
	}
	return dbRecoveryMutationError{cause: err, rejected: rejected}
}

// DBRecoveryRequestRejected identifies an authorization or invalid-state rejection, which
// cannot have applied the write. Transport failures and lost responses remain
// uncertain and must never clear the retained attempt marker.
func DBRecoveryRequestRejected(err error) bool {
	var failure dbRecoveryMutationError
	return errors.As(err, &failure) && failure.rejected
}

// DBRecoveryRequestUncertain identifies a submitted write without a known
// rejection. Reconciliation can observe it but must not issue it again.
func DBRecoveryRequestUncertain(err error) bool {
	var failure dbRecoveryMutationError
	return errors.As(err, &failure) && !failure.rejected
}

// Observation failures retain the original intent, not permission to resubmit.
type dbRecoveryReadError struct{ cause error }

func (e dbRecoveryReadError) Error() string { return "RDS recovery observation is unresolved" }
func (e dbRecoveryReadError) Unwrap() error { return e.cause }

func dbRecoveryObservationError(err error) error {
	if err == nil || refused(err) {
		return err
	}
	var native smithy.APIError
	var transport net.Error
	var response interface{ HTTPStatusCode() int }
	pending := IsNotFound(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &transport)
	if errors.As(err, &native) {
		pending = pending || native.ErrorFault() == smithy.FaultServer
		switch native.ErrorCode() {
		case "Throttling", "ThrottlingException", "RequestLimitExceeded", "RequestTimeout", "RequestTimeoutException", "ServiceUnavailable":
			pending = true
		}
	}
	if errors.As(err, &response) {
		pending = pending || response.HTTPStatusCode() >= 500 || response.HTTPStatusCode() == 429
	}
	if pending {
		return dbRecoveryReadError{cause: err}
	}
	return err
}

// DBRecoveryReconciliationPending identifies metadata reads that cannot yet
// settle an existing recovery request. Identity, settings and permission
// rejections are not observation failures.
func DBRecoveryReconciliationPending(err error) bool {
	var failure dbRecoveryReadError
	return errors.As(err, &failure)
}
