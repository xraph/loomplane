package recipes_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
)

type octopusDigests struct {
	Image   string            `json:"image"`
	Version string            `json:"version"`
	Digests map[string]string `json:"digests"`
}

func readOctopusDigests(t *testing.T) octopusDigests {
	t.Helper()
	raw, err := os.ReadFile("testdata/octopus-0.5.0-image-digests.json")
	if err != nil {
		t.Fatal(err)
	}
	var d octopusDigests
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func octopusKind(t *testing.T) recipes.Kind {
	t.Helper()
	kind, ok := recipes.Default().Kind(recipes.OctopusKind)
	if !ok {
		t.Fatal("octopus is not registered")
	}
	return kind
}

func octopusGateway(t *testing.T) recipes.Component {
	t.Helper()
	kind := octopusKind(t)
	if len(kind.Components) != 1 || kind.Components[0].Name != recipes.OctopusGateway {
		t.Fatalf("components = %+v, want exactly the gateway", kind.Components)
	}
	return kind.Components[0]
}

func TestOctopusKindResolvesPinnedGateway(t *testing.T) {
	digests := readOctopusDigests(t)
	for _, arch := range []string{"arm64", "amd64"} {
		c, err := recipes.Default().Resolve(recipes.OctopusKind, "0.5", arch, "server")
		if err != nil {
			t.Fatalf("%s: %v", arch, err)
		}
		if len(c.Components) != 1 {
			t.Fatalf("%s: %d components", arch, len(c.Components))
		}
		gw := c.Components[0]
		if gw.Name != "gateway" || gw.Role != recipes.RoleProcess || gw.Step != 0 {
			t.Fatalf("%s: gateway = %+v", arch, gw)
		}
		if want := "ghcr.io/xraph/octopus@" + digests.Digests[arch]; gw.Image != want {
			t.Fatalf("%s: image = %q, want %q", arch, gw.Image, want)
		}
	}
	if _, err := recipes.Default().Resolve(recipes.OctopusKind, "0.5", "riscv64", "server"); err == nil {
		t.Fatal("riscv64 resolved")
	}
	var info recipes.KindInfo
	for _, k := range recipes.Default().Kinds() {
		if k.Name == recipes.OctopusKind {
			info = k
		}
	}
	if info.Placement != recipes.PlacementAppProject || !slices.Equal(info.Targets, []string{"server", "kubernetes"}) || !slices.Equal(info.Versions, []string{"0.5"}) {
		t.Fatalf("info = %+v", info)
	}
}

func TestOctopusPortsAreRoutableAndControl(t *testing.T) {
	gw := octopusGateway(t)
	want := []recipes.Port{
		{Name: "http", Container: 8080, Protocol: "http", Routable: true},
		{Name: "admin", Container: 9090, Protocol: "http", Control: true},
	}
	if !reflect.DeepEqual(gw.Ports, want) {
		t.Fatalf("ports = %+v", gw.Ports)
	}
	if gw.Readiness == nil || *gw.Readiness != (recipes.HTTPProbe{Port: "http", Path: "/readyz"}) {
		t.Fatalf("readiness = %+v", gw.Readiness)
	}
	if gw.Volume != nil || gw.Protection != recipes.ProtectionNone {
		t.Fatalf("volume = %+v protection = %q", gw.Volume, gw.Protection)
	}
}

func TestOctopusSecretsAndEnv(t *testing.T) {
	kind := octopusKind(t)
	names := make([]string, 0, len(kind.Secrets))
	for _, s := range kind.Secrets {
		names = append(names, s.Name)
		if s.Bytes != 32 || s.Encoding != "base64url" || s.Prefix != "" {
			t.Fatalf("secret %q = %+v, want 32 bytes base64url", s.Name, s)
		}
	}
	if want := []string{"admin-password", "session-secret", "admin-api-token", "farp-token"}; !slices.Equal(names, want) {
		t.Fatalf("secrets = %v", names)
	}
	gw := octopusGateway(t)
	wantEnv := []recipes.EnvVar{
		{Name: "OCTOPUS_ADMIN_USERNAME", Value: "admin"},
		{Name: "OCTOPUS_ADMIN_PASSWORD", Secret: "admin-password"},
		{Name: "OCTOPUS_ADMIN_SESSION_SECRET", Secret: "session-secret"},
		{Name: "OCTOPUS_ADMIN_API_TOKEN", Secret: "admin-api-token"},
		{Name: "OCTOPUS_FARP_TOKEN", Secret: "farp-token"},
		{Name: "RUST_LOG", Value: "info"},
	}
	if !reflect.DeepEqual(gw.Env, wantEnv) {
		t.Fatalf("env = %+v", gw.Env)
	}
	wantFiles := []recipes.File{{Path: "/etc/octopus/config.yaml", Mode: fs.FileMode(0o444)}}
	if !reflect.DeepEqual(gw.Files, wantFiles) {
		t.Fatalf("files = %+v", gw.Files)
	}
	if len(gw.Command) != 0 || len(gw.Args) != 0 {
		t.Fatalf("command = %v args = %v, want the image default", gw.Command, gw.Args)
	}
}

func TestOctopusDeclaresDiscoveryRules(t *testing.T) {
	gw := octopusGateway(t)
	want := []recipes.KubernetesRule{
		{APIGroups: []string{""}, Resources: []string{"services", "endpoints"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{"discovery.k8s.io"}, Resources: []string{"endpointslices"}, Verbs: []string{"get", "list", "watch"}},
	}
	if !reflect.DeepEqual(gw.KubernetesRules, want) {
		t.Fatalf("rules = %+v", gw.KubernetesRules)
	}
	// The default registry already accepted the kind; a fresh registry must too.
	if err := recipes.NewRegistry().Register(octopusKind(t)); err != nil {
		t.Fatalf("registry refused the rules: %v", err)
	}
}

func TestOctopusOutputIsTheGatewayURL(t *testing.T) {
	want := []recipes.Output{{Name: "url", EnvKey: "OCTOPUS_URL", From: &recipes.Ref{Component: "gateway", Field: "url:http"}}}
	if got := octopusKind(t).Outputs; !reflect.DeepEqual(got, want) {
		t.Fatalf("outputs = %+v", got)
	}
	if got := recipes.EnvKey("octopus"); got != "OCTOPUS_URL" {
		t.Fatalf("EnvKey = %q", got)
	}
}

func TestOctopusDigestsMatchTheRelease(t *testing.T) {
	digests := readOctopusDigests(t)
	if digests.Image != "ghcr.io/xraph/octopus" || digests.Version != "0.5.0" {
		t.Fatalf("digest file is for %s %s", digests.Image, digests.Version)
	}
	kind := octopusKind(t)
	if len(kind.Releases) == 0 {
		t.Fatal("no release")
	}
	r := kind.Releases[0]
	if r.Version != "0.5" || r.Product != "0.5.0" || r.Revision != "1" {
		t.Fatalf("release = %+v", r)
	}
	image := r.Images[recipes.OctopusGateway]
	if image.Repository != digests.Image {
		t.Fatalf("repository = %q", image.Repository)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if digests.Digests[arch] == "" || image.Digests[arch] != digests.Digests[arch] {
			t.Fatalf("%s digest = %q, release says %q", arch, image.Digests[arch], digests.Digests[arch])
		}
	}
	if len(image.Digests) != 2 {
		t.Fatalf("digests = %v", image.Digests)
	}
}
