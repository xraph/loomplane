package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/server"
)

// gatewayStore adds binding placement to the in-memory overview store. clouds
// maps a binding name to its Cloud placement; "" means it has none.
type gatewayStore struct {
	*overviewStore
	clouds    map[string]string
	previewed []*RouteOperation
}

func (s *gatewayStore) RouteBindingCloud(_ context.Context, org, project, environment, service string) (string, error) {
	cloud, ok := s.clouds[service]
	if org != "org" || project != "shop" || environment != "prod" || !ok {
		return "", ErrNotFound
	}
	return cloud, nil
}
func (s *gatewayStore) ResolveRouteBinding(_ context.Context, org, project string, in RouteInput) (RouteBinding, error) {
	cloud, ok := s.clouds[in.Service]
	if org != "org" || project != "shop" || in.EnvironmentID != "prod" || !ok || cloud == "" || cloud != in.CloudID {
		return RouteBinding{}, ErrNotFound
	}
	return RouteBinding{BindingID: "binding_" + in.Service, ServerID: "srv_1", ConnectionID: "conn_srv", InstanceID: "instance", ReleaseID: "release", ContainerID: "container", RuntimeID: "daemon", Address: "192.0.2.10"}, nil
}
func (s *gatewayStore) PreviewRoute(_ context.Context, op *RouteOperation) error {
	s.previewed = append(s.previewed, op)
	return nil
}

func gatewayFixture() (*Service, *gatewayStore) {
	svc, base := overviewFixture()
	base.zones = append(base.zones, &Zone{ID: "zone_gw", OrgID: "org", Name: "gateway.dev", Mode: "external", ProofName: "_loomplane.gateway.dev", ProofValue: "proof", ProjectIDs: []string{"shop"}, VerifiedAt: svc.now(), Revision: 1})
	st := &gatewayStore{overviewStore: base, clouds: map[string]string{"octo--gateway": "cloud_1", "loose": ""}}
	svc.Domains, svc.Resolver = st, proofResolver{[]string{"proof"}}
	return svc, st
}

func gatewayInput() RouteInput {
	return RouteInput{ZoneID: "zone_gw", EnvironmentID: "prod", Service: "octo--gateway", Hostname: "api.gateway.dev", Port: 8080}
}

func TestPreviewRouteInfersCloud(t *testing.T) {
	svc, st := gatewayFixture()
	op, err := svc.PreviewRoute(t.Context(), overviewDev, "shop", gatewayInput())
	if err != nil {
		t.Fatal(err)
	}
	if op.Route.Input.CloudID != "cloud_1" || op.Route.Binding.ServerID != "srv_1" || len(st.previewed) != 1 || st.previewed[0].Route.Input.CloudID != "cloud_1" {
		t.Fatalf("stored intent: %+v", op.Route)
	}
	in := gatewayInput()
	in.CloudID = "cloud_other"
	if _, err = svc.PreviewRoute(t.Context(), overviewDev, "shop", in); !errors.Is(err, ErrNotFound) {
		t.Fatal("resolved under a Cloud the binding isn't placed in", err)
	}
	in = gatewayInput()
	in.Service = "missing"
	if _, err = svc.PreviewRoute(t.Context(), overviewDev, "shop", in); !errors.Is(err, ErrNotFound) {
		t.Fatal("inferred a Cloud for a missing binding", err)
	}
	in.Service = "loose"
	_, err = svc.PreviewRoute(t.Context(), overviewDev, "shop", in)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "This service isn't placed through a Cloud, so Loomplane can't route to its server.") {
		t.Fatal("a binding without a Cloud placement", err)
	}
	if len(st.previewed) != 1 {
		t.Fatal("a refused preview was stored", len(st.previewed))
	}
}

func make9Prefixes() []string {
	out := make([]string, 0, 9)
	for i := range 9 {
		out = append(out, fmt.Sprintf("/p%d", i))
	}
	return out
}

func TestPreviewRouteRejectsUnsafeBlockedPrefixes(t *testing.T) {
	for _, bad := range [][]string{
		{"admin"}, {""}, {"/"}, {"/admin/"}, {"/api/admin/"}, {"//admin"}, {"/api//admin"}, {"/%61dmin"}, {"/admin%2F"},
		{"/a*"}, {"/a?b"}, {"/a#b"}, {"/."}, {"/.."}, {"/api/../admin"}, {"/api/./admin"}, {"/admin/.."},
		{"/" + strings.Repeat("a", 128)}, make9Prefixes(), {"/_farp", "/admin/"},
	} {
		err := ValidateBlockedPathPrefixes(bad)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "Blocked path prefixes must look like /admin") {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	for _, good := range [][]string{nil, {}, {"/__farp", "/_farp", "/admin"}, {"/api/v1/admin"}, {"/.well-known"}, {"/a..b"}, {"/" + strings.Repeat("a", 127)}, make9Prefixes()[:8]} {
		if err := ValidateBlockedPathPrefixes(good); err != nil {
			t.Fatalf("%v: %v", good, err)
		}
	}
	svc, st := gatewayFixture()
	in := gatewayInput()
	for _, unsafe := range [][]string{{"/admin", "admin"}, {"/admin/"}} {
		in.BlockedPathPrefixes = unsafe
		if _, err := svc.PreviewRoute(t.Context(), overviewDev, "shop", in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("preview accepted %q: %v", unsafe, err)
		}
	}
	// Duplicates collapse before the count, so this is within the limit.
	in.BlockedPathPrefixes = append([]string{"/admin", "/_farp", "/admin", "/__farp"}, make9Prefixes()[:5]...)
	op, err := svc.PreviewRoute(t.Context(), overviewDev, "shop", in)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string{"/__farp", "/_farp", "/admin"}, make9Prefixes()[:5]...)
	slices.Sort(want)
	if !slices.Equal(op.Route.Input.BlockedPathPrefixes, want) || len(st.previewed) != 1 {
		t.Fatalf("stored prefixes: %q", op.Route.Input.BlockedPathPrefixes)
	}
	in.BlockedPathPrefixes = []string{}
	if op, err = svc.PreviewRoute(t.Context(), overviewDev, "shop", in); err != nil || op.Route.Input.BlockedPathPrefixes != nil {
		t.Fatalf("empty prefixes: %#v %v", op, err)
	}
}

func TestRouteConfigComparesBlockedPrefixes(t *testing.T) {
	route := Route{ID: "r", OrgID: "org", ProjectID: "shop", Input: RouteInput{EnvironmentID: "prod", Service: "octo--gateway", Hostname: "api.gateway.dev", Port: 8080, BlockedPathPrefixes: []string{"/_farp", "/admin"}},
		Binding: RouteBinding{InstanceID: "instance", ReleaseID: "release", ContainerID: "container"}}
	backend := routeBackend(route)
	backend.Address = "10.0.0.2"
	config := func(prefixes ...string) server.IngressConfig {
		return server.IngressConfig{Routes: []server.IngressRoute{{ID: "r", Hostname: "api.gateway.dev", Backend: backend, BlockedPathPrefixes: prefixes}}}
	}
	if !routeConfigMatches(route, config("/_farp", "/admin"), false) {
		t.Fatal("the accepted prefixes did not match")
	}
	for _, edited := range [][]string{nil, {"/_farp"}, {"/_farp", "/admin", "/debug"}} {
		if routeConfigMatches(route, config(edited...), false) {
			t.Fatalf("a proxy config with prefixes %q matched", edited)
		}
	}
}
