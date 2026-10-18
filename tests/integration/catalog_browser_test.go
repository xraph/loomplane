//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// holdCatalogBrowser keeps an opt-in, disposable acceptance fixture alive for
// native browser checks and client generation. It writes no credentials.
func holdCatalogBrowser(t *testing.T, base, project, connection string) {
	t.Helper()
	directory := os.Getenv("LOOMPLANE_CATALOG_BROWSER_DIR")
	if directory == "" {
		return
	}
	if !filepath.IsAbs(directory) {
		t.Fatal("browser fixture directory must be absolute")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{"url": base, "project": project, "connection": connection})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "fixture.json")
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	t.Log("catalog browser fixture is ready")
	deadline := time.Now().Add(30 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err = os.Stat(filepath.Join(directory, "done")); err == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("browser fixture timed out")
}
