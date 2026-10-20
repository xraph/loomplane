//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCatalogBrowserIgnoresPreviousCompletion(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "done")
	writeDone := func() {
		t.Helper()
		if err := os.WriteFile(marker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeDone()
	t.Setenv("LOOMPLANE_CATALOG_BROWSER_DIR", directory)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		holdCatalogBrowser(t, "http://localhost:1234", "project", "connection")
	}()
	t.Cleanup(func() {
		writeDone()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("browser fixture did not stop during cleanup")
		}
	})
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-finished:
			t.Fatal("previous completion marker released the new browser fixture")
		case <-deadline.C:
			t.Fatal("browser fixture was not published")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(directory, "fixture.json")); err != nil {
				continue
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("new browser fixture retained the previous completion marker")
			}
			writeDone()
			select {
			case <-finished:
				return
			case <-deadline.C:
				t.Fatal("browser fixture did not release after its own completion")
			}
		}
	}
}
