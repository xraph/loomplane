package identity

import (
	"encoding/json"
	"testing"

	"github.com/xraph/loomplane/internal/config"
)

// The route itself needs the running engine (setup state, the waitlist
// setting, the session); the integration tests cover it. This checks the
// part the config decides.
func TestPublicConfigReportsTheAIFlag(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := config.Defaults()
		cfg.AI.Enabled = enabled
		raw, err := json.Marshal(New(cfg).staticPublicConfig())
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Features struct {
				AI bool `json:"ai"`
			} `json:"features"`
			ConsolePath *string `json:"console_path"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		if body.Features.AI != enabled {
			t.Fatalf("features.ai = %v with cfg.AI.Enabled = %v", body.Features.AI, enabled)
		}
		if body.ConsolePath != nil {
			t.Fatalf("the static config carries a console path: %s", raw)
		}
	}
}
