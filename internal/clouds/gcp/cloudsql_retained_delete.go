package gcp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

func sameSQLBackupIdentity(a, b *SQLBackup) bool {
	return a != nil && b != nil && a.Name == b.Name && a.CreatedAt.Equal(b.CreatedAt) && a.Type == b.Type && a.DatabaseVersion == b.DatabaseVersion && a.KMSKey == b.KMSKey
}

// DeleteRetainedSQLBackup deletes one original independent backup. The caller
// must durably save attempted before the one DELETE. Continuations observe the
// backup's own incarnation metadata and absence, never the reusable live name.
func (s *Session) DeleteRetainedSQLBackup(ctx context.Context, region, instance, physical string, selected SQLBackup, attempted bool, check func(context.Context) error, before func() error) error {
	if err := s.checkSQLBackupSource(region, instance, physical); err != nil {
		return err
	}
	id, err := s.sqlBackupID(selected.Name)
	if err != nil {
		return err
	}
	if check == nil || selected.CreatedAt.IsZero() || selected.DatabaseVersion == "" || (selected.Type != "ON_DEMAND" && selected.Type != "FINAL") {
		return errors.New("native deletion requires an independent backup and its original scope")
	}
	path := s.sqlPath("/backups/" + url.PathEscape(id))
	if err := check(ctx); err != nil {
		return err
	}
	if !attempted {
		if before == nil {
			return errors.New("native backup deletion requires durable intent")
		}
		if _, readErr := s.ReadSQLBackup(ctx, region, instance, physical, selected); readErr != nil && !IsNotFound(readErr) {
			return readErr
		}
		if err := before(); err != nil {
			return err
		}
		if err := check(ctx); err != nil {
			return err
		}
		if _, readErr := s.ReadSQLBackup(ctx, region, instance, physical, selected); IsNotFound(readErr) {
			return check(ctx)
		} else if readErr != nil {
			return readErr
		}
		if err := check(ctx); err != nil {
			return err
		}
		var operation sqlOperation
		if err := s.do(ctx, http.MethodDelete, path, nil, &operation); err != nil {
			return retainedSQLDeleteError{cause: err}
		}
	}
	for {
		if err := check(ctx); err != nil {
			return err
		}
		var detail sqlBackup
		err := s.do(ctx, http.MethodGet, path, nil, &detail)
		if IsNotFound(err) {
			return check(ctx)
		}
		if err != nil {
			return err
		}
		point, partial, err := s.sqlBackupPoint(detail, region, instance, physical)
		if err != nil {
			return err
		}
		if partial || !sameSQLBackupIdentity(&selected, point) {
			return errors.New("native deletion observed a different backup identity")
		}
		if err := s.wait(ctx); err != nil {
			return err
		}
	}
}

type retainedSQLDeleteError struct{ cause error }

func (e retainedSQLDeleteError) Error() string { return "native backup delete request failed" }
func (e retainedSQLDeleteError) Unwrap() error { return e.cause }

// RetainedSQLDeleteRejected distinguishes an explicit native refusal from an
// unknown response. It grants no permission to retry without fresh guards.
func RetainedSQLDeleteRejected(err error) bool {
	var write retainedSQLDeleteError
	var native *APIError
	return errors.As(err, &write) && errors.As(write.cause, &native) && (native.Status == 400 || native.Status == 401 || native.Status == 403 || native.Status == 409)
}
func RetainedSQLDeleteUncertain(err error) bool {
	var write retainedSQLDeleteError
	return errors.As(err, &write) && !RetainedSQLDeleteRejected(err)
}
