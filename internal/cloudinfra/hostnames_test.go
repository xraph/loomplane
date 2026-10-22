package cloudinfra

import (
	"errors"
	"regexp"
	"testing"
	"time"
)

func TestValidateRouteHostnameKeepsClaimsExact(t *testing.T) {
	cf := &Zone{Name: "acme.dev", Mode: "managed", Provider: "cloudflare", ChallengeConnectionID: "cf-dns", VerifiedAt: time.Now()}
	if _, err := NormalizeHostname("*.acme.dev"); !errors.Is(err, ErrInvalid) {
		t.Fatal("claim path accepts wildcards")
	}
	if h, err := ValidateRouteHostname(cf, "*.API.acme.dev."); err != nil || h.Hostname != "*.api.acme.dev" || !h.Wildcard || h.Base != "api.acme.dev" {
		t.Fatalf("%+v %v", h, err)
	}
	if h, err := ValidateRouteHostname(cf, "*.acme.dev"); err != nil || h.Hostname != "*.acme.dev" || h.Base != "acme.dev" {
		t.Fatalf("wildcard over the apex: %+v %v", h, err)
	}
	if h, err := ValidateRouteHostname(cf, "API.acme.dev"); err != nil || h.Hostname != "api.acme.dev" || h.Wildcard {
		t.Fatalf("exact: %+v %v", h, err)
	}
	for _, bad := range []string{"*.evil.dev", "a.*.acme.dev", "**.acme.dev", "*acme.dev", "*.*.acme.dev", "*", "*.", "api.evil.dev", "api.acme.dev:443"} {
		if _, err := ValidateRouteHostname(cf, bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	noCred := *cf
	noCred.ChallengeConnectionID = ""
	if _, err := ValidateRouteHostname(&noCred, "*.acme.dev"); !errors.Is(err, ErrWildcardUnsupported) {
		t.Fatal(err)
	}
	external := *cf
	external.Mode, external.Provider = "external", ""
	if _, err := ValidateRouteHostname(&external, "*.acme.dev"); !errors.Is(err, ErrWildcardUnsupported) {
		t.Fatal(err)
	}
	unverified := *cf
	unverified.VerifiedAt = time.Time{}
	if _, err := ValidateRouteHostname(&unverified, "*.acme.dev"); !errors.Is(err, ErrWildcardUnsupported) {
		t.Fatal(err)
	}
	// An exact hostname needs no challenge credential.
	if _, err := ValidateRouteHostname(&noCred, "api.acme.dev"); err != nil {
		t.Fatal(err)
	}
}

func TestRouteProbeHostnameForWildcard(t *testing.T) {
	route := Route{ID: "route_1", Input: RouteInput{Hostname: "*.acme.dev"}}
	got := routeProbeHostname(route)
	if !regexp.MustCompile(`^lp-probe-[0-9a-f]{8}\.acme\.dev$`).MatchString(got) || got != routeProbeHostname(route) {
		t.Fatal(got)
	}
	other := route
	other.ID = "route_2"
	if routeProbeHostname(other) == got {
		t.Fatal("two routes share a probe hostname")
	}
	route.Input.Hostname = "api.acme.dev"
	if routeProbeHostname(route) != "api.acme.dev" {
		t.Fatal("an exact hostname is probed as itself")
	}
}
