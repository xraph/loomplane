package octopusloom

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func fakeAdmin(f *fakeOctopus) *Admin {
	return NewAdmin(f.channel(), []byte(fakeAdminToken), []byte(fakeFarpToken))
}

func TestAdminSendsTheTokenHeaderOnly(t *testing.T) {
	ctx := context.Background()
	f := newFakeOctopus(t)
	a := fakeAdmin(f)

	if _, err := a.SystemInfo(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.PutUpstream(ctx, "lp-auth-9000", UpstreamBody{Strategy: "round_robin", Instances: []UpstreamInstance{{ID: "lp-auth-0", Address: "auth", Port: 9000}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Upstreams(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.PutRoute(ctx, "lp-octoroute_a", RouteBody{Path: "/auth", Methods: []string{}, Hosts: []string{}, Upstream: "lp-auth-9000"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Routes(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteRoute(ctx, "lp-octoroute_a"); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteUpstream(ctx, "lp-auth-9000"); err != nil {
		t.Fatal(err)
	}
	if err := a.PutFarpBinding(ctx, []string{"api.acme.dev"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.FarpBinding(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.FarpServices(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.farp(ctx, http.MethodDelete, "/_farp/v1/deregister/lp-svc-billing", nil, nil, http.StatusNoContent, http.StatusNotFound); err != nil {
		t.Fatal(err)
	}

	var admin, farp int
	for _, r := range f.log() {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("%s %s sent Authorization", r.Method, r.Path)
		}
		switch {
		case strings.HasPrefix(r.Path, "/admin/"):
			admin++
			if got := r.Header.Values("X-Octopus-Admin-Token"); len(got) != 1 || got[0] != fakeAdminToken {
				t.Errorf("%s %s admin token header = %q", r.Method, r.Path, got)
			}
			if r.Header.Get("X-Octopus-Farp-Token") != "" {
				t.Errorf("%s %s sent the FARP token to the admin API", r.Method, r.Path)
			}
		case strings.HasPrefix(r.Path, "/_farp/"):
			farp++
			if got := r.Header.Values("X-Octopus-Farp-Token"); len(got) != 1 || got[0] != fakeFarpToken {
				t.Errorf("%s %s FARP token header = %q", r.Method, r.Path, got)
			}
			if r.Header.Get("X-Octopus-Admin-Token") != "" {
				t.Errorf("%s %s sent the admin token to FARP", r.Method, r.Path)
			}
		}
		if r.Body != "" && r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s %s Content-Type = %q", r.Method, r.Path, r.Header.Get("Content-Type"))
		}
	}
	if admin != 10 || farp != 1 {
		t.Fatalf("saw %d admin and %d FARP requests, want 10 and 1", admin, farp)
	}

	// A failure whose body echoes the token keeps the token out of the
	// error and reads at most 512 bytes of the body.
	f.status["GET /admin/api/system/info"] = http.StatusInternalServerError
	f.failBody = "bad token " + fakeAdminToken + " " + strings.Repeat("y", 2000) + " " + fakeAdminToken
	_, err := a.SystemInfo(ctx)
	var status *StatusError
	if !errors.As(err, &status) || status.Status != http.StatusInternalServerError {
		t.Fatalf("err = %v, want a *StatusError for 500", err)
	}
	if strings.Contains(err.Error(), fakeAdminToken) {
		t.Fatalf("error carries the token: %q", err.Error())
	}
	if len(status.Body) > 512 {
		t.Fatalf("error carries %d bytes of body", len(status.Body))
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error doesn't name the status: %q", err.Error())
	}
	if errors.Is(err, ErrUnavailable) {
		t.Fatal("a 500 isn't Octopus being unreachable")
	}
}

func TestAdminReadsConflictsMissingAndUnreachable(t *testing.T) {
	ctx := context.Background()
	f := newFakeOctopus(t)
	a := fakeAdmin(f)
	f.upstreams["lp-auth-9000"] = UpstreamBody{Strategy: "round_robin"}
	f.conflicts["lp-octoroute_a"] = "manual-1"

	err := a.PutRoute(ctx, "lp-octoroute_a", RouteBody{Path: "/auth", Methods: []string{}, Hosts: []string{}, Upstream: "lp-auth-9000"})
	var conflict *RouteConflictError
	if !errors.As(err, &conflict) || conflict.ID != "lp-octoroute_a" || conflict.Conflict != "manual-1" {
		t.Fatalf("err = %v, want RouteConflictError{lp-octoroute_a, manual-1}", err)
	}

	// Deleting what Octopus doesn't have is done already.
	if err := a.DeleteRoute(ctx, "lp-nope"); err != nil {
		t.Fatalf("DeleteRoute of a missing route: %v", err)
	}
	if err := a.DeleteUpstream(ctx, "lp-nope-1"); err != nil {
		t.Fatalf("DeleteUpstream of a missing upstream: %v", err)
	}

	f.status["GET /admin/api/routes"] = http.StatusServiceUnavailable
	if _, err := a.Routes(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("503: err = %v, want ErrUnavailable", err)
	}
	f.srv.Close()
	if _, err := a.SystemInfo(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed server: err = %v, want ErrUnavailable", err)
	}
}

func TestAdminReadsOctopusRouteAndServiceShapes(t *testing.T) {
	ctx := context.Background()
	f := newFakeOctopus(t)
	a := fakeAdmin(f)
	f.upstreams["lp-auth-9000"] = UpstreamBody{Strategy: "round_robin", Instances: []UpstreamInstance{{ID: "lp-auth-0", Address: "auth", Port: 9000}}}
	f.routes["lp-octoroute_a"] = &fakeRoute{Origin: "config", RouteBody: RouteBody{Path: "/auth/*", Hosts: []string{"api.acme.dev"}, Methods: []string{"GET"}, Upstream: "lp-auth-9000", StripPrefix: "/auth"}}
	f.services = []map[string]any{{
		"name": "billing", "version": "1.2.0", "instance_id": "lp-svc-billing", "schemas_count": 1,
		"capabilities": []string{"rest"},
		// Octopus 0.5 writes registered_at as a Rust SystemTime debug string
		// and updated_at as Unix seconds.
		"registered_at": "SystemTime { tv_sec: 1761040800, tv_nsec: 5 }", "updated_at": "1761044400",
	}}

	info, err := a.SystemInfo(ctx)
	if err != nil || info.Version != "0.5.0" || info.StartTime != "2026-10-21 10:00:00" {
		t.Fatalf("SystemInfo = %+v, %v", info, err)
	}
	routes, err := a.Routes(ctx)
	if err != nil || len(routes) != 1 {
		t.Fatalf("Routes = %+v, %v", routes, err)
	}
	r := routes[0]
	if r.RouteID == nil || *r.RouteID != "lp-octoroute_a" || r.Origin != "config" || r.Host == nil || *r.Host != "api.acme.dev" ||
		r.Method != "GET" || r.Path != "/auth/*" || r.Upstream != "lp-auth-9000" || r.StripPrefix == nil || *r.StripPrefix != "/auth" {
		t.Fatalf("route = %+v", r)
	}
	ups, err := a.Upstreams(ctx)
	if err != nil || len(ups) != 1 || ups[0].Name != "lp-auth-9000" || len(ups[0].Instances) != 1 || ups[0].Instances[0] != (UpstreamInstance{ID: "lp-auth-0", Address: "auth", Port: 9000}) {
		t.Fatalf("Upstreams = %+v, %v", ups, err)
	}
	services, err := a.FarpServices(ctx)
	if err != nil || len(services) != 1 {
		t.Fatalf("FarpServices = %+v, %v", services, err)
	}
	s := services[0]
	if s.Name != "billing" || s.InstanceID != "lp-svc-billing" || s.RegisteredAt.Unix() != 1761040800 || s.UpdatedAt.Unix() != 1761044400 {
		t.Fatalf("service = %+v", s)
	}
}
