package octopusloom

import (
	"encoding/json"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/internal/planning"
)

// testInstance is a fixed Ctrl Plane instance id, so addresses are stable.
const testInstance = "inst_01h455vb4pex5vsknk084sn02q"

const testContainer = "lp-inst-01h455vb4pex5vsknk084sn02q"

func serviceBinding(t *testing.T, name, conn string, cfg planning.ServiceConfig) *planning.Binding {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &planning.Binding{
		Subject: planning.ServiceSubject, Name: name, ConnectionID: conn,
		ExternalID: testInstance, Config: raw, Revision: "rev-" + name,
	}
}

func serverGateway() Gateway {
	return Gateway{
		GatewayRef:    GatewayRef{EnvironmentID: "env_x", Resource: "edge"},
		Provider:      ProviderServer,
		FarpHostnames: []string{"api.acme.dev"},
	}
}

func manualRoute(id, host, path, service string) *Route {
	return &Route{ID: id, Hostname: host, Path: path, Service: service, Port: 8080}
}

func TestDesiredSkipsRouteToServiceOnAnotherConnection(t *testing.T) {
	d := BuildDesired(DesiredInput{
		Gateway:      serverGateway(),
		ConnectionID: "conn_a",
		Bindings:     []*planning.Binding{serviceBinding(t, "billing", "conn_b", planning.ServiceConfig{Port: 8080})},
		Routes:       []*Route{manualRoute("octoroute_a", "api.acme.dev", "/billing/*", "billing")},
		Hostnames:    []string{"api.acme.dev"},
	})
	if len(d.Upstreams) != 0 || len(d.Routes) != 0 {
		t.Fatalf("want nothing pushed, got %+v %+v", d.Upstreams, d.Routes)
	}
	want := "billing runs on a different server or cluster, so Octopus can't reach it."
	if got := d.Skipped["octoroute_a"]; got != want {
		t.Fatalf("Skipped = %q, want %q", got, want)
	}
}

func TestDesiredSkipsRouteWhoseServiceIsGoneOrMoved(t *testing.T) {
	d := BuildDesired(DesiredInput{
		Gateway:      serverGateway(),
		ConnectionID: "conn_a",
		Bindings: []*planning.Binding{
			serviceBinding(t, "ledger", "conn_a", planning.ServiceConfig{Port: 8081}),
			// A resource with a service's name is not a route target.
			{Subject: planning.ResourceSubject, Name: "cache", ConnectionID: "conn_a", ExternalID: testInstance, Config: json.RawMessage(`{}`)},
		},
		Routes: []*Route{
			manualRoute("octoroute_gone", "", "/billing", "billing"),
			manualRoute("octoroute_moved", "", "/ledger", "ledger"),
			manualRoute("octoroute_res", "", "/cache", "cache"),
		},
	})
	for id, want := range map[string]string{
		"octoroute_gone":  "billing isn't deployed in this environment.",
		"octoroute_moved": "ledger now listens on 8081. Edit the route to send it there.",
		"octoroute_res":   "cache isn't deployed in this environment.",
	} {
		if got := d.Skipped[id]; got != want {
			t.Errorf("Skipped[%s] = %q, want %q", id, got, want)
		}
	}
	if len(d.Routes) != 0 || len(d.Upstreams) != 0 {
		t.Fatalf("nothing should be pushed: %+v %+v", d.Routes, d.Upstreams)
	}
}

func TestDesiredSharesOneUpstreamPerServicePort(t *testing.T) {
	d := BuildDesired(DesiredInput{
		Gateway:      serverGateway(),
		ConnectionID: "conn_a",
		Bindings:     []*planning.Binding{serviceBinding(t, "billing", "conn_a", planning.ServiceConfig{Port: 8080})},
		Routes: []*Route{
			manualRoute("octoroute_b", "api.acme.dev", "/invoices", "billing"),
			manualRoute("octoroute_a", "", "/billing/*", "billing"),
		},
	})
	want := []Upstream{{Name: "lp-billing-8080", Instances: []Instance{{ID: "lp-billing-0", Host: testContainer, Port: 8080}}}}
	if !reflect.DeepEqual(d.Upstreams, want) {
		t.Fatalf("Upstreams = %+v, want %+v", d.Upstreams, want)
	}
	ids := make([]string, 0, len(d.Routes))
	for _, r := range d.Routes {
		ids = append(ids, r.ID)
		if r.Upstream != "lp-billing-8080" {
			t.Errorf("%s uses %s", r.ID, r.Upstream)
		}
	}
	wantIDs := []string{"lp-octoroute_a", "lp-octoroute_a-base", "lp-octoroute_b"}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("route ids = %v, want %v", ids, wantIDs)
	}
	if len(d.Skipped) != 0 {
		t.Fatalf("Skipped = %v", d.Skipped)
	}
}

func TestDesiredKeepsRoutesOnDetachedHostnames(t *testing.T) {
	d := BuildDesired(DesiredInput{
		Gateway:      serverGateway(),
		ConnectionID: "conn_a",
		Bindings:     []*planning.Binding{serviceBinding(t, "billing", "conn_a", planning.ServiceConfig{Port: 8080})},
		Routes:       []*Route{manualRoute("octoroute_a", "old.acme.dev", "/x", "billing")},
		Hostnames:    []string{"api.acme.dev"},
	})
	if len(d.Routes) != 1 || !reflect.DeepEqual(d.Routes[0].Hosts, []string{"old.acme.dev"}) {
		t.Fatalf("detached-hostname route dropped: %+v", d.Routes)
	}
}

func TestDesiredFarpHostnamesAreTheAttachedOnes(t *testing.T) {
	g := serverGateway()
	g.FarpHostnames = []string{"b.acme.dev", "a.acme.dev", "gone.acme.dev"}
	d := BuildDesired(DesiredInput{Gateway: g, ConnectionID: "conn_a", Hostnames: []string{"a.acme.dev", "b.acme.dev", "c.acme.dev"}})
	if want := []string{"a.acme.dev", "b.acme.dev"}; !reflect.DeepEqual(d.FarpHostnames, want) {
		t.Fatalf("FarpHostnames = %v, want %v", d.FarpHostnames, want)
	}
	g.FarpHostnames = nil
	d = BuildDesired(DesiredInput{Gateway: g, ConnectionID: "conn_a", Hostnames: []string{"a.acme.dev"}})
	if len(d.FarpHostnames) != 0 {
		t.Fatalf("no FARP hostnames chosen, got %v", d.FarpHostnames)
	}
}

func TestDesiredFarpHostnamesStayPutWhenNoneIsAttached(t *testing.T) {
	// The only FARP hostname was detached. FARP routes keep it rather than
	// widen to every attached hostname: they answer only on the private
	// network until someone picks another.
	g := serverGateway()
	g.FarpHostnames = []string{"Old.acme.dev", "gone.acme.dev"}
	d := BuildDesired(DesiredInput{Gateway: g, ConnectionID: "conn_a", Hostnames: []string{"a.acme.dev", "b.acme.dev"}})
	if want := []string{"gone.acme.dev", "old.acme.dev"}; !reflect.DeepEqual(d.FarpHostnames, want) {
		t.Fatalf("FarpHostnames = %v, want %v", d.FarpHostnames, want)
	}
	var r rendered
	if err := yaml.Unmarshal(renderDesired(t, d), &r); err != nil {
		t.Fatal(err)
	}
	if want := []string{"gone.acme.dev", "old.acme.dev"}; !reflect.DeepEqual(r.Farp.Gateway.Hostnames, want) {
		t.Fatalf("farp.gateway.hostnames = %v, want %v", r.Farp.Gateway.Hostnames, want)
	}
	// Nothing attached at all is the same.
	d = BuildDesired(DesiredInput{Gateway: g, ConnectionID: "conn_a"})
	if want := []string{"gone.acme.dev", "old.acme.dev"}; !reflect.DeepEqual(d.FarpHostnames, want) {
		t.Fatalf("FarpHostnames = %v, want %v", d.FarpHostnames, want)
	}
}

func TestDesiredRegistrationsOnlyOnServers(t *testing.T) {
	farp := &planning.FarpConfig{
		Gateway: "edge", Port: 8080,
		Metadata: map[string]string{"farp.port": "8080", "farp.openapi.path": "/openapi.json"},
	}
	svc := serviceBinding(t, "billing", "conn_a", planning.ServiceConfig{Port: 8080, Farp: farp})
	other := serviceBinding(t, "inner", "conn_a", planning.ServiceConfig{Port: 9000, Farp: &planning.FarpConfig{Gateway: "inner", Port: 9000}})
	plain := serviceBinding(t, "plain", "conn_a", planning.ServiceConfig{Port: 3000})
	in := DesiredInput{Gateway: serverGateway(), ConnectionID: "conn_a", Bindings: []*planning.Binding{other, plain, svc}}

	d := BuildDesired(in)
	want := []Registration{{
		InstanceID: "lp-svc-billing", Service: "billing", Address: testContainer, Port: 8080,
		Revision: "rev-billing", Metadata: map[string]string{"farp.openapi.path": "/openapi.json"},
	}}
	if !reflect.DeepEqual(d.Registrations, want) {
		t.Fatalf("Registrations = %+v, want %+v", d.Registrations, want)
	}
	if _, ok := farp.Metadata["farp.port"]; !ok {
		t.Fatal("BuildDesired changed the binding's metadata")
	}

	in.Gateway.Provider = ProviderKubernetes
	in.Gateway.Namespace = "shop"
	if d := BuildDesired(in); len(d.Registrations) != 0 {
		t.Fatalf("kubernetes gateway registers nothing, got %+v", d.Registrations)
	}
}

func TestDesiredRegistrationsSortedAndSameConnectionOnly(t *testing.T) {
	farp := func(p int) *planning.FarpConfig { return &planning.FarpConfig{Gateway: "edge", Port: p} }
	d := BuildDesired(DesiredInput{
		Gateway:      serverGateway(),
		ConnectionID: "conn_a",
		Bindings: []*planning.Binding{
			serviceBinding(t, "zeta", "conn_a", planning.ServiceConfig{Port: 1, Farp: farp(1)}),
			serviceBinding(t, "alpha", "conn_a", planning.ServiceConfig{Port: 2, Farp: farp(2)}),
			serviceBinding(t, "far", "conn_b", planning.ServiceConfig{Port: 3, Farp: farp(3)}),
		},
	})
	names := make([]string, 0, len(d.Registrations))
	for _, r := range d.Registrations {
		names = append(names, r.Service)
		if r.Metadata == nil {
			t.Errorf("%s has nil Metadata", r.Service)
		}
	}
	if want := []string{"alpha", "zeta"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("registered %v, want %v", names, want)
	}
}

func TestServiceAddress(t *testing.T) {
	b := &planning.Binding{ExternalID: testInstance}
	for _, tc := range []struct{ provider, namespace, want string }{
		{ProviderServer, "", testContainer},
		{ProviderKubernetes, "shop", testContainer + ".shop.svc"},
	} {
		got, err := ServiceAddress(tc.provider, tc.namespace, b)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.provider, got, err, tc.want)
		}
	}
	if _, err := ServiceAddress("vercel", "", b); err == nil {
		t.Error("unknown provider accepted")
	}
	if _, err := ServiceAddress(ProviderServer, "", &planning.Binding{ExternalID: "nope"}); err == nil {
		t.Error("bad external id accepted")
	}
}
