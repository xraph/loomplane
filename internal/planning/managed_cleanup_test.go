package planning

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestManagedCleanupHistoricalSerialization(t *testing.T) {
	raw := `{"id":"service:api","op":"delete","subject":"service","name":"api","provider":"kubernetes","engine":"","creates":"","reason":"","step":0}`
	var action Action
	if err := json.Unmarshal([]byte(raw), &action); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "cleanup_targets") {
		t.Fatal("historical action gained cleanup metadata")
	}
	if string(got) != raw {
		t.Fatalf("historical JSON changed: %s", got)
	}
}
