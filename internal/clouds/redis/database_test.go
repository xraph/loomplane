package redis

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/xraph/trove"
	"github.com/xraph/trove/drivers/localdriver"

	"github.com/xraph/loomplane/internal/backupfiles"
)

func testFiles(t *testing.T) *backupfiles.Store {
	t.Helper()
	dsn := "file://" + t.TempDir()
	driver := localdriver.New()
	if err := driver.Open(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	files, err := trove.Open(driver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := files.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if err = files.CreateBucket(t.Context(), "backups"); err != nil {
		t.Fatal(err)
	}
	store, err := backupfiles.New(files, "backups", dsn)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestProbeRequiresAuthenticatedNativePong(t *testing.T) {
	for _, response := range []string{"PONG\n", "NOAUTH Authentication required\n", "LOADING dataset\n"} {
		db := Database{Engine: "redis", Run: func(_ context.Context, _ []string, out io.Writer) error {
			_, err := io.WriteString(out, response)
			return err
		}}
		if err := db.Ready(t.Context()); (err == nil) != (response == "PONG\n") {
			t.Fatalf("readiness accepted invalid protocol response %q: %v", response, err)
		}
	}
}

func TestConfigureChecksSuppliedCredentialWithoutPuttingItInArguments(t *testing.T) {
	password := "a-long-test-password-with-quotes-'and-backslash\\"
	uploaded, cleaned := false, false
	db := Database{Engine: "valkey", Upload: func(_ context.Context, _ string, _ int64, write func(io.Writer) error) error {
		uploaded = true
		return write(io.Discard)
	}, Run: func(_ context.Context, args []string, out io.Writer) error {
		if strings.Contains(strings.Join(args, " "), password) {
			t.Fatal("application password leaked into argv")
		}
		if args[0] == "rm" {
			cleaned = true
			return nil
		}
		_, err := io.WriteString(out, "PONG\n")
		return err
	}}
	if err := db.Configure(t.Context(), password); err != nil || !uploaded || !cleaned {
		t.Fatalf("credential probe/cleanup failed: %v, %v, %v", err, uploaded, cleaned)
	}
}

func TestRestoreRejectsLiveSourceBeforeTouchingData(t *testing.T) {
	db := Database{Engine: "redis", Run: func(context.Context, []string, io.Writer) error {
		t.Fatal("live source was touched by restore")
		return nil
	}}
	if err := db.Restore(t.Context(), nil, backupfiles.Object{}); err == nil {
		t.Fatal("live source accepted restore")
	}
}

func TestCorruptSnapshotCannotChangeOrRestartCopy(t *testing.T) {
	files := testFiles(t)
	object, err := files.Put(t.Context(), "snapshot.rdb", strings.NewReader("REDIS-invalid-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	object.SHA256 = strings.Repeat("0", 64)
	db := Database{Engine: "redis", Recovery: true, Upload: func(_ context.Context, _ string, _ int64, write func(io.Writer) error) error {
		return write(io.Discard)
	},
		Restart: func(context.Context) error { t.Fatal("corrupt snapshot restarted copy"); return nil },
		Run: func(_ context.Context, args []string, _ io.Writer) error {
			if args[0] != "rm" {
				t.Fatal("corrupt snapshot reached data operations")
			}
			return nil
		},
	}
	if err = db.Restore(t.Context(), files, object); err == nil {
		t.Fatal("corrupt snapshot accepted")
	}
}

func TestFailedSnapshotIsNotPublished(t *testing.T) {
	files := testFiles(t)
	db := Database{Engine: "valkey", Run: func(_ context.Context, _ []string, out io.Writer) error {
		if _, err := io.WriteString(out, "REDIS-partial"); err != nil {
			return err
		}
		return errors.New("replication stream interrupted")
	}}
	if _, err := db.Export(t.Context(), files, "failed.rdb"); err == nil {
		t.Fatal("failed export published")
	}
	if _, err := files.Trove.Head(t.Context(), files.Bucket, "failed.rdb"); err == nil {
		t.Fatal("partial snapshot remains readable")
	}
}
