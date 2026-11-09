package releaseprep

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestTerminal(t *testing.T) {
	for state, want := range map[string]bool{
		StatePlanning: false, StateReviewing: false, StatePromoting: false, StateWaiting: false,
		StatePromoted: true, StateApproved: true, StateRejected: true, StateStale: true, StateExpired: true,
		StateSuperseded: true, StateBlocked: true, StateStopped: true, StateFailed: true, StateCancelled: true,
	} {
		if got := Terminal(state); got != want {
			t.Errorf("Terminal(%q) = %v, want %v", state, got, want)
		}
	}
}

func keysOf(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// The realtime event is the report minus what must not travel in an event
// (evidence and concerns, which hold text from commits, diffs and the
// model) and the fields filled at read time.
func TestReleaseReportEventIsTheReportWithoutPrivateFields(t *testing.T) {
	var want []string
	for _, k := range keysOf(t, ReleaseReport{}) {
		switch k {
		case "evidence", "concerns", "request_id", "decided_by":
		default:
			want = append(want, k)
		}
	}
	if got := keysOf(t, ReleaseReportEvent{}); !slices.Equal(got, want) {
		t.Fatalf("event keys %v, want %v", got, want)
	}
}
