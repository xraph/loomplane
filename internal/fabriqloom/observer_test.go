package fabriqloom_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/fabriqloom"
	"github.com/xraph/loomplane/internal/planning"
)

// release is the written binding of service, which needs the Fabriq
// resources named, and a database.
func release(t *testing.T, service string, fabriqs ...string) deployments.Release {
	t.Helper()
	cfg := planning.ServiceConfig{Provider: "server", Env: []planning.EnvLink{
		{Key: "DATABASE_URL", From: "db", Subject: planning.ResourceSubject},
		{Key: "API_URL", From: "other", Subject: planning.ServiceSubject},
	}}
	for _, name := range fabriqs {
		cfg.Env = append(cfg.Env, planning.EnvLink{Key: "FABRIQ_URL", From: name, Subject: planning.ResourceSubject, Field: "consumer:url", Options: map[string]string{"tenant": "acme"}})
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return deployments.Release{OrgID: "org", ProjectID: "proj", EnvironmentID: "env",
		Binding: &planning.Binding{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: service, Provider: "server", Config: raw}}
}

func TestObserverIgnoresOtherBindings(t *testing.T) {
	h := newHarness("acme")
	old := idOf(t, h.f, h.bind(t, "api", "acme"))
	h.bind(t, "api", "globex")
	observer := fabriqloom.NewObserver(h.deps())

	// A service with no Fabriq link, a removal, and a resource binding open
	// no channel.
	if err := observer.Released(t.Context(), release(t, "web")); err != nil {
		t.Fatal(err)
	}
	removed := release(t, "api", "fabriq")
	removed.Removed = true
	if err := observer.Released(t.Context(), removed); err != nil {
		t.Fatal(err)
	}
	resource := release(t, "api", "fabriq")
	resource.Binding.Subject = planning.ResourceSubject
	if err := observer.Released(t.Context(), resource); err != nil {
		t.Fatal(err)
	}
	// Web needs Fabriq, but holds nothing retiring.
	h.bind(t, "web", "acme")
	if err := observer.Released(t.Context(), release(t, "web", "fabriq")); err != nil {
		t.Fatal(err)
	}
	if n := h.access.opens(); n != 0 {
		t.Fatalf("%d channels opened", n)
	}

	// A revoke that fails is logged and retried by the next release.
	h.f.failRevoke = true
	err := observer.Released(t.Context(), release(t, "api", "fabriq"))
	if err == nil {
		t.Fatal("a failed revoke wasn't reported")
	}
	noSecret(t, h.f, err)
	if len(h.warned) != 1 || !strings.Contains(h.warned[0], "api") {
		t.Fatalf("warned: %v", h.warned)
	}
	if got := h.keys.inState("api", fabriqloom.KeyRetiring); len(got) != 1 || got[0].ID != old {
		t.Fatalf("retiring: %+v", got)
	}
	if !h.secrets.has(old) {
		t.Fatal("the retiring key left Vault")
	}
	h.f.failRevoke = false
	if err := observer.Released(t.Context(), release(t, "api", "fabriq")); err != nil {
		t.Fatal(err)
	}
	if got := h.keys.inState("api", fabriqloom.KeyRetiring); len(got) != 0 {
		t.Fatalf("retiring: %+v", got)
	}
	if h.f.key(old).RevokedAt == nil {
		t.Fatal("the old key is still valid")
	}
}

func TestObserverTreatsAnUnknownKeyAsRevoked(t *testing.T) {
	h := newHarness("acme")
	h.bind(t, "api", "acme")
	h.bind(t, "api", "globex")
	retiring := h.keys.inState("api", fabriqloom.KeyRetiring)[0].ID
	// Fabriq forgot it, say after its database was restored.
	h.f.mu.Lock()
	for i, k := range h.f.keys {
		if k.ID == retiring {
			h.f.keys = append(h.f.keys[:i], h.f.keys[i+1:]...)
			break
		}
	}
	h.f.mu.Unlock()
	if err := fabriqloom.NewObserver(h.deps()).Released(t.Context(), release(t, "api", "fabriq")); err != nil {
		t.Fatal(err)
	}
	if got := h.keys.inState("api", fabriqloom.KeyRevoked); len(got) != 1 || got[0].ID != retiring {
		t.Fatalf("revoked: %+v", got)
	}
}
