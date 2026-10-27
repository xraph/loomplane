package octopusloom

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

// gatewayBinding is the gateway component's binding on conn_a, placed on
// Cloud cloud_1.
func gatewayBinding(t *testing.T) *planning.Binding {
	t.Helper()
	raw, err := json.Marshal(planning.ResourceConfig{Kind: "octopus", CloudPlacement: &planning.CloudPlacement{CloudID: "cloud_1"}})
	if err != nil {
		t.Fatal(err)
	}
	return &planning.Binding{Subject: planning.ResourceSubject, Name: "edge--gateway", ConnectionID: "conn_a", ExternalID: testInstance, Config: raw}
}

func farpOptIn(metadata map[string]string) *planning.FarpConfig {
	return &planning.FarpConfig{Gateway: "edge", Port: 8080, Metadata: metadata}
}

func str(s string) *string { return &s }

// liveEntries are the entries Octopus reports for one route: one per
// method and host, a nil host for any host.
func liveEntries(routeID, origin, path, upstream string, methods []string, hosts ...string) []RouteInfo {
	hostPtrs := []*string{nil}
	if len(hosts) > 0 {
		hostPtrs = make([]*string, 0, len(hosts))
		for _, h := range hosts {
			hostPtrs = append(hostPtrs, str(h))
		}
	}
	out := make([]RouteInfo, 0, len(methods)*len(hostPtrs))
	for _, m := range methods {
		for _, h := range hostPtrs {
			entry := routeID + ":" + m
			if h != nil {
				entry += ":" + *h
			}
			out = append(out, RouteInfo{ID: entry, RouteID: str(routeID), Origin: origin, Host: h, Path: path, Method: m, Upstream: upstream})
		}
	}
	return out
}

func upstream(name string, port int) UpstreamInfo {
	return UpstreamInfo{Name: name, Strategy: "RoundRobin", Instances: []UpstreamInstance{{ID: name + "-0", Address: name, Port: port}}}
}

// mockBoard is the Routes board mock: acme.dev, api.acme.dev (FARP's
// home) and *.acme.dev attached; FARP routes for api and billing and an
// /auth/* that a manual route on api.acme.dev overrides.
func mockBoard(t *testing.T) BoardInput {
	t.Helper()
	ref := GatewayRef{OrgID: "org_x", ProjectID: "proj_x", EnvironmentID: "env_x", Resource: "edge"}
	routes := []*Route{
		{ID: "octoroute_web", GatewayRef: ref, Hostname: "acme.dev", Path: "/", Methods: []string{}, Service: "web", Port: 3000, Note: "Storefront", Revision: 1},
		{ID: "octoroute_auth", GatewayRef: ref, Hostname: "api.acme.dev", Path: "/auth/*", Methods: []string{}, Service: "auth", Port: 9000, StripPrefix: true, Revision: 2},
		{ID: "octoroute_sub", GatewayRef: ref, Hostname: "*.acme.dev", Path: "/", Methods: []string{}, Service: "web", Port: 3000, Note: "Customer subdomains, like shop1.acme.dev", Revision: 1},
	}
	live := liveEntries("orders", "farp", "/v1/orders/*", "api", []string{"GET", "POST"}, "api.acme.dev")
	live = append(live, liveEntries("invoices", "farp", "/v1/invoices/*", "billing", []string{"GET", "POST"}, "api.acme.dev")...)
	live = append(live, liveEntries("events", "farp", "/v1/events", "billing", []string{"GET"}, "api.acme.dev")...)
	live = append(live, liveEntries("auth-farp", "farp", "/auth/*", "auth", []string{"GET"}, "api.acme.dev")...)
	// Loomplane's own routes, as its config file put them there.
	live = append(live, liveEntries("lp-octoroute_web", "config", "/", "lp-web-3000", methodOrder, "acme.dev")...)
	live = append(live, liveEntries("lp-octoroute_auth", "config", "/auth/*", "lp-auth-9000", methodOrder, "api.acme.dev")...)
	live = append(live, liveEntries("lp-octoroute_auth-base", "config", "/auth", "lp-auth-9000", methodOrder, "api.acme.dev")...)
	return BoardInput{
		Gateway: Gateway{GatewayRef: ref, Provider: ProviderServer, FarpHostnames: []string{"api.acme.dev"}, Generation: 4, AppliedGeneration: 4, Revision: 7},
		Bindings: []*planning.Binding{
			gatewayBinding(t),
			serviceBinding(t, "web", "conn_a", planning.ServiceConfig{Port: 3000}),
			serviceBinding(t, "api", "conn_a", planning.ServiceConfig{Port: 8080, Farp: farpOptIn(map[string]string{"farp.openapi.path": "/openapi.json"})}),
			serviceBinding(t, "billing", "conn_a", planning.ServiceConfig{Port: 8080, Farp: farpOptIn(map[string]string{"farp.openapi.path": "/openapi.json", "farp.asyncapi.path": "/asyncapi.json"})}),
			serviceBinding(t, "auth", "conn_a", planning.ServiceConfig{Port: 9000}),
		},
		Routes: routes,
		Hostnames: []BoardHostname{
			{ServiceHostname: ServiceHostname{RouteID: "route_wild", ZoneID: "zone_1", Hostname: "*.acme.dev", Status: "provisioning"}, Status: &cloudinfra.RouteStatus{RouteID: "route_wild", Hostname: "*.acme.dev", Wildcard: true, Certificate: cloudinfra.DomainProvisioning}},
			{ServiceHostname: ServiceHostname{RouteID: "route_api", ZoneID: "zone_1", Hostname: "api.acme.dev", Status: "ready"}, Status: &cloudinfra.RouteStatus{RouteID: "route_api", Hostname: "api.acme.dev", Route: cloudinfra.DomainReady}},
			{ServiceHostname: ServiceHostname{RouteID: "route_apex", ZoneID: "zone_1", Hostname: "acme.dev", Status: "ready"}, Status: &cloudinfra.RouteStatus{RouteID: "route_apex", Hostname: "acme.dev", Route: cloudinfra.DomainReady}},
		},
		Live: &LiveState{
			Info:   SystemInfo{Version: "0.5.0", StartTime: "2026-10-21 10:00:00"},
			Routes: live,
			Upstreams: []UpstreamInfo{
				upstream("api", 8080), upstream("billing", 8080), upstream("auth", 9000),
				upstream("lp-web-3000", 3000), upstream("lp-auth-9000", 9000),
			},
		},
	}
}

func card(t *testing.T, b *Board, hostname string) HostCard {
	t.Helper()
	for _, c := range b.Hosts {
		if c.Hostname == hostname {
			return c
		}
	}
	t.Fatalf("no card %q in %+v", hostname, cardNames(b))
	return HostCard{}
}

func cardNames(b *Board) []string {
	out := make([]string, 0, len(b.Hosts))
	for _, c := range b.Hosts {
		out = append(out, c.Hostname)
	}
	return out
}

func rowAt(t *testing.T, c HostCard, path, source string) BoardRoute {
	t.Helper()
	for _, r := range c.Routes {
		if r.Path == path && r.Source == source {
			return r
		}
	}
	t.Fatalf("no %s row %s on %q: %+v", source, path, c.Hostname, c.Routes)
	return BoardRoute{}
}

func TestBoardGroupsRoutesByHostAndMarksOverrides(t *testing.T) {
	b := BuildBoard(mockBoard(t))
	if got, want := cardNames(b), []string{"acme.dev", "api.acme.dev", "*.acme.dev"}; !slices.Equal(got, want) {
		t.Fatalf("cards %v, want %v", got, want)
	}
	for _, c := range b.Hosts {
		if !c.Attached || c.FarpHome != (c.Hostname == "api.acme.dev") || c.Status == nil || c.DomainRouteID != c.Status.RouteID {
			t.Fatalf("card %+v", c)
		}
	}
	api := card(t, b, "api.acme.dev")
	if len(api.Routes) != 4 {
		t.Fatalf("api.acme.dev has %d routes: %+v", len(api.Routes), api.Routes)
	}
	auth := rowAt(t, api, "/auth/*", "here")
	want := BoardRoute{
		Key: "here:octoroute_auth", Source: "here", RouteID: "octoroute_auth", Path: "/auth/*", Methods: []string{},
		Service: "auth", Port: 9000, Note: "Strips /auth before forwarding", StripPrefix: true, Overrides: true, Revision: 2, Sync: "applied",
	}
	if !reflect.DeepEqual(auth, want) {
		t.Fatalf("/auth/* row\n got %+v\nwant %+v", auth, want)
	}
	for _, r := range api.Routes {
		if r.Path == "/auth/*" && r.Source == "farp" {
			t.Fatalf("the FARP /auth/* the manual route overrides is shown: %+v", r)
		}
	}
	orders := rowAt(t, api, "/v1/orders/*", "farp")
	if orders.Service != "api" || orders.Port != 8080 || !slices.Equal(orders.Methods, []string{"GET", "POST"}) ||
		orders.Note != "From the OpenAPI schema" || orders.RouteID != "" || orders.Sync != "" || orders.Overrides {
		t.Fatalf("orders row %+v", orders)
	}
	// billing names both schemas, so neither one is credited.
	if events := rowAt(t, api, "/v1/events", "farp"); events.Service != "billing" || events.Note != "From the FARP schemas" || !slices.Equal(events.Methods, []string{"GET"}) {
		t.Fatalf("events row %+v", events)
	}
	if web := rowAt(t, card(t, b, "acme.dev"), "/", "here"); web.Note != "Storefront" || web.Service != "web" || web.Port != 3000 {
		t.Fatalf("acme.dev / row %+v", web)
	}
	if b.Counts != (SourceCounts{All: 6, Farp: 3, Here: 3}) {
		t.Fatalf("counts %+v", b.Counts)
	}
	if !b.Live || b.LiveError != "" || len(b.Unrouted) != 0 || len(b.Unroutable) != 0 {
		t.Fatalf("board %+v", b)
	}
	if b.Gateway.Version != "0.5.0" || b.Gateway.Resource != "edge" || b.Gateway.Attach.CloudID != "cloud_1" ||
		b.Gateway.Attach.Service != "edge--gateway" || b.Gateway.Attach.Port != 8080 ||
		!slices.Equal(b.Gateway.Attach.BlockedPathPrefixes, []string{"/__farp", "/_farp", "/admin"}) ||
		b.Gateway.PrivateURL != "http://"+testContainer+":8080" {
		t.Fatalf("gateway %+v", b.Gateway)
	}
}

func TestBoardRouteSyncPrefersRouteErrors(t *testing.T) {
	in := mockBoard(t)
	in.Gateway.State.RouteErrors = map[string]string{"octoroute_auth": "Octopus already has its own route for /auth/* on api.acme.dev (manual-1)."}
	b := BuildBoard(in)
	auth := rowAt(t, card(t, b, "api.acme.dev"), "/auth/*", "here")
	if auth.Sync != "failed" || auth.SyncError != in.Gateway.State.RouteErrors["octoroute_auth"] {
		t.Fatalf("auth row %+v", auth)
	}
	in.Gateway.Generation = 5
	b = BuildBoard(in)
	if web := rowAt(t, card(t, b, "acme.dev"), "/", "here"); web.Sync != "pending" || web.SyncError != "" {
		t.Fatalf("web row %+v", web)
	}
}

func TestBoardOneRouteFailedTheOthersApplied(t *testing.T) {
	// A sync that reached Octopus and had one route refused records the
	// generation as applied, so only that route shows the failure.
	in := mockBoard(t)
	in.Gateway.Generation, in.Gateway.AppliedGeneration = 5, 5
	in.Gateway.State.Error = "Octopus didn't accept one of this gateway's routes. The route says why."
	in.Gateway.State.RouteErrors = map[string]string{"octoroute_auth": "Octopus already has its own route for /auth/* on api.acme.dev (manual-1)."}
	b := BuildBoard(in)
	if auth := rowAt(t, card(t, b, "api.acme.dev"), "/auth/*", "here"); auth.Sync != "failed" {
		t.Fatalf("auth row %+v", auth)
	}
	for _, host := range []string{"acme.dev", "*.acme.dev"} {
		if r := rowAt(t, card(t, b, host), "/", "here"); r.Sync != "applied" || r.SyncError != "" {
			t.Fatalf("%s row %+v", host, r)
		}
	}
	if b.Gateway.SyncError != in.Gateway.State.Error || b.Gateway.AppliedGeneration != b.Gateway.Generation {
		t.Fatalf("gateway %+v", b.Gateway)
	}
}

func TestBoardFarpRoutesWithNoHostnamesLandOnAnyHostCard(t *testing.T) {
	in := mockBoard(t)
	in.Gateway.FarpHostnames = nil
	in.Routes = nil
	in.Live.Routes = append(liveEntries("orders", "farp", "/v1/orders/*", "api", []string{"GET"}),
		liveEntries("invoices", "farp", "/v1/invoices/*", "billing", []string{"GET"})...)
	b := BuildBoard(in)
	if got, want := cardNames(b), []string{"acme.dev", "api.acme.dev", "*.acme.dev", ""}; !slices.Equal(got, want) {
		t.Fatalf("cards %v, want %v", got, want)
	}
	anyHost := card(t, b, "")
	if anyHost.Attached || len(anyHost.Routes) != 2 || anyHost.Routes[0].Source != "farp" || anyHost.Routes[1].Source != "farp" {
		t.Fatalf("any-host card %+v", anyHost)
	}
	for _, c := range b.Hosts {
		if c.FarpHome {
			t.Fatalf("%q is FARP's home with no FARP hostnames", c.Hostname)
		}
		if c.Hostname != "" && len(c.Routes) != 0 {
			t.Fatalf("%q has routes %+v", c.Hostname, c.Routes)
		}
	}
}

func TestBoardShowsUnroutedAndUnroutableServices(t *testing.T) {
	in := mockBoard(t)
	in.Bindings = append(in.Bindings,
		serviceBinding(t, "admin", "conn_a", planning.ServiceConfig{Port: 4000}),
		serviceBinding(t, "worker", "conn_a", planning.ServiceConfig{Workload: provider.Worker}),
		serviceBinding(t, "jobs", "conn_b", planning.ServiceConfig{Port: 8080}),
	)
	b := BuildBoard(in)
	if want := []ServicePort{{Service: "admin", Port: 4000}}; !reflect.DeepEqual(b.Unrouted, want) {
		t.Fatalf("unrouted %+v, want %+v", b.Unrouted, want)
	}
	want := []UnroutableService{{Service: "jobs", Reason: "other-target"}, {Service: "worker", Reason: "no-port"}}
	if !reflect.DeepEqual(b.Unroutable, want) {
		t.Fatalf("unroutable %+v, want %+v", b.Unroutable, want)
	}
}

func TestBoardMapsKubernetesFarpServiceNamesBackToServices(t *testing.T) {
	const apiInstance = "inst_01jbzq7m0dxf4w3y5v8k2n6r9t"
	iid, err := id.Parse(apiInstance)
	if err != nil {
		t.Fatal(err)
	}
	k8sName := kubernetes.WorkloadName(iid)
	in := mockBoard(t)
	in.Gateway.Provider, in.Gateway.Namespace, in.Gateway.FarpHostnames = ProviderKubernetes, "apps", nil
	in.Routes = nil
	apiBinding := serviceBinding(t, "api", "conn_a", planning.ServiceConfig{Port: 8080, Farp: farpOptIn(map[string]string{"farp.openapi.path": "/openapi.json"})})
	apiBinding.ExternalID = apiInstance
	in.Bindings = []*planning.Binding{gatewayBinding(t), apiBinding}
	in.Live.Routes = liveEntries("orders", "kubernetes", "/v1/orders/*", k8sName, []string{"GET"})
	in.Live.Upstreams = []UpstreamInfo{upstream(k8sName, 8080)}
	b := BuildBoard(in)
	orders := rowAt(t, card(t, b, ""), "/v1/orders/*", "farp")
	if orders.Service != "api" || orders.Port != 8080 || orders.Note != "From the OpenAPI schema" {
		t.Fatalf("orders row %+v", orders)
	}
	if b.Gateway.PrivateURL != "http://"+testContainer+".apps.svc:8080" {
		t.Fatalf("private url %q", b.Gateway.PrivateURL)
	}
	if b.Discovery.Mode != "kubernetes" || b.Discovery.Namespace != "apps" {
		t.Fatalf("discovery %+v", b.Discovery)
	}
}

func TestBoardTreatsPushedConfigRouteAsAddedHere(t *testing.T) {
	in := mockBoard(t)
	ref := in.Gateway.GatewayRef
	in.Gateway.FarpHostnames = nil
	in.Routes = []*Route{
		{ID: "octoroute_a", GatewayRef: ref, Hostname: "api.acme.dev", Path: "/auth/*", Methods: []string{}, Service: "auth", Port: 9000, StripPrefix: true, Revision: 1},
		{ID: "octoroute_b", GatewayRef: ref, Hostname: "api.acme.dev", Path: "/login", Methods: []string{"POST"}, Service: "auth", Port: 9000, Revision: 1},
	}
	// A push replaced the config file's route, so Octopus says admin.
	live := liveEntries("lp-octoroute_a", "admin", "/auth/*", "lp-auth-9000", methodOrder, "api.acme.dev")
	live = append(live, liveEntries("lp-octoroute_a-base", "admin", "/auth", "lp-auth-9000", methodOrder, "api.acme.dev")...)
	live = append(live, liveEntries("lp-octoroute_b", "config", "/login", "lp-auth-9000", []string{"POST"}, "api.acme.dev")...)
	live = append(live, liveEntries("lp-octoroute_gone", "config", "/old", "lp-auth-9000", []string{"GET"}, "api.acme.dev")...)
	in.Live.Routes = live
	b := BuildBoard(in)
	api := card(t, b, "api.acme.dev")
	if len(api.Routes) != 2 || api.Routes[0].Source != "here" || api.Routes[1].Source != "here" {
		t.Fatalf("api.acme.dev rows %+v", api.Routes)
	}
	if b.Counts != (SourceCounts{All: 2, Farp: 0, Here: 2}) {
		t.Fatalf("counts %+v", b.Counts)
	}
	for _, c := range b.Hosts {
		for _, r := range c.Routes {
			if r.Source == "octopus-admin" || r.Path == "/old" {
				t.Fatalf("row %+v on %q", r, c.Hostname)
			}
		}
	}
}

func TestBoardShowsOctopusAdminRoutesSeparately(t *testing.T) {
	in := mockBoard(t)
	in.Live.Routes = append(in.Live.Routes, liveEntries("manual-1", "admin", "/debug", "billing", []string{"GET"})...)
	b := BuildBoard(in)
	row := rowAt(t, card(t, b, ""), "/debug", "octopus-admin")
	if row.Note != "Added in the Octopus admin. It won't survive a restart, so add it here instead." || row.Service != "billing" || row.Port != 8080 || row.Sync != "" {
		t.Fatalf("admin row %+v", row)
	}
	if b.Counts != (SourceCounts{All: 7, Farp: 3, Here: 3}) {
		t.Fatalf("counts %+v", b.Counts)
	}
}

func TestBoardWorksWhenOctopusIsUnreachable(t *testing.T) {
	in := mockBoard(t)
	in.Live = nil
	in.Gateway.Generation = 5
	b := BuildBoard(in)
	if b.Live || b.LiveError != "Octopus isn't answering on its private port, so FARP routes can't be shown. Your routes are kept and pushed again when it's back." {
		t.Fatalf("live %v %q", b.Live, b.LiveError)
	}
	if b.Counts != (SourceCounts{All: 3, Farp: 0, Here: 3}) {
		t.Fatalf("counts %+v", b.Counts)
	}
	for _, c := range b.Hosts {
		for _, r := range c.Routes {
			if r.Source != "here" || r.Sync != "pending" || r.Overrides {
				t.Fatalf("row %+v on %q", r, c.Hostname)
			}
		}
	}
	if b.Gateway.Version != "" {
		t.Fatalf("version %q with Octopus down", b.Gateway.Version)
	}
}

func TestBoardKeepsRoutesOnDetachedHostname(t *testing.T) {
	in := mockBoard(t)
	in.Routes = append(in.Routes, &Route{ID: "octoroute_old", GatewayRef: in.Gateway.GatewayRef, Hostname: "old.acme.dev", Path: "/", Methods: []string{}, Service: "web", Port: 3000, Revision: 1})
	b := BuildBoard(in)
	if got, want := cardNames(b), []string{"acme.dev", "api.acme.dev", "*.acme.dev", "old.acme.dev"}; !slices.Equal(got, want) {
		t.Fatalf("cards %v, want %v", got, want)
	}
	old := card(t, b, "old.acme.dev")
	if old.Attached || old.Status != nil || old.DomainRouteID != "" || len(old.Routes) != 1 || old.Routes[0].RouteID != "octoroute_old" {
		t.Fatalf("old.acme.dev card %+v", old)
	}
}

func TestDiscoveryOnDockerReadsTheRegistrations(t *testing.T) {
	in := mockBoard(t)
	at := time.Date(2026, 10, 21, 11, 0, 0, 0, time.UTC)
	in.Gateway.State.Registrations = map[string]Registered{
		"api":     {InstanceID: "lp-svc-api", RegisteredAt: at, HeartbeatAt: at.Add(time.Minute)},
		"billing": {InstanceID: "lp-svc-billing", Error: "Octopus refused billing's FARP registration: 400"},
	}
	in.Bindings = append(in.Bindings, serviceBinding(t, "search", "conn_a", planning.ServiceConfig{Port: 8080, Farp: farpOptIn(nil)}))
	d := BuildBoard(in).Discovery
	if d.Mode != "push" || d.Namespace != "" || !d.LastChange.Equal(at) {
		t.Fatalf("discovery %+v", d)
	}
	got := map[string]DiscoveredService{}
	for _, s := range d.Services {
		got[s.Service] = s
	}
	if len(d.Services) != 3 || d.Services[0].Service != "api" {
		t.Fatalf("services %+v", d.Services)
	}
	if s := got["api"]; !s.OptedIn || s.State != "registered" || s.Routes != 1 || !s.UpdatedAt.Equal(at) || s.Metadata["farp.openapi.path"] != "/openapi.json" {
		t.Fatalf("api %+v", s)
	}
	if s := got["billing"]; s.State != "failed" || s.Error == "" || s.Routes != 2 {
		t.Fatalf("billing %+v", s)
	}
	if s := got["search"]; s.State != "waiting" || s.Routes != 0 {
		t.Fatalf("search %+v", s)
	}
}

func TestDiscoveryOnKubernetesReadsWhatOctopusFound(t *testing.T) {
	const apiInstance = "inst_01jbzq7m0dxf4w3y5v8k2n6r9t"
	iid, err := id.Parse(apiInstance)
	if err != nil {
		t.Fatal(err)
	}
	in := mockBoard(t)
	in.Gateway.Provider, in.Gateway.Namespace = ProviderKubernetes, "apps"
	apiBinding := serviceBinding(t, "api", "conn_a", planning.ServiceConfig{Port: 8080, Farp: farpOptIn(nil)})
	apiBinding.ExternalID = apiInstance
	in.Bindings = []*planning.Binding{gatewayBinding(t), apiBinding,
		serviceBinding(t, "billing", "conn_a", planning.ServiceConfig{Port: 8080, Farp: farpOptIn(nil)})}
	seen := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	in.Live.FarpServices = []FarpService{{Name: kubernetes.WorkloadName(iid), UpdatedAt: seen}}
	d := BuildBoard(in).Discovery
	if len(d.Services) != 2 || d.Services[0].Service != "api" || d.Services[0].State != "discovered" || !d.Services[0].UpdatedAt.Equal(seen) ||
		d.Services[1].Service != "billing" || d.Services[1].State != "waiting" {
		t.Fatalf("services %+v", d.Services)
	}
}

func TestBoardHasAnAnyHostCardWhenFarpAnswersOnAnyHost(t *testing.T) {
	in := mockBoard(t)
	in.Gateway.FarpHostnames = nil
	in.Live = nil
	b := BuildBoard(in)
	if got, want := cardNames(b), []string{"acme.dev", "api.acme.dev", "*.acme.dev", ""}; !slices.Equal(got, want) {
		t.Fatalf("cards %v, want %v", got, want)
	}
	if anyHost := card(t, b, ""); anyHost.Attached || anyHost.FarpHome || anyHost.Routes == nil || len(anyHost.Routes) != 0 {
		t.Fatalf("any-host card %+v", anyHost)
	}
	// A FARP hostname that isn't attached any more leaves FARP on any host.
	in.Gateway.FarpHostnames = []string{"gone.acme.dev"}
	if got := cardNames(BuildBoard(in)); !slices.Contains(got, "") {
		t.Fatalf("cards %v", got)
	}
	// FARP routes on api.acme.dev and no manual route on any host: no card.
	in.Gateway.FarpHostnames = []string{"api.acme.dev"}
	if got := cardNames(BuildBoard(in)); slices.Contains(got, "") {
		t.Fatalf("cards %v", got)
	}
	// A manual route with no hostname has its card.
	in.Routes = append(in.Routes, &Route{ID: "octoroute_any", GatewayRef: in.Gateway.GatewayRef, Path: "/health", Methods: []string{}, Service: "web", Port: 3000, Revision: 1})
	if anyHost := card(t, BuildBoard(in), ""); len(anyHost.Routes) != 1 || anyHost.Routes[0].RouteID != "octoroute_any" {
		t.Fatalf("any-host card %+v", anyHost)
	}
}

func TestBoardShowsFarpRoutesWithLoomplaneLikeIDs(t *testing.T) {
	const apiInstance = "inst_01jbzq7m0dxf4w3y5v8k2n6r9t"
	iid, err := id.Parse(apiInstance)
	if err != nil {
		t.Fatal(err)
	}
	k8sName := kubernetes.WorkloadName(iid)
	in := mockBoard(t)
	in.Gateway.Provider, in.Gateway.Namespace, in.Gateway.FarpHostnames = ProviderKubernetes, "apps", nil
	apiBinding := serviceBinding(t, "api", "conn_a", planning.ServiceConfig{Port: 8080, Farp: farpOptIn(map[string]string{"farp.openapi.path": "/openapi.json"})})
	apiBinding.ExternalID = apiInstance
	in.Bindings = []*planning.Binding{gatewayBinding(t), apiBinding}
	// Discovery named the route after the Service, which starts with lp-.
	in.Live.Routes = liveEntries(k8sName+"-orders", "kubernetes", "/v1/orders/*", k8sName, []string{"GET"})
	in.Live.Upstreams = []UpstreamInfo{upstream(k8sName, 8080)}
	in.Routes = nil
	row := rowAt(t, card(t, BuildBoard(in), ""), "/v1/orders/*", "farp")
	if row.Service != "api" || row.Port != 8080 {
		t.Fatalf("row %+v", row)
	}
	// One origin of Loomplane's and one of FARP's is FARP's.
	in.Live.Routes = append(liveEntries("lp-mixed", "config", "/mixed", "api", []string{"GET"}),
		liveEntries("lp-mixed", "farp", "/mixed", "api", []string{"POST"})...)
	rowAt(t, card(t, BuildBoard(in), ""), "/mixed", "farp")
}

func TestBoardBringsTheFarpRouteBackWhenTheManualRouteGoes(t *testing.T) {
	in := mockBoard(t)
	in.Routes = slices.DeleteFunc(in.Routes, func(r *Route) bool { return r.ID == "octoroute_auth" })
	b := BuildBoard(in)
	api := card(t, b, "api.acme.dev")
	auth := rowAt(t, api, "/auth/*", "farp")
	if auth.Service != "auth" || auth.Port != 9000 || auth.Overrides {
		t.Fatalf("FARP /auth/* row %+v", auth)
	}
	for _, c := range b.Hosts {
		for _, r := range c.Routes {
			if r.Overrides {
				t.Fatalf("row %+v on %q overrides with no manual route on /auth/*", r, c.Hostname)
			}
		}
	}
	if len(api.Routes) != 4 || b.Counts != (SourceCounts{All: 6, Farp: 4, Here: 2}) {
		t.Fatalf("api.acme.dev %+v, counts %+v", api.Routes, b.Counts)
	}
}
