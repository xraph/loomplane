package fleet

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFleetEventCanonicalIntegers(t *testing.T) {
	var event Event
	if err := json.Unmarshal([]byte(`{"version":1,"sequence":5e1,"generation":1e1,"scaling":{"policy_revision":1e2,"previous_replicas":1e1,"desired_replicas":2e1}}`), &event); err != nil {
		t.Fatal(err)
	}
	if event.Sequence != 50 || event.Generation != 10 || event.Scaling.PolicyRevision != 100 || event.Scaling.PreviousReplicas != 10 || event.Scaling.DesiredReplicas != 20 {
		t.Fatalf("integer decoding: %+v", event)
	}
	for _, raw := range []string{`{"sequence":1.5}`, `{"sequence":1e1000000}`, `{"sequence":9223372036854775808}`, `{"sequence":1} {}`} {
		if err := json.Unmarshal([]byte(raw), &event); err == nil {
			t.Fatalf("invalid integer accepted: %s", raw)
		}
	}
	if err := json.Unmarshal([]byte(`{"sequence":9223372036854775807}`), &event); err != nil || event.Sequence != 9223372036854775807 {
		t.Fatalf("integer precision lost: %+v %v", event, err)
	}
}

func TestFleetEventSchemaIdentity(t *testing.T) {
	if name := reflect.TypeOf(Event{}).Name(); name != "FleetEvent" {
		t.Fatalf("fleet event schema collides with agent activity: %s", name)
	}
}
