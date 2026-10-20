package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/xraph/forge"
	"github.com/xraph/trove"

	"github.com/xraph/loomplane/internal/artifacts"
	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/config"
)

// An extension-managed artifact store must reopen the existing bucket and keys.
func TestFilesExtensionPreservesArtifacts(t *testing.T) {
	cfg := config.Config{}
	cfg.Storage.URL = "file://" + t.TempDir()
	original, err := artifacts.Open(t.Context(), cfg.Storage.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = original.Put(t.Context(), "sources/org/commit.tar.gz", strings.NewReader("existing artifact"), "application/gzip"); err != nil {
		t.Fatal(err)
	}
	if err = original.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		var files forge.Extension
		for _, ext := range Extensions(cfg) {
			if ext.Name() == "trove" {
				files = ext
				break
			}
		}
		if files == nil {
			t.Fatal("platform does not register the Trove extension")
		}
		app := forge.New(forge.WithAppName("files-test"))
		if err = files.Register(app); err != nil {
			t.Fatal(err)
		}
		if err = files.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		provider, ok := files.(interface{ Trove() *trove.Trove })
		if !ok {
			t.Fatal("file extension does not expose its Trove store")
		}
		object, getErr := provider.Trove().Get(t.Context(), artifacts.Bucket, "sources/org/commit.tar.gz")
		if getErr != nil {
			t.Fatal(getErr)
		}
		data, readErr := io.ReadAll(object.ReadCloser)
		object.Close()
		if readErr != nil || string(data) != "existing artifact" {
			t.Fatalf("restart %d: %q, %v", attempt, data, readErr)
		}
		if err = files.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFilesExtensionProvidesPinnedBackupStore(t *testing.T) {
	cfg := config.Config{}
	cfg.Storage.URL = "file://" + t.TempDir()
	replacement := "file://" + t.TempDir()
	var captured backupfiles.Object
	for attempt := range 3 {
		if !t.Run(fmt.Sprintf("open-%d", attempt), func(t *testing.T) {
			current := cfg
			if attempt == 2 {
				current.Storage.BackupURL = replacement
			}
			files := newFiles(current)
			app := forge.New(forge.WithAppName("pinned-backup-test"))
			if err := files.Register(app); err != nil {
				t.Fatal(err)
			}
			if err := files.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = files.Stop(context.Background()) })
			store, err := forge.InjectType[*backupfiles.Store](app.Container())
			if err != nil {
				t.Fatal("production backup store is not provided by Trove", err)
			}
			if store.Bucket != "loomplane-backups" {
				t.Fatalf("backup bucket fallback changed: %q", store.Bucket)
			}
			if attempt != 1 {
				point, putErr := store.Put(t.Context(), "org/point", strings.NewReader("persistent backup"))
				if putErr != nil || point.DestinationID == "" {
					t.Fatalf("unpinned backup: %+v %v", point, putErr)
				}
				if attempt == 0 {
					raw, marshalErr := json.Marshal(point)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					if err = json.Unmarshal(raw, &captured); err != nil {
						t.Fatal(err)
					}
				}
			}
			var output strings.Builder
			err = store.CopyVerified(t.Context(), captured, &output)
			if attempt < 2 && (err != nil || output.String() != "persistent backup") {
				t.Fatalf("restart lost the pinned backup: %q %v", output.String(), err)
			}
			if attempt == 2 && (err == nil || output.Len() != 0) {
				t.Fatal("changed backup directory accepted the previous recovery point", err)
			}
		}) {
			return
		}
	}
}

func TestFilesExtensionClosesStoresWhenBackupRegistrationFails(t *testing.T) {
	cfg := config.Config{}
	cfg.Storage.URL = "file://" + t.TempDir()
	files := newFiles(cfg)
	app := forge.New(forge.WithAppName("failed-backup-registration"))
	if err := forge.ProvideValue(app.Container(), &backupfiles.Store{}); err != nil {
		t.Fatal(err)
	}
	if err := files.Register(app); err == nil {
		t.Fatal("duplicate backup service did not fail registration")
	}
	backups, err := files.Manager().Get("backups")
	if err != nil {
		t.Fatal(err)
	}
	if err = backups.Driver().Ping(t.Context()); err == nil {
		t.Fatal("failed backup registration leaked its open driver")
	}
}

func TestFilesSeparateBackupsAndBorrowedLifecycle(t *testing.T) {
	cfg := config.Defaults()
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Storage.BackupURL = "file://" + t.TempDir()
	cfg.Storage.ArtifactBucket, cfg.Storage.BackupBucket = "source-files", "recovery-files"
	files := newFiles(cfg)
	app := forge.New(forge.WithAppName("separate-files-test"))
	if err := files.Register(app); err != nil {
		t.Fatal(err)
	}
	if err := files.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := files.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	artifactsStore := artifacts.New(files.Trove(), cfg.Storage.ArtifactBucket)
	if _, err := artifactsStore.Put(t.Context(), "same-key", strings.NewReader("source"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := artifactsStore.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	backup, err := files.Manager().Get("backups")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = backup.Get(t.Context(), cfg.Storage.BackupBucket, "same-key"); err == nil {
		t.Fatal("artifact leaked into backup store")
	}
	if _, err = backup.Put(t.Context(), cfg.Storage.BackupBucket, "same-key", strings.NewReader("backup")); err != nil {
		t.Fatal(err)
	}
	r, _, err := artifactsStore.Open(t.Context(), "same-key")
	if err != nil {
		t.Fatal("borrower closed the shared store", err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil || string(got) != "source" {
		t.Fatalf("artifact changed: %q %v", got, err)
	}
}
