package planning

import (
	"encoding/json"
	"testing"
)

func TestRollbackKeepsCurrentExternalResourcePin(t *testing.T) {
	old := &ExistingResourceConfig{ResourceID: "inventory", Revision: 1, EndpointID: "old", EndpointRevision: 1}
	current := &ExistingResourceConfig{ResourceID: "inventory", Revision: 2, EndpointID: "current", EndpointRevision: 2}
	cfg := ResourceConfig{Provider: "digitalocean", Kind: "postgres", AccountIdentity: "account", ExistingResource: current}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := Action{ID: "resource:db", Subject: ResourceSubject, Name: "db", Provider: "digitalocean", Op: Update, Resource: &ResourceConfig{Kind: "postgres", ExistingResource: old}}
	result := &Result{Actions: []Action{a}, Executable: true}
	rollbackFilter(result, []*Binding{{Subject: ResourceSubject, Name: "db", Provider: "digitalocean", ConnectionID: "current-connection", Config: raw}})
	got := result.Actions[0]
	if got.Op != Keep || got.Resource == nil || got.Resource.ExistingResource == nil || *got.Resource.ExistingResource != *current || got.ConnectionID != "current-connection" {
		t.Fatal("rollback approved historical resource access although it keeps the current attachment")
	}
	if old.Revision != 1 {
		t.Fatal("rollback mutated historical config")
	}
}

func TestAppPlatformRollbackCannotResurrectRemovedExternalAccess(t *testing.T) {
	a := Action{Name: "api", Provider: "digitalocean", Op: Update, Service: &ServiceConfig{CommitSHA: "approved"}}
	_, artifact, finding := appPlatformRollback(a, ReleasedImage{Service: "api", Provider: "digitalocean", Revision: "historical", CommitSHA: "approved", ExternalResources: true}, "previous")
	if finding == nil || artifact != nil {
		t.Fatal("rollback could reconnect a removed external attachment")
	}
}

func TestRollbackRefusesUnreadableCurrentExternalPin(t *testing.T) {
	for _, raw := range []string{`{`, `null`, `{}`} {
		t.Run(raw, func(t *testing.T) {
			a := Action{ID: "resource:db", Subject: ResourceSubject, Name: "db", Provider: "digitalocean", Op: Update, Resource: &ResourceConfig{Kind: "postgres", ExistingResource: &ExistingResourceConfig{ResourceID: "inventory"}}}
			result := &Result{Actions: []Action{a}, Executable: true}
			rollbackFilter(result, []*Binding{{Subject: ResourceSubject, Name: "db", Config: []byte(raw)}})
			if result.Executable || len(result.Problems) == 0 {
				t.Fatal("unreadable current access was replaced by historical pin")
			}
		})
	}
}
