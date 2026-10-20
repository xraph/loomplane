package backupfiles

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/trove"
	"github.com/xraph/trove/driver"
	"github.com/xraph/trove/drivers/localdriver"
)

func TestDeleteLocalBackupKeepsOtherObjectsAndCanRetry(t *testing.T) {
	drv := localdriver.New()
	backupDSN := "file://" + t.TempDir()
	if err := drv.Open(t.Context(), backupDSN); err != nil {
		t.Fatal(err)
	}
	files, err := trove.Open(drv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = files.Close(context.Background()) })
	if err = files.CreateBucket(t.Context(), "backups"); err != nil {
		t.Fatal(err)
	}
	store := configuredStore(t, files, "backups", backupDSN)
	point, err := store.Put(t.Context(), "org/project/point/attempt.dump", strings.NewReader("first"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Put(t.Context(), "org/project/other/attempt.dump", strings.NewReader("second"))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = store.Delete(t.Context(), point); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = files.Head(t.Context(), "backups", point.Key); !errors.Is(err, driver.ErrNotFound) {
		t.Fatalf("deleted point still exists: %v", err)
	}
	if _, err = files.Head(t.Context(), "backups", other.Key); err != nil {
		t.Fatalf("unrelated point changed: %v", err)
	}
}

type deletionDriver struct {
	driver.Driver
	deleted bool
}

func (*deletionDriver) Name() string { return "s3" }
func (d *deletionDriver) Delete(context.Context, string, string, ...driver.DeleteOption) error {
	d.deleted = true
	return nil
}

type versionDeletionDriver struct {
	*deletionDriver
	version string
	err     error
}

func (d *versionDeletionDriver) DeleteVersion(_ context.Context, _, _, version string) error {
	d.version = version
	return d.err
}

func TestDeleteBackupRequiresExactVersionCapability(t *testing.T) {
	for _, version := range []string{"", "version-1"} {
		drv := &deletionDriver{}
		files, err := trove.Open(drv)
		if err != nil {
			t.Fatal(err)
		}
		store := configuredStore(t, files, "backups", "s3://us-east-1/backups")
		ref := pinnedRef(t, store, Object{Key: "org/point", Size: 3, SHA256: strings.Repeat("a", 64), VersionID: version})
		if err = store.Delete(t.Context(), ref); err == nil || drv.deleted {
			t.Fatalf("unsafe fallback for version %q: deleted=%v, err=%v", version, drv.deleted, err)
		}
	}
}

func TestDeleteBackupPinsVersionAndPreservesRetentionRefusal(t *testing.T) {
	locked := errors.New("object retention has not expired")
	for _, cause := range []error{nil, driver.ErrNotFound, locked} {
		drv := &versionDeletionDriver{deletionDriver: &deletionDriver{}, err: cause}
		files, err := trove.Open(drv)
		if err != nil {
			t.Fatal(err)
		}
		store := configuredStore(t, files, "backups", "s3://us-east-1/backups")
		err = store.Delete(t.Context(), pinnedRef(t, store, Object{Key: "org/point", Size: 3, SHA256: strings.Repeat("a", 64), VersionID: "old-version"}))
		if drv.deleted || drv.version != "old-version" {
			t.Fatalf("did not select the captured version: %+v", drv)
		}
		if errors.Is(cause, driver.ErrNotFound) {
			cause = nil
		}
		if !errors.Is(err, cause) {
			t.Fatalf("deletion result: %v, want %v", err, cause)
		}
	}
}

func TestDeleteBackupRejectsMalformedReferenceBeforeStorage(t *testing.T) {
	drv := &versionDeletionDriver{deletionDriver: &deletionDriver{}}
	files, err := trove.Open(drv)
	if err != nil {
		t.Fatal(err)
	}
	store := configuredStore(t, files, "backups", "s3://us-east-1/backups")
	for _, ref := range []Object{
		{Key: "../point", Size: 3, SHA256: strings.Repeat("a", 64), VersionID: "v"},
		{Key: "point", Size: -1, SHA256: strings.Repeat("a", 64), VersionID: "v"},
		{Key: "point", Size: 3, SHA256: "invalid", VersionID: "v"},
	} {
		if err = store.Delete(t.Context(), ref); err == nil || drv.version != "" || drv.deleted {
			t.Fatalf("malformed reference reached storage: %+v, %v", ref, err)
		}
	}
}
