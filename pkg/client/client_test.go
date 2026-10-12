package client

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_errorsAndLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer lp_live_k" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"error":"that API key isn't valid"}`))
			return
		}
		switch r.URL.Path {
		case "/api/v1/projects":
			_, _ = w.Write([]byte(`{"projects":[{"id":"p1","name":"Mono","slug":"mono"},{"id":"p2","name":"Web","slug":"web"},{"id":"p3","name":"web","slug":"web-2"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"project not found"}}`))
		}
	}))
	defer srv.Close()
	ctx := t.Context()

	_, err := New(srv.URL, "wrong").Projects(ctx)
	if !errors.Is(err, ErrUnauthorized) || err.Error() != "that API key isn't valid" {
		t.Fatalf("a bad key: %v", err)
	}
	c := New(srv.URL+"/", "lp_live_k")
	if _, err := c.Project(ctx, "nope"); !errors.Is(err, ErrNotFound) || err.Error() != "project not found" {
		t.Fatalf("a missing project: %v", err)
	}
	for ref, want := range map[string]string{"p2": "p2", "mono": "p1", "MONO": "p1", "web-2": "p3"} {
		p, err := c.FindProject(ctx, ref)
		if err != nil || p.ID != want {
			t.Errorf("FindProject(%q) = %+v, %v; want %s", ref, p, err, want)
		}
	}
	if _, err := c.FindProject(ctx, "Web"); !errors.Is(err, ErrConflict) {
		t.Errorf("an ambiguous name: %v", err)
	}
	if _, err := c.FindProject(ctx, "api"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown name: %v", err)
	}
}
