package postgres

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/xraph/trove"
	"github.com/xraph/trove/drivers/localdriver"

	"github.com/xraph/loomplane/internal/backupfiles"
)

func TestExportPropagatesNativeFailureWithoutPublishing(t *testing.T) {
	driver := localdriver.New()
	backupDSN := "file://" + t.TempDir()
	if err := driver.Open(t.Context(), backupDSN); err != nil {
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
	if err = files.CreateBucket(t.Context(), "backup"); err != nil {
		t.Fatal(err)
	}
	store, err := backupfiles.New(files, "backup", backupDSN)
	if err != nil {
		t.Fatal(err)
	}
	db := Database{Name: "database", Run: func(_ context.Context, args []string, out io.Writer) error {
		if args[0] != "pg_dump" {
			t.Fatalf("unexpected tool: %v", args)
		}
		if _, writeErr := io.WriteString(out, "partial"); writeErr != nil {
			return writeErr
		}
		return errors.New("native dump failed")
	}}
	if _, err = db.Export(t.Context(), store, "failed.dump"); err == nil {
		t.Fatal("published a failed native export")
	}
	if _, err = files.Head(t.Context(), "backup", "failed.dump"); err == nil {
		t.Fatal("failed recovery point remains readable")
	}
}

func TestRestoreRequiresRecoveryBeforeReadingFiles(t *testing.T) {
	db := Database{Name: "database", Run: func(context.Context, []string, io.Writer) error { t.Fatal("restored over the live source"); return nil }}
	if err := db.Restore(t.Context(), nil, backupfiles.Object{}); err == nil {
		t.Fatal("source restore accepted")
	}
}

func TestConfigureDoesNotExposePasswordInArgumentsAndRemovesFiles(t *testing.T) {
	password := "a-long-fixture-password:with\\characters"
	uploaded := map[string][]byte{}
	removed := map[string]bool{}
	db := Database{Name: "database", Upload: func(_ context.Context, name string, _ int64, write func(io.Writer) error) error {
		var b bytes.Buffer
		if err := write(&b); err != nil {
			return err
		}
		uploaded[name] = b.Bytes()
		return nil
	}, Run: func(_ context.Context, args []string, out io.Writer) error {
		for _, arg := range args {
			if strings.Contains(arg, password) {
				t.Fatal("password entered command arguments")
			}
		}
		if args[0] == "rm" {
			for _, path := range args[2:] {
				removed[strings.TrimPrefix(path, "/tmp/")] = true
			}
			return nil
		}
		if args[0] == "env" {
			_, err := io.WriteString(out, "app\n")
			return err
		}
		if args[0] != "psql" {
			t.Fatalf("unexpected command %v", args)
		}
		return nil
	}}
	if err := db.Configure(t.Context(), password); err != nil {
		t.Fatal(err)
	}
	if len(uploaded) != 2 || len(removed) != 2 {
		t.Fatalf("temporary credential files were not removed: %d %d", len(uploaded), len(removed))
	}
	for name := range uploaded {
		if !removed[name] {
			t.Fatalf("retained credential file %s", name)
		}
	}
}
