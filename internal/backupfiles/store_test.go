package backupfiles

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/xraph/trove"
	"github.com/xraph/trove/driver"
	"github.com/xraph/trove/drivers/localdriver"
)

func TestRoundTripAndCorruption(t *testing.T) {
	drv := localdriver.New()
	backupDSN := "file://" + t.TempDir()
	if err := drv.Open(t.Context(), backupDSN); err != nil {
		t.Fatal(err)
	}
	files, err := trove.Open(drv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { files.Close(context.Background()) })
	if err = files.CreateBucket(t.Context(), "backups"); err != nil {
		t.Fatal(err)
	}
	store := configuredStore(t, files, "backups", backupDSN)
	ref, err := store.Put(t.Context(), "org/env/run/data", strings.NewReader("abc"))
	if err != nil {
		t.Fatal(err)
	}
	if ref.Size != 3 || ref.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("wrong integrity metadata: %+v", ref)
	}
	var restored bytes.Buffer
	if err = store.CopyVerified(t.Context(), ref, &restored); err != nil || restored.String() != "abc" {
		t.Fatalf("restore %q: %v", restored.String(), err)
	}
	if _, err = files.Put(t.Context(), "backups", ref.Key, strings.NewReader("abd")); err != nil {
		t.Fatal(err)
	}
	if err = store.CopyVerified(t.Context(), ref, io.Discard); err == nil {
		t.Fatal("accepted corrupt backup")
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("source interrupted") }

// The fake models multipart transport; the real local driver owns object bytes.
type multipartStore struct {
	driver.Driver
	parts   [][]byte
	aborted bool
	fail    bool
	maxPart int
}

func (d *multipartStore) InitiateMultipart(context.Context, string, string, ...driver.PutOption) (string, error) {
	return "upload-1", nil
}
func (d *multipartStore) UploadPart(_ context.Context, _, _, _ string, n int, r io.Reader) (*driver.PartInfo, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(b) > d.maxPart {
		d.maxPart = len(b)
	}
	if d.fail {
		return nil, errors.New("transport interrupted")
	}
	d.parts = append(d.parts, b)
	return &driver.PartInfo{PartNumber: n, ETag: "etag", Size: int64(len(b))}, nil
}
func (d *multipartStore) CompleteMultipart(ctx context.Context, bucket, key, _ string, _ []driver.PartInfo) (*driver.ObjectInfo, error) {
	return d.Put(ctx, bucket, key, bytes.NewReader(bytes.Join(d.parts, nil)))
}
func (d *multipartStore) AbortMultipart(ctx context.Context, _, _, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.aborted = true
	return nil
}

func TestMultipartBoundsAndFailureCleanup(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "bounded", true: "abort"}[fail], func(t *testing.T) {
			local := localdriver.New()
			backupDSN := "file://" + t.TempDir()
			if err := local.Open(t.Context(), backupDSN); err != nil {
				t.Fatal(err)
			}
			drv := &multipartStore{Driver: local, fail: fail}
			files, err := trove.Open(drv)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { files.Close(context.Background()) })
			if err = files.CreateBucket(t.Context(), "backups"); err != nil {
				t.Fatal(err)
			}
			store := configuredStore(t, files, "backups", backupDSN)
			ref, err := store.Put(t.Context(), "org/env/run/data", io.LimitReader(zeroReader{}, 21<<20))
			if fail {
				if err == nil || !drv.aborted {
					t.Fatalf("failed upload was not aborted: %v", err)
				}
				if _, err = files.Head(t.Context(), "backups", "org/env/run/data"); err == nil {
					t.Fatal("published failed backup")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if drv.maxPart > 8<<20 || len(drv.parts) != 3 || ref.Size != 21<<20 {
				t.Fatalf("unbounded or incomplete upload: part=%d parts=%d size=%d", drv.maxPart, len(drv.parts), ref.Size)
			}
			if err = store.CopyVerified(t.Context(), ref, io.Discard); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestInterruptedSourceDoesNotPublishObject(t *testing.T) {
	drv := localdriver.New()
	backupDSN := "file://" + t.TempDir()
	if err := drv.Open(t.Context(), backupDSN); err != nil {
		t.Fatal(err)
	}
	files, err := trove.Open(drv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { files.Close(context.Background()) })
	if err = files.CreateBucket(t.Context(), "backups"); err != nil {
		t.Fatal(err)
	}
	store := configuredStore(t, files, "backups", backupDSN)
	if _, err = store.Put(t.Context(), "org/env/run/data", io.MultiReader(strings.NewReader("partial"), brokenReader{})); err == nil {
		t.Fatal("accepted truncated input")
	}
	if _, err = files.Head(t.Context(), "backups", "org/env/run/data"); err == nil {
		t.Fatal("partial backup was published")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = store.Put(ctx, "org/env/run/cancelled", strings.NewReader("data")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	for _, key := range []string{"../outside", "/absolute", "a/../b", "", "a\\b"} {
		if _, err = store.Put(t.Context(), key, strings.NewReader("data")); err == nil {
			t.Errorf("accepted %q", key)
		}
	}
}

func TestBackupSizeLimitRejectsRatherThanTruncates(t *testing.T) {
	for _, input := range []string{"abc", "abcd"} {
		data, err := io.ReadAll(&boundedReader{r: strings.NewReader(input), remaining: 3})
		if string(data) != "abc" || (err != nil) != (len(input) > 3) {
			t.Fatalf("input %q: data %q error %v", input, data, err)
		}
	}
}

func configuredStore(t *testing.T, files *trove.Trove, bucket, dsn string) *Store {
	t.Helper()
	store, err := New(files, bucket, dsn)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func pinnedRef(t *testing.T, store *Store, ref Object) Object {
	t.Helper()
	id, err := store.destinationID(ref.Key)
	if err != nil {
		t.Fatal(err)
	}
	ref.DestinationID = id
	return ref
}
