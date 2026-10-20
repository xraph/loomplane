//go:build integration

package backupfiles

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/xraph/trove"
	"github.com/xraph/trove/drivers/s3driver"
)

func TestS3LargeBackupRoundTrip(t *testing.T) {
	dsn := os.Getenv("LOOMPLANE_TEST_BACKUP_S3_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_BACKUP_S3_URL to an isolated S3 test store")
	}
	drv := s3driver.New()
	if err := drv.Open(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	files, err := trove.Open(drv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { files.Close(context.Background()) })
	const bucket = "loomplane-backup-acceptance"
	if err = files.CreateBucket(t.Context(), bucket); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { files.DeleteBucket(context.Background(), bucket) })
	store := configuredStore(t, files, bucket, dsn)
	const size = 129 << 20
	ref, err := store.Put(t.Context(), "org/env/large-backup", io.LimitReader(zeroReader{}, size))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { files.Delete(context.Background(), bucket, ref.Key) })
	if ref.Size != size {
		t.Fatalf("backup bytes: %d", ref.Size)
	}
	if err = store.CopyVerified(t.Context(), ref, io.Discard); err != nil {
		t.Fatal(err)
	}
	t.Logf("verified %d bytes through Trove multipart S3 upload and checksum download", ref.Size)
}
