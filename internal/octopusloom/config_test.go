package octopusloom

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/internal/planning"
)

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	want, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func renderDesired(t *testing.T, d Desired) []byte {
	t.Helper()
	out, err := RenderConfig(d)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func authDesired(provider, namespace string) Desired {
	host := testContainer
	if provider == ProviderKubernetes {
		host += ".shop.svc"
	}
	return Desired{
		Provider: provider, Namespace: namespace, EnvironmentID: "env_x", Resource: "edge",
		FarpHostnames: []string{"api.acme.dev"},
		Upstreams:     []Upstream{{Name: "lp-auth-9000", Instances: []Instance{{ID: "lp-auth-0", Host: host, Port: 9000}}}},
		Routes: []OctopusRoute{
			{ID: "lp-octoroute_a", Hosts: []string{"api.acme.dev"}, Path: "/auth/*", Upstream: "lp-auth-9000", StripPrefix: "/auth"},
			{ID: "lp-octoroute_a-base", Hosts: []string{"api.acme.dev"}, Path: "/auth", Upstream: "lp-auth-9000", StripPrefix: "/auth"},
		},
	}
}

func TestRenderServerConfig(t *testing.T) {
	golden(t, "config-server.yaml", renderDesired(t, authDesired(ProviderServer, "")))
}

func TestRenderKubernetesConfigWatchesOnlyThisGateway(t *testing.T) {
	out := renderDesired(t, authDesired(ProviderKubernetes, "shop"))
	golden(t, "config-kubernetes.yaml", out)
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["kubernetes"]; ok {
		t.Error("the operator's kubernetes key must not be set")
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	a := renderDesired(t, authDesired(ProviderServer, ""))
	for range 5 {
		if b := renderDesired(t, authDesired(ProviderServer, "")); !bytes.Equal(a, b) {
			t.Fatal("two renders of the same state differ")
		}
	}
}

type rendered struct {
	Routes []struct {
		ID          string   `yaml:"id"`
		Hosts       []string `yaml:"hosts"`
		Path        string   `yaml:"path"`
		Upstream    string   `yaml:"upstream"`
		StripPrefix string   `yaml:"strip_prefix"`
	} `yaml:"routes"`
	Farp struct {
		Gateway *struct {
			Hostnames []string `yaml:"hostnames"`
		} `yaml:"gateway"`
	} `yaml:"farp"`
}

func TestRenderTrailingStarAddsBaseEntry(t *testing.T) {
	d := BuildDesired(DesiredInput{
		Gateway:      serverGateway(),
		ConnectionID: "conn_a",
		Bindings:     []*planning.Binding{serviceBinding(t, "billing", "conn_a", planning.ServiceConfig{Port: 8080})},
		Routes: []*Route{
			{ID: "octoroute_w", Hostname: "api.acme.dev", Path: "/billing/*", Service: "billing", Port: 8080, StripPrefix: true},
			{ID: "octoroute_r", Hostname: "api.acme.dev", Path: "/*", Service: "billing", Port: 8080},
			{ID: "octoroute_x", Hostname: "api.acme.dev", Path: "/exact", Service: "billing", Port: 8080},
		},
	})
	var r rendered
	if err := yaml.Unmarshal(renderDesired(t, d), &r); err != nil {
		t.Fatal(err)
	}
	type entry struct{ id, path, strip string }
	got := make([]entry, 0, len(r.Routes))
	for _, e := range r.Routes {
		got = append(got, entry{e.ID, e.Path, e.StripPrefix})
		if e.Upstream != "lp-billing-8080" || len(e.Hosts) != 1 || e.Hosts[0] != "api.acme.dev" {
			t.Errorf("%s: upstream %q hosts %v", e.ID, e.Upstream, e.Hosts)
		}
	}
	want := []entry{
		{"lp-octoroute_r", "/*", ""}, {"lp-octoroute_r-base", "/", ""},
		{"lp-octoroute_w", "/billing/*", "/billing"}, {"lp-octoroute_w-base", "/billing", "/billing"},
		{"lp-octoroute_x", "/exact", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestRenderAnyHostRouteHasNoHosts(t *testing.T) {
	d := authDesired(ProviderServer, "")
	d.FarpHostnames = nil
	d.Routes = []OctopusRoute{
		{ID: "lp-octoroute_a", Path: "/auth", Upstream: "lp-auth-9000"},
		{ID: "lp-octoroute_b", Hosts: []string{"*.acme.dev"}, Path: "/", Upstream: "lp-auth-9000"},
	}
	out := renderDesired(t, d)
	var r rendered
	if err := yaml.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	if r.Routes[0].Hosts != nil {
		t.Errorf("any-host route has hosts %v", r.Routes[0].Hosts)
	}
	if len(r.Routes[1].Hosts) != 1 || r.Routes[1].Hosts[0] != "*.acme.dev" {
		t.Errorf("wildcard host = %v", r.Routes[1].Hosts)
	}
	if r.Farp.Gateway != nil {
		t.Errorf("farp.gateway should be omitted, got %+v", r.Farp.Gateway)
	}
	if strings.Contains(string(out), "hostnames") {
		t.Errorf("hostnames key present:\n%s", out)
	}
}

func TestRenderNeverContainsSecrets(t *testing.T) {
	secrets := []string{"s3cr3t-farp-token", "s3cr3t-admin-password", "s3cr3t-session-id"}
	d := authDesired(ProviderKubernetes, "shop")
	out := string(renderDesired(t, d))
	if !strings.Contains(out, "${OCTOPUS_FARP_TOKEN}") {
		t.Error("the FARP token placeholder is missing")
	}
	for _, s := range secrets {
		if strings.Contains(out, s) {
			t.Errorf("secret %q in output", s)
		}
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, child := range v {
				switch k {
				case "password", "api_token", "session":
					t.Errorf("key %q must not be rendered", k)
				}
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(doc)
	if !strings.Contains(out, "require_auth: true") {
		t.Error("admin must require auth")
	}
}
