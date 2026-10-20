package backupfiles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/xraph/trove"
	"github.com/xraph/trove/drivers/localdriver"
)

func TestBackupDestinationSurvivesCredentialsButNotNamespaceChanges(t *testing.T) {
	base, err := destinationLocator("s3://old-key:old-secret@us-east-1/default?endpoint=https://store.example.com&path_style=true")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := destinationLocator("s3://new-key:new-secret@us-east-1/other-default?path_style=1&endpoint=https://store.example.com/")
	if err != nil || rotated != base {
		t.Fatalf("credential rotation or default bucket changed the explicit destination: %q %q %v", base, rotated, err)
	}
	for _, dsn := range []string{
		"s3://us-east-1/default?endpoint=https://other-store.example.com",
		"s3://us-west-2/default?endpoint=https://store.example.com",
		"s3://us-east-1/default?endpoint=http://store.example.com",
	} {
		other, cause := destinationLocator(dsn)
		if cause != nil || other == base {
			t.Fatalf("namespace change ignored: %q %v", dsn, cause)
		}
	}
	if strings.Contains(fmt.Sprint(base), "old-key") || strings.Contains(fmt.Sprint(base), "old-secret") {
		t.Fatal("locator contains credentials")
	}
}

// This v1 value must remain stable across releases that read existing points.
func TestBackupDestinationIdentityV1IsStable(t *testing.T) {
	files, err := trove.Open(&deletionDriver{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(files, "backups", "s3://KEY:SECRET@us-east-1/default?endpoint=https://store.example.com")
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.destinationID("org/point")
	if err != nil || id != "7e1777c2312f591c9be50e43d4ead9a7ca220bd8e7a7894ef3c6b4fc7c3dcbf1" {
		t.Fatalf("persisted destination identity changed without a migration: %q %v", id, err)
	}
}

func TestDestinationLocatorNormalizesLocalPathsAndRedactsErrors(t *testing.T) {
	root := t.TempDir()
	first, err := destinationLocator("file://" + root)
	if err != nil {
		t.Fatal(err)
	}
	alias, err := destinationLocator("local://" + root + "/child/..")
	if err != nil || alias != first {
		t.Fatalf("local alias changed destination: %+v %+v %v", first, alias, err)
	}
	for _, dsn := range []string{
		"s3://KEY:SECRET%zz@us-east-1/bucket",
		"https://KEY:SECRET@unsupported.example.com",
		"s3://KEY:SECRET@us-east-1/bucket?endpoint=https://storage.example.com?token=SECRET",
		"s3://KEY:SECRET@us-east-1/bucket?endpoint=%zz",
	} {
		if _, err = destinationLocator(dsn); err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "KEY") {
			t.Fatalf("destination error exposed credentials or accepted an unsupported configuration: %v", err)
		}
	}
}

func TestBackupPinsDestinationBeforeReadAndDelete(t *testing.T) {
	root := t.TempDir()
	drv := localdriver.New()
	if err := drv.Open(t.Context(), "file://"+root); err != nil {
		t.Fatal(err)
	}
	files, err := trove.Open(drv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = files.Close(context.Background()) })
	for _, bucket := range []string{"backups", "other"} {
		if err = files.CreateBucket(t.Context(), bucket); err != nil {
			t.Fatal(err)
		}
		if _, err = files.Put(t.Context(), bucket, "org/point", strings.NewReader("backup")); err != nil {
			t.Fatal(err)
		}
	}
	store, err := New(files, "backups", "file://"+root)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put(t.Context(), "org/point", strings.NewReader("backup"))
	if err != nil || ref.DestinationID == "" {
		t.Fatalf("backup lacks destination pin: %+v %v", ref, err)
	}
	store.Bucket = "other"
	var output bytes.Buffer
	if err = store.CopyVerified(t.Context(), ref, &output); err == nil || output.Len() != 0 {
		t.Fatal("read redirected to a different bucket", err)
	}
	if err = store.Delete(t.Context(), ref); err == nil {
		t.Fatal("deletion redirected to a different bucket")
	}
	if _, err = files.Head(t.Context(), "other", ref.Key); err != nil {
		t.Fatal("redirected deletion changed the other bucket", err)
	}
	store.Bucket = "backups"
	legacy := ref
	legacy.DestinationID = ""
	if err = store.CopyVerified(t.Context(), legacy, &output); err != nil || output.String() != "backup" {
		t.Fatalf("legacy backup cannot be recovered: %q %v", output.String(), err)
	}
	if err = store.Delete(t.Context(), legacy); err == nil {
		t.Fatal("legacy backup retired without a destination pin")
	}
	if err = store.Delete(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
}

func TestUnconfiguredBackupStoreCannotPublishNewPoints(t *testing.T) {
	drv := localdriver.New()
	if err := drv.Open(t.Context(), "file://"+t.TempDir()); err != nil {
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
	store := Store{Trove: files, Bucket: "backups"}
	source := strings.NewReader("backup")
	if _, err = store.Put(t.Context(), "org/point", source); err == nil {
		t.Fatal("published an unpinned recovery point")
	}
	if source.Len() != len("backup") {
		t.Fatal("unconfigured upload consumed its source")
	}
	if _, err = files.Head(t.Context(), "backups", "org/point"); !errors.Is(err, trove.ErrNotFound) {
		t.Fatal("unconfigured upload left an object", err)
	}
}
