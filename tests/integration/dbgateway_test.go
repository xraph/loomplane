//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/xraph/loomplane/internal/config"
)

// The gateway endpoints mount in the real app outside the session guard, and
// answer 503 until a signing key is configured.
func TestPlatform_dbGatewayEndpointsNeedASigningKey(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Dashboard.Dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg.Dashboard.Dir, "index.html"), []byte("<div id=root>loomplane</div>"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := start(t, cfg)
	defer r.stop()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, r.srv.URL+"/api/v1/db-gateways/dbgw_1/snapshot", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusServiceUnavailable || body.Error != "the database gateway signing key is not configured" {
		t.Fatalf("got %d %q, want 503", res.StatusCode, body.Error)
	}
}
