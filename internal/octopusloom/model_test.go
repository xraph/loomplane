package octopusloom

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func routeInput(path string) RouteInput {
	return RouteInput{Hostname: "api.acme.dev", Path: path, Service: "billing", Port: 8080}
}

func TestNormalizeRouteInputPaths(t *testing.T) {
	for _, path := range []string{"/", "/*", "/auth", "/auth/*", "/v1/orders/*"} {
		got, err := NormalizeRouteInput(routeInput(path))
		if err != nil {
			t.Errorf("%q refused: %v", path, err)
			continue
		}
		if got.Path != path {
			t.Errorf("%q became %q", path, got.Path)
		}
	}
	for _, tc := range []struct{ path, message string }{
		{"auth", "starts with /"},
		{"/a/*/b", "*"},
		{"/a*", "*"},
		{"/a/", "Drop the trailing slash, or end with /* to match everything under it."},
		{"/a//b", "//"},
		{"/a/../b", ".."},
		{"/a?x", "?"},
		{"/a#x", "#"},
		{"/" + strings.Repeat("a", 512), "512"},
	} {
		_, err := NormalizeRouteInput(routeInput(tc.path))
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%.20q: want ErrInvalid, got %v", tc.path, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.message) {
			t.Errorf("%.20q: message %q lacks %q", tc.path, err.Error(), tc.message)
		}
	}
	// A path of exactly 512 characters is fine.
	if _, err := NormalizeRouteInput(routeInput("/" + strings.Repeat("a", 511))); err != nil {
		t.Errorf("512 characters refused: %v", err)
	}
	// The exact message for a trailing slash is the whole error text.
	_, err := NormalizeRouteInput(routeInput("/a/"))
	if err == nil || err.Error() != "Drop the trailing slash, or end with /* to match everything under it." {
		t.Errorf("trailing slash message: %v", err)
	}
}

func TestNormalizeRouteInputFields(t *testing.T) {
	got, err := NormalizeRouteInput(RouteInput{Hostname: " API.Acme.Dev ", Path: "/x", Service: "billing", Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	if got.Hostname != "api.acme.dev" {
		t.Errorf("hostname %q", got.Hostname)
	}
	if got.Methods == nil || len(got.Methods) != 0 {
		t.Errorf("methods should be an empty slice, got %#v", got.Methods)
	}
	anyHost, err := NormalizeRouteInput(RouteInput{Path: "/", Service: "billing", Port: 80})
	if err != nil || anyHost.Hostname != "" {
		t.Errorf("any-host route: %v %q", err, anyHost.Hostname)
	}
	for name, in := range map[string]RouteInput{
		"hostname with a path": {Hostname: "a.dev/x", Path: "/", Service: "s", Port: 80},
		"hostname with a port": {Hostname: "a.dev:80", Path: "/", Service: "s", Port: 80},
		"no service":           {Path: "/", Port: 80},
		"bad service":          {Path: "/", Service: "Bill_ing", Port: 80},
		"port zero":            {Path: "/", Service: "s", Port: 0},
		"port too big":         {Path: "/", Service: "s", Port: 65536},
	} {
		if _, err := NormalizeRouteInput(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

func TestNormalizeRouteInputMethods(t *testing.T) {
	in := routeInput("/x")
	in.Methods = []string{"post", "GET", "get"}
	got, err := NormalizeRouteInput(in)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"GET", "POST"}; !reflect.DeepEqual(got.Methods, want) {
		t.Errorf("methods %v, want %v", got.Methods, want)
	}
	in.Methods = []string{"options", "delete", "patch", "put", "head", "get", "post"}
	got, err = NormalizeRouteInput(in)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}; !reflect.DeepEqual(got.Methods, want) {
		t.Errorf("methods %v, want %v", got.Methods, want)
	}
	in.Methods = []string{"PATCHY"}
	_, err = NormalizeRouteInput(in)
	if !errors.Is(err, ErrInvalid) || err.Error() != "PATCHY isn't an HTTP method Octopus routes" {
		t.Errorf("PATCHY: %v", err)
	}
}

func TestNormalizeRouteInputStripPrefix(t *testing.T) {
	for _, path := range []string{"/", "/*"} {
		in := routeInput(path)
		in.StripPrefix = true
		got, err := NormalizeRouteInput(in)
		if err != nil || got.StripPrefix {
			t.Errorf("%q: strip prefix should drop to false, got %v (%v)", path, got.StripPrefix, err)
		}
	}
	in := routeInput("/auth/*")
	in.StripPrefix = true
	if got, err := NormalizeRouteInput(in); err != nil || !got.StripPrefix {
		t.Errorf("/auth/* keeps strip prefix: %v %v", got.StripPrefix, err)
	}
	for name, note := range map[string]string{
		"121 characters": strings.Repeat("n", 121),
		"a newline":      "one\ntwo",
		"a return":       "one\rtwo",
	} {
		noted := routeInput("/x")
		noted.Note = note
		if _, err := NormalizeRouteInput(noted); !errors.Is(err, ErrInvalid) {
			t.Errorf("note with %s: want ErrInvalid, got %v", name, err)
		}
	}
	in = routeInput("/x")
	in.Note = strings.Repeat("n", 120)
	if _, err := NormalizeRouteInput(in); err != nil {
		t.Errorf("120 characters refused: %v", err)
	}
}

func TestRouteInputRejectsDuplicatePath(t *testing.T) {
	existing := []*Route{{ID: "octoroute_a", Hostname: "api.acme.dev", Path: "/auth/*"}}
	in := RouteInput{Hostname: "api.acme.dev", Path: "/auth/*"}
	err := CheckRouteConflicts(existing, "", in)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if want := "A route for /auth/* on api.acme.dev already exists. Edit it instead."; err.Error() != want {
		t.Errorf("message %q, want %q", err.Error(), want)
	}
	for _, other := range []RouteInput{
		{Hostname: "other.acme.dev", Path: "/auth/*"},
		{Hostname: "", Path: "/auth/*"},
	} {
		if err := CheckRouteConflicts(existing, "", other); err != nil {
			t.Errorf("%+v: %v", other, err)
		}
	}
	// A route listed under the any-host name conflicts only with any-host.
	anyHost := []*Route{{ID: "octoroute_b", Hostname: "", Path: "/x"}}
	if err := CheckRouteConflicts(anyHost, "", RouteInput{Hostname: "", Path: "/x"}); !errors.Is(err, ErrConflict) {
		t.Errorf("any-host duplicate: %v", err)
	} else if want := "A route for /x on any host already exists. Edit it instead."; err.Error() != want {
		t.Errorf("message %q, want %q", err.Error(), want)
	}
}

func TestRouteInputRejectsCoveredBasePath(t *testing.T) {
	wild := []*Route{{ID: "octoroute_a", Hostname: "api.acme.dev", Path: "/auth/*"}}
	err := CheckRouteConflicts(wild, "", RouteInput{Hostname: "api.acme.dev", Path: "/auth"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if want := "/auth/* already answers /auth on api.acme.dev. Edit that route instead."; err.Error() != want {
		t.Errorf("message %q, want %q", err.Error(), want)
	}
	exact := []*Route{{ID: "octoroute_a", Hostname: "api.acme.dev", Path: "/auth"}}
	err = CheckRouteConflicts(exact, "", RouteInput{Hostname: "api.acme.dev", Path: "/auth/*"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if want := "/auth already has its own route on api.acme.dev, and /auth/* would answer it too. Remove or edit /auth first."; err.Error() != want {
		t.Errorf("message %q, want %q", err.Error(), want)
	}
	// Another host is unaffected, and so is a different base path.
	if err := CheckRouteConflicts(wild, "", RouteInput{Hostname: "other.dev", Path: "/auth"}); err != nil {
		t.Errorf("other host: %v", err)
	}
	if err := CheckRouteConflicts(wild, "", RouteInput{Hostname: "api.acme.dev", Path: "/authz"}); err != nil {
		t.Errorf("sibling path: %v", err)
	}
	// "/*" answers "/".
	root := []*Route{{ID: "octoroute_r", Hostname: "api.acme.dev", Path: "/*"}}
	if err := CheckRouteConflicts(root, "", RouteInput{Hostname: "api.acme.dev", Path: "/"}); !errors.Is(err, ErrConflict) {
		t.Errorf("/ under /*: %v", err)
	}
	// Updating a route against itself is never a conflict.
	if err := CheckRouteConflicts(wild, "octoroute_a", RouteInput{Hostname: "api.acme.dev", Path: "/auth/*"}); err != nil {
		t.Errorf("self update: %v", err)
	}
	if err := CheckRouteConflicts(wild, "octoroute_a", RouteInput{Hostname: "api.acme.dev", Path: "/auth"}); err != nil {
		t.Errorf("self update to base path: %v", err)
	}
}

func TestRoutePrefixAndWildcard(t *testing.T) {
	for _, tc := range []struct {
		path, prefix string
		wildcard     bool
	}{
		{"/", "/", false},
		{"/*", "/", true},
		{"/x", "/x", false},
		{"/x/*", "/x", true},
		{"/v1/orders/*", "/v1/orders", true},
	} {
		r := Route{Path: tc.path}
		if r.Prefix() != tc.prefix || r.Wildcard() != tc.wildcard {
			t.Errorf("%q: prefix %q wildcard %v", tc.path, r.Prefix(), r.Wildcard())
		}
	}
}

func TestOctopusNames(t *testing.T) {
	if got, want := OctopusRouteIDs("octoroute_x", true), []string{"lp-octoroute_x", "lp-octoroute_x-base"}; !reflect.DeepEqual(got, want) {
		t.Errorf("wildcard ids %v", got)
	}
	if got, want := OctopusRouteIDs("octoroute_x", false), []string{"lp-octoroute_x"}; !reflect.DeepEqual(got, want) {
		t.Errorf("exact ids %v", got)
	}
	if got := UpstreamName("billing", 8080); got != "lp-billing-8080" {
		t.Errorf("upstream %q", got)
	}
	if got := FarpInstanceID("billing"); got != "lp-svc-billing" {
		t.Errorf("instance %q", got)
	}
}

func TestNormalizeRouteInputWildcardHostnames(t *testing.T) {
	for host, want := range map[string]string{
		"*.acme.dev":    "*.acme.dev",
		" *.ACME.dev ":  "*.acme.dev",
		"*.eu.acme.dev": "*.eu.acme.dev",
		"api.acme.dev":  "api.acme.dev",
		"":              "",
	} {
		in := routeInput("/x")
		in.Hostname = host
		got, err := NormalizeRouteInput(in)
		if err != nil {
			t.Errorf("%q refused: %v", host, err)
			continue
		}
		if got.Hostname != want {
			t.Errorf("%q became %q, want %q", host, got.Hostname, want)
		}
	}
	for _, host := range []string{"*", "*.", "*.dev", "a.*.dev", "**.acme.dev", "*acme.dev", "*.*.acme.dev", "*.acme.dev.", ".acme.dev", "*.-acme.dev"} {
		in := routeInput("/x")
		in.Hostname = host
		if _, err := NormalizeRouteInput(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: want ErrInvalid, got %v", host, err)
		}
	}
}

func TestWildcardHostnameNeverConflictsWithExactHost(t *testing.T) {
	existing := []*Route{{ID: "octoroute_a", Hostname: "api.acme.dev", Path: "/auth/*"}}
	if err := CheckRouteConflicts(existing, "", RouteInput{Hostname: "*.acme.dev", Path: "/auth/*"}); err != nil {
		t.Errorf("wildcard against exact host: %v", err)
	}
	wild := []*Route{{ID: "octoroute_w", Hostname: "*.acme.dev", Path: "/auth/*"}}
	if err := CheckRouteConflicts(wild, "", RouteInput{Hostname: "api.acme.dev", Path: "/auth/*"}); err != nil {
		t.Errorf("exact host against wildcard: %v", err)
	}
	if err := CheckRouteConflicts(wild, "", RouteInput{Hostname: "*.acme.dev", Path: "/auth/*"}); !errors.Is(err, ErrConflict) {
		t.Errorf("same wildcard host: %v", err)
	}
}

func TestRouteJSONNamesItsGatewayInSnakeCase(t *testing.T) {
	out, err := json.Marshal(Route{ID: "octoroute_1", GatewayRef: GatewayRef{OrgID: "o", ProjectID: "p", EnvironmentID: "e", Resource: "edge"}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"org_id": "o", "project_id": "p", "environment_id": "e", "resource": "edge"} {
		if got[key] != want {
			t.Errorf("%s = %v in %s", key, got[key], out)
		}
	}
	for _, key := range []string{"OrgID", "ProjectID", "EnvironmentID", "Resource"} {
		if _, ok := got[key]; ok {
			t.Errorf("%s leaked into %s", key, out)
		}
	}
}
