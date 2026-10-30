package firecracker

import (
	"errors"
	"testing"
)

func TestRouteRequiresOwnedReadyResourceAndRejectsStaleChange(t *testing.T) {
	h, _, q := testHost(t)
	q.Spec.Port = 8080
	if _, err := h.Apply(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	q.Route = &RouteSpec{ID: "route", Hostname: "api.example.com", Port: 8080}
	if _, err := h.ApplyRoute(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	q.Authority.Generation = 2
	q.Authority.Operation = "new-route"
	q.Route.BlockedPaths = []string{"/admin"}
	if _, err := h.ApplyRoute(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	q.Authority.Generation = 1
	if _, err := h.ApplyRoute(t.Context(), q); !errors.Is(err, ErrConflict) {
		t.Fatal("stale route accepted", err)
	}
	q.Authority.Generation = 3
	q.Scope.Org = "foreign"
	if _, err := h.ApplyRoute(t.Context(), q); !errors.Is(err, ErrConflict) {
		t.Fatal("hostname stolen", err)
	}
}
func TestRouteCannotExposeAnUnreviewedPort(t *testing.T) {
	h, _, q := testHost(t)
	q.Spec.Port = 8080
	if _, err := h.Apply(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	q.Route = &RouteSpec{ID: "route", Hostname: "api.example.com", Port: 9090}
	if _, err := h.ApplyRoute(t.Context(), q); !errors.Is(err, ErrConflict) {
		t.Fatal("unreviewed port accepted", err)
	}
}
