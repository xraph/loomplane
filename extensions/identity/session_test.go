package identity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/config"
)

func TestPublicConfigReportsTheAIFlag(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := config.Defaults()
		cfg.AI.Enabled = enabled
		r := forge.NewRouter()
		if err := New(cfg).publicConfigRoute(r); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/public-config", nil)
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		if out.Code != http.StatusOK {
			t.Fatalf("status %d: %s", out.Code, out.Body)
		}
		var body struct {
			Features struct {
				AI bool `json:"ai"`
			} `json:"features"`
		}
		if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Features.AI != enabled {
			t.Fatalf("features.ai = %v with cfg.AI.Enabled = %v", body.Features.AI, enabled)
		}
	}
}
