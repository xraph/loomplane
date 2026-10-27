package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xraph/loomplane/pkg/manifest"
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

func TestEditManifestSendsLinkUnlinkAndSetUse(t *testing.T) {
	var got map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/projects/p1/manifest/edits" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"revision":2}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "lp_live_k")
	cases := map[string]ManifestEdit{
		"link":    {Link: &ServiceLink{Service: "api", Need: "fabriq", Options: map[string]string{"tenant": "acme"}}},
		"unlink":  {Unlink: &ServiceLink{Service: "api", Need: "fabriq"}},
		"set_use": {SetUse: &ResourceUse{Resource: "fabriq", Dependency: "redis", Use: manifest.Use{Resource: "cache"}}},
	}
	want := map[string]string{
		"link":    `{"service":"api","need":"fabriq","options":{"tenant":"acme"}}`,
		"unlink":  `{"service":"api","need":"fabriq"}`,
		"set_use": `{"resource":"fabriq","dependency":"redis","use":{"resource":"cache"}}`,
	}
	for key, edit := range cases {
		got = nil
		if _, err := c.EditManifest(t.Context(), "p1", edit); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || string(got[key]) != want[key] {
			t.Errorf("%s: sent %s, want {%q: %s}", key, got, key, want[key])
		}
	}
}

func TestActorNameNamesTheAutomation(t *testing.T) {
	for id, want := range map[string]string{
		"loomplane:aorg_1": "Loomplane automation",
		"ausr_1":           "ausr_1",
		"loomplane:":       "loomplane:",
		"":                 "",
	} {
		if got := ActorName(id); got != want {
			t.Errorf("ActorName(%q) = %q, want %q", id, got, want)
		}
	}
}
