package deployments

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
)

func TestStoredSteps_keepWhatAStepReplaces(t *testing.T) {
	steps := []Step{
		{ActionID: "service:api", Status: StepFailed, Replaced: &planning.Binding{ID: "bind_api", Provider: "cloud-run", ExternalID: "ext-old"}},
		{ActionID: "service:web", Status: StepPending},
	}
	raw, err := encodeSteps(steps)
	if err != nil {
		t.Fatal(err)
	}
	back, err := decodeSteps(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 2 || back[0].Replaced == nil || back[0].Replaced.ExternalID != "ext-old" || back[1].Replaced != nil {
		t.Fatalf("read back %+v", back)
	}
	// Steps stored before they kept it read as replacing nothing.
	old, err := decodeSteps([]byte(`[{"action_id":"service:api","status":"failed"}]`))
	if err != nil || len(old) != 1 || old[0].Replaced != nil {
		t.Fatalf("an older step read as %+v, %v", old, err)
	}
	// The API shows a step without it.
	shown, err := json.Marshal(steps[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(shown), "ext-old") {
		t.Fatalf("the API shows %s", shown)
	}
}
