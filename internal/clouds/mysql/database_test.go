package mysql

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

func testFiles(t *testing.T) *backupfiles.Store {
	t.Helper()
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
	if bucketErr := files.CreateBucket(t.Context(), "backups"); bucketErr != nil {
		t.Fatal(bucketErr)
	}
	store, err := backupfiles.New(files, "backups", backupDSN)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestExportUsesMatchingNativeClientAndRejectsPartialDump(t *testing.T) {
	for _, engine := range []string{"mysql", "mariadb"} {
		t.Run(engine, func(t *testing.T) {
			files := testFiles(t)
			db := Database{Engine: engine, Name: "database", Run: func(_ context.Context, args []string, out io.Writer) error {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "information_schema.tables") {
					_, err := io.WriteString(out, "0\n")
					return err
				}
				tool := "mysqldump"
				if engine == "mariadb" {
					tool = "mariadb-dump"
				}
				if !strings.Contains(joined, tool) || !strings.Contains(joined, "--single-transaction") {
					t.Fatalf("wrong native dump: %v", args)
				}
				if engine == "mariadb" && strings.Contains(joined, "--set-gtid-purged") {
					t.Fatal("MySQL-only option sent to MariaDB")
				}
				if _, err := io.WriteString(out, "partial"); err != nil {
					return err
				}
				return errors.New("dump failed")
			}}
			if _, err := db.Export(t.Context(), files, "failed.sql"); err == nil {
				t.Fatal("accepted a partial native dump")
			}
			if _, err := files.Trove.Head(t.Context(), files.Bucket, "failed.sql"); err == nil {
				t.Fatal("published a failed backup")
			}
		})
	}
}

func TestExportRejectsNontransactionalTables(t *testing.T) {
	db := Database{Engine: "mysql", Name: "database", Run: func(_ context.Context, args []string, out io.Writer) error {
		if !strings.Contains(strings.Join(args, " "), "information_schema.tables") {
			t.Fatal("export started for a nontransactional database")
		}
		_, err := io.WriteString(out, "1\n")
		return err
	}}
	if _, err := db.Export(t.Context(), nil, "backup.sql"); err == nil {
		t.Fatal("nontransactional tables accepted as a consistent snapshot")
	}
}

func TestRestoreRejectsLiveSourceAndCorruptionBeforeSQL(t *testing.T) {
	db := Database{Engine: "mysql", Name: "database", Run: func(context.Context, []string, io.Writer) error { t.Fatal("restored over live data"); return nil }}
	if err := db.Restore(t.Context(), nil, backupfiles.Object{}); err == nil {
		t.Fatal("live source restore accepted")
	}
	files := testFiles(t)
	object, err := files.Put(t.Context(), "backup.sql", strings.NewReader("SELECT 1;"))
	if err != nil {
		t.Fatal(err)
	}
	object.SHA256 = strings.Repeat("0", 64)
	db.Recovery = true
	db.Upload = func(_ context.Context, _ string, _ int64, write func(io.Writer) error) error {
		return write(io.Discard)
	}
	db.Run = func(_ context.Context, args []string, _ io.Writer) error {
		if args[0] != "rm" {
			t.Fatal("SQL ran before integrity verification")
		}
		return nil
	}
	if err := db.Restore(t.Context(), files, object); err == nil {
		t.Fatal("corrupt backup accepted")
	}
}

func TestRestorePropagatesSQLFailureAndCleansTemporaryFile(t *testing.T) {
	files := testFiles(t)
	object, err := files.Put(t.Context(), "backup.sql", strings.NewReader("invalid SQL"))
	if err != nil {
		t.Fatal(err)
	}
	cleaned := false
	db := Database{Engine: "mariadb", Name: "database", Recovery: true,
		Upload: func(_ context.Context, _ string, _ int64, write func(io.Writer) error) error {
			return write(io.Discard)
		},
		Run: func(_ context.Context, args []string, _ io.Writer) error {
			if args[0] == "rm" {
				cleaned = true
				return nil
			}
			if !strings.Contains(strings.Join(args, " "), "mariadb") {
				t.Fatal("wrong restore client")
			}
			return errors.New("SQL import failed")
		},
	}
	if err := db.Restore(t.Context(), files, object); err == nil || !cleaned {
		t.Fatalf("import failure/cleanup: %v %v", err, cleaned)
	}
}

func TestConfigureKeepsPasswordOutOfArgumentsAndCleansFiles(t *testing.T) {
	password := "fixture-password-with-'quotes-and-backslash\\"
	uploaded, removed := 0, 0
	db := Database{Engine: "mysql", Name: "database", Upload: func(_ context.Context, _ string, _ int64, write func(io.Writer) error) error {
		var content bytes.Buffer
		if err := write(&content); err != nil {
			return err
		}
		uploaded++
		return nil
	}, Run: func(_ context.Context, args []string, out io.Writer) error {
		if strings.Contains(strings.Join(args, " "), password) {
			t.Fatal("password entered command arguments")
		}
		if args[0] == "rm" {
			removed += len(args) - 2
			return nil
		}
		if strings.Contains(strings.Join(args, " "), "CURRENT_USER") {
			_, err := io.WriteString(out, "app@%\n")
			return err
		}
		return nil
	}}
	if err := db.Configure(t.Context(), password); err != nil {
		t.Fatal(err)
	}
	if uploaded == 0 || uploaded != removed {
		t.Fatalf("temporary files: uploaded %d removed %d", uploaded, removed)
	}
}
