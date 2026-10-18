package platform

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/xraph/forge"
	"github.com/xraph/trove"

	"github.com/xraph/loomplane/internal/artifacts"
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
