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
	if err := s.validate(ref.Key); err != nil {
		return err
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	drv := s.Trove.DriverFor(s.Bucket, ref.Key)
	var err error
	switch {
	case ref.VersionID != "":
		versions, ok := drv.(interface {
			DeleteVersion(context.Context, string, string, string) error
		})
		if !ok {
			return errors.New("backup driver cannot delete a captured object version")
		}
		err = versions.DeleteVersion(ctx, s.Bucket, ref.Key, ref.VersionID)
	case drv.Name() == "local" || drv.Name() == "mem":
		// These drivers have no versions. Backup attempt keys are never reused.
		err = s.Trove.Delete(ctx, s.Bucket, ref.Key)
	default:
		return errors.New("remote backup deletion requires a captured object version")
	}
	if errors.Is(err, driver.ErrNotFound) {
		return nil
	}
	return err
}
