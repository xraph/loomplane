package recipes_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
)

func TestMemcachedIsAPrivateDisposableCache(t *testing.T) {
	kind, ok := recipes.Default().Kind("memcached")
	if !ok {
		t.Fatal("Memcached is not registered")
	}
	if kind.Placement != recipes.PlacementAppProject || !slices.Equal(kind.Targets, []string{"server"}) || len(kind.Components) != 1 || len(kind.Dependencies) != 0 {
		t.Fatalf("incorrect Memcached placement: %+v", kind)
	}
	c := kind.Components[0]
	if c.Volume != nil || c.Protection != recipes.ProtectionNone || c.Readiness == nil || c.Readiness.Port != "cache" || c.Readiness.Path != "" || c.Ports[0].Protocol != "memcached" || c.Ports[0].Container != 11211 || c.Ports[0].Routable {
		t.Fatalf("cache must have native readiness without a durability claim: %+v", c)
	}
	if !strings.Contains(strings.Join(c.Args, " "), "-U 0") || !strings.Contains(strings.Join(c.Args, " "), "-m 128") {
		t.Fatal("unbounded or UDP-enabled cache")
	}
	if len(kind.Outputs) != 1 || kind.Outputs[0].EnvKey != "MEMCACHED_SERVERS" || kind.Outputs[0].From.Field != "address:cache" {
		t.Fatal("client address must be host:port")
	}
	for _, architecture := range []string{"amd64", "arm64"} {
		image := kind.Releases[0].Images[c.Name]
		if !strings.HasPrefix(image.Digests[architecture], "sha256:") || kind.Releases[0].Product != "1.6.45" {
			t.Fatal("missing checked image pin")
		}
	}
}

func TestMemcachedReadinessCannotClaimUncheckedKubernetesSupport(t *testing.T) {
	kind, ok := recipes.Default().Kind("memcached")
	if !ok {
		t.Fatal("Memcached is not registered")
	}
	kind.Targets = []string{"server", "kubernetes"}
	if err := recipes.NewRegistry().Register(kind); err == nil {
		t.Fatal("Kubernetes readiness was accepted without a native adapter")
	}
}
