package provider

import (
	"encoding/json"
	"testing"
)

func TestInstanceObservationChangesReviewedDigest(t *testing.T) {
	const base = `{"revision":"kubernetes-instance-isolation-v1","pod_security_version":"v1.35","cpu_millis":1000,"memory_mib":512,"ephemeral_mib":128,"max_pods":2}`
	var before, after InstanceIsolationPolicy
	if err := json.Unmarshal([]byte(base), &before); err != nil {
		t.Fatal(err)
	}
	raw := base[:len(base)-1] + `,"observation_revision":"kubernetes-instance-observation-v1"}`
	if err := json.Unmarshal([]byte(raw), &after); err != nil {
		t.Fatal(err)
	}
	a, err := before.Digest()
	if err != nil {
		t.Fatal(err)
	}
	b, err := after.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("observation permission missing from reviewed digest")
	}
}
