package deployments

import (
	"testing"

	"github.com/xraph/loomplane/internal/planning"
)

func TestBindingReaperRemovesNativeApplicationOutputInventory(t *testing.T) {
	for _, provider := range []string{"cloudflare-workers", "vercel"} {
		t.Run(provider, func(t *testing.T) {
			key := resourceKey("org", "env", "assets", "output:APPLICATION_TOKEN")
			metadata := resourceKey("org", "env", "assets", apiOutputFields)
			other := resourceKey("org", "other", "assets", "output:APPLICATION_TOKEN")
			secrets := snapshotSecrets{key: []byte("private"), metadata: []byte(`["output:APPLICATION_TOKEN"]`), other: []byte("other-private")}
			reaper := &BindingReaper{Secrets: secrets}
			reaper.finish(t.Context(), reapedEnvironment{org: "org", id: "env", bindings: []*planning.Binding{{Subject: planning.ResourceSubject, Name: "assets", Provider: provider}}})
			if _, exists := secrets[key]; exists {
				t.Fatal("deleted environment retained a resource application secret")
			}
			if _, exists := secrets[metadata]; exists {
				t.Fatal("deleted environment retained the output inventory")
			}
			if _, exists := secrets[other]; !exists {
				t.Fatal("cleanup crossed environment scope")
			}
		})
	}
}

func TestBindingReaperRetainsInventoryForPartialCleanupRetry(t *testing.T) {
	key := resourceKey("org", "env", "assets", "output:APPLICATION_TOKEN")
	other := resourceKey("org", "env", "assets", "output:READ_TOKEN")
	metadata := resourceKey("org", "env", "assets", apiOutputFields)
	store := &nativeOutputTestStore{values: snapshotSecrets{key: []byte("private"), other: []byte("read-private"), metadata: []byte(`["output:APPLICATION_TOKEN","output:READ_TOKEN"]`)}, failDelete: key}
	reaper := &BindingReaper{Secrets: store}
	env := reapedEnvironment{org: "org", id: "env", bindings: []*planning.Binding{{Subject: planning.ResourceSubject, Name: "assets", Provider: "vercel"}}}
	reaper.finish(t.Context(), env)
	if _, exists := store.values[other]; exists {
		t.Fatal("one failed deletion retained the other credential")
	}
	if _, exists := store.values[metadata]; !exists {
		t.Fatal("partial cleanup lost the inventory needed for retry")
	}
	reaper.finish(t.Context(), env)
	if len(store.values) != 0 {
		t.Fatal("cleanup retry retained application credentials")
	}
}
