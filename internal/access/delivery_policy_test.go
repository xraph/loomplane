package access

import (
	"testing"

	domain "github.com/xraph/loomplane/pkg/federation"
)

func TestDeliveryPolicyOnlyIncludesAgentDestinations(t *testing.T) {
	input, now := policyFixture(t)
	input.EvaluatedAt = now
	for _, tc := range []struct {
		name        string
		scope       domain.Scope
		destination string
		want        int
	}{
		{"source", input.Scope, "caller-cluster", 1},
		{"other source", input.Scope, "other", 0},
		{"target", input.Exports[0].Scope, "cluster-a", 1},
		{"other target", input.Exports[0].Scope, "other", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := input
			in.Scope = tc.scope
			policy, err := CompileForAgent(in, domain.AgentIdentity{Scope: tc.scope, DestinationID: tc.destination})
			if err != nil || len(policy.Grants) != tc.want || len(policy.Exports) != tc.want {
				t.Fatalf("agent policy: %+v %v", policy, err)
			}
		})
	}
	if _, err := CompileForAgent(input, domain.AgentIdentity{Scope: input.Exports[0].Scope, DestinationID: "cluster-a"}); err == nil {
		t.Fatal("foreign input accepted")
	}
	if len(input.Grants) != 1 || len(input.Exports) != 1 {
		t.Fatal("caller input mutated")
	}
}

func TestDeliveryEndpointsStayWithinCurrentExport(t *testing.T) {
	input, now := policyFixture(t)
	input.EvaluatedAt = now
	policy, err := CompileForAgent(input, domain.AgentIdentity{Scope: input.Scope, DestinationID: "caller-cluster"})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := domain.Endpoint{Metadata: domain.Metadata{Scope: input.Exports[0].Scope, ID: "endpoint"}, ExportID: "api", DestinationID: "cluster-a", Port: 8443}
	foreign := endpoint
	foreign.OrgID = "foreign"
	wrongPort := endpoint
	wrongPort.Port = 9443
	removedDestination := endpoint
	removedDestination.DestinationID = "removed"
	tombstone := domain.EndpointTombstone{Metadata: endpoint.Metadata, ExportID: endpoint.ExportID, DestinationID: endpoint.DestinationID}
	removedTombstone := tombstone
	removedTombstone.DestinationID = "removed"
	snapshot := domain.Snapshot{Policy: policy, Endpoints: []domain.Endpoint{endpoint, foreign, wrongPort, removedDestination}, Tombstones: []domain.EndpointTombstone{tombstone, removedTombstone}}
	filterDeliveryEndpoints(&snapshot)
	if len(snapshot.Endpoints) != 1 || snapshot.Endpoints[0] != endpoint || len(snapshot.Tombstones) != 1 || snapshot.Tombstones[0] != tombstone {
		t.Fatal("endpoint widened export")
	}
}
