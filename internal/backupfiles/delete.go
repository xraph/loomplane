package backupfiles

import (
	"context"
	"errors"

	"github.com/xraph/trove/driver"
)

// Delete removes a captured recovery object, after its caller has authorized
// retirement. Remote stores must delete the recorded version. A generic S3
// deletion can create a marker without removing the retained backup bytes.
func (s *Store) Delete(ctx context.Context, ref Object) error {
	remove, err := s.deletion(ref)
	if err != nil {
		return err
	}
	err = remove(ctx)
	if errors.Is(err, driver.ErrNotFound) {
		return nil
	}
	return err
}

// CanDelete checks destination identity and exact-version support without I/O.
// It does not establish that the object exists or that retention permits deletion.
func (s *Store) CanDelete(ref Object) error {
	_, err := s.deletion(ref)
	return err
}

func (s *Store) deletion(ref Object) (func(context.Context) error, error) {
	if err := s.validate(ref.Key); err != nil {
		return nil, err
	}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if err := s.checkDestination(ref, true); err != nil {
		return nil, err
	}
	drv := s.Trove.DriverFor(s.Bucket, ref.Key)
	switch {
	case ref.VersionID != "":
		versions, ok := drv.(interface {
			DeleteVersion(context.Context, string, string, string) error
		})
		if !ok {
			return nil, errors.New("backup driver cannot delete a captured object version")
		}
		return func(ctx context.Context) error { return versions.DeleteVersion(ctx, s.Bucket, ref.Key, ref.VersionID) }, nil
	case drv.Name() == "local" || drv.Name() == "mem":
		// These drivers have no versions. Backup attempt keys are never reused.
		return func(ctx context.Context) error { return s.Trove.Delete(ctx, s.Bucket, ref.Key) }, nil
	default:
		return nil, errors.New("remote backup deletion requires a captured object version")
	}
}
