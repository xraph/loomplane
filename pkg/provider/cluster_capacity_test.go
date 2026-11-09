package provider

import (
	"encoding/json"
	"testing"
	"time"
)

func TestClusterCapacityUnknownFactsRoundTrip(t *testing.T) {
	original := ProviderClusterCapacity{Provider: "digitalocean", Ref: ClusterRef{ID: "c", AccountID: "a", Incarnation: "birth"}, CheckedAt: time.Now().UTC(), Nodes: []ClusterCapacityNode{{ExternalVMID: "42", PoolID: "pool"}}}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var got ProviderClusterCapacity
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Complete || got.Nodes[0].CPUMillis != nil || got.Nodes[0].EphemeralStorageBytes != nil {
		t.Fatal("unknown facts changed")
	}
}
