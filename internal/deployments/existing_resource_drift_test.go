package deployments

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/security"
)

type existingInventoryView struct {
	record *providerresources.ProviderResource
}

func (f existingInventoryView) Get(context.Context, security.Principal, string) (*providerresources.ProviderResource, error) {
	return f.record, nil
}

func TestExistingResourceDriftUsesInventoryWithoutNativeLifecycle(t *testing.T) {
	pin := &planning.ExistingResourceConfig{ResourceID: "resource", EndpointID: "endpoint", Revision: 4, EndpointRevision: 1, AccountIdentity: "account", ExternalID: "native-db", Incarnation: "instance"}
	raw, err := json.Marshal(planning.ResourceConfig{ExistingResource: pin})
	if err != nil {
		t.Fatal(err)
	}
	b := &planning.Binding{Subject: planning.ResourceSubject, OrgID: "org", Provider: "digitalocean", Name: "db", Config: raw}
	record := &providerresources.ProviderResource{ID: "resource", OrgID: "org", AccountIdentity: "account", ExternalID: "native-db", Incarnation: "instance", EndpointID: "endpoint", EndpointRevision: 1, Revision: 4, ObservationState: "observed"}
	c := &DriftChecker{Executors: map[string]Executor{"digitalocean": forbiddenNativeResource{t}}, ExistingResources: existingInventoryView{record}}
	if drift, _, ok := c.check(t.Context(), b, func(string, error) {}); !ok || drift != "" {
		t.Fatalf("unchanged inventory drifted: %s, %v", drift, ok)
	}
	record.Status = "missing"
	if drift, _, ok := c.check(t.Context(), b, func(string, error) {}); !ok || drift != planning.DriftMissing {
		t.Fatalf("missing inventory not reported: %s, %v", drift, ok)
	}
	record.Status = "ready"
	record.ObservationState = "stale"
	if _, _, ok := c.check(t.Context(), b, func(string, error) {}); ok {
		t.Fatal("stale observation was authoritative")
	}
	record.ObservationState = "observed"
	record.Incarnation = "replacement"
	if drift, _, ok := c.check(t.Context(), b, func(string, error) {}); !ok || drift != planning.DriftChanged {
		t.Fatalf("replacement identity not reported: %s, %v", drift, ok)
	}
}
