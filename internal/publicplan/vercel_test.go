package publicplan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/manifest"
)

func TestVercelBuildSettingsRemainVisibleInReview(t *testing.T) {
	command := "pnpm build"
	for _, cfg := range []*planning.ServiceConfig{
		{Commands: &manifest.BuildCommands{Build: &command}},
		{Vercel: &manifest.VercelSettings{Services: true, Root: ".", ServiceCommands: map[string]manifest.BuildCommands{"web": {Build: &command}}}},
	} {
		raw, err := json.Marshal(projectServiceConfig(cfg))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), command) {
			t.Fatalf("command omitted from review: %s", raw)
		}
	}
}
