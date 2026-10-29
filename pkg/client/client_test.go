package client

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

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

func TestWaitDeploymentStopsWhenPaused(t *testing.T) {
	defer func(every time.Duration) { Every = every }(Every)
	Every = time.Millisecond
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/deployments/d1" {
			t.Errorf("read %s", r.URL.Path)
		}
		if reads.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"id":"d1","status":"running","steps":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"d1","status":"paused","steps":[],"capacity_wait":{"revision":2,"blocks":[{"reason":"hard_limit","resource":"gpus:nvidia-l4","cloud_id":"c","limit":2,"counted":2,"requested":1}],"created_at":"2026-10-27T10:00:00Z"}}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	d, err := New(srv.URL, "k").WaitDeployment(ctx, "d1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Paused() || d.Done() || reads.Load() != 2 {
		t.Fatalf("paused=%v done=%v reads=%d", d.Paused(), d.Done(), reads.Load())
	}
	w := d.CapacityWait
	if w == nil || w.Revision != 2 || len(w.Blocks) != 1 || w.Blocks[0].Resource != "gpus:nvidia-l4" || w.Blocks[0].Limit != 2 || w.CreatedAt.IsZero() {
		t.Fatalf("wait %+v", w)
	}
}

func TestDeploymentDoneIncludesCancelled(t *testing.T) {
	for status, want := range map[string][2]bool{
		"succeeded": {true, false},
		"failed":    {true, false},
		"cancelled": {true, false},
		"running":   {false, false},
		"paused":    {false, true},
	} {
		d := &Deployment{Status: status}
		if d.Done() != want[0] || d.Paused() != want[1] {
			t.Errorf("%s: done=%v paused=%v", status, d.Done(), d.Paused())
		}
	}
}

func TestCapacityWaitRequests(t *testing.T) {
	for name, call := range map[string]func(*Client) (*Deployment, error){
		"resume": func(c *Client) (*Deployment, error) { return c.ResumeCapacity(t.Context(), "d/1", 3) },
		"cancel": func(c *Client) (*Deployment, error) { return c.CancelCapacityWait(t.Context(), "d/1", 3) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.EscapedPath() != "/api/v1/deployments/d%2F1/capacity/"+name {
					t.Errorf("request %s %s", r.Method, r.URL.EscapedPath())
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 1 || body["expected_revision"] != float64(3) {
					t.Errorf("body %v %v", body, err)
				}
				_, _ = w.Write([]byte(`{"id":"d/1","status":"running","steps":[]}`))
			}))
			defer srv.Close()
			d, err := call(New(srv.URL, "k"))
			if err != nil || d.ID != "d/1" || d.Status != "running" {
				t.Fatalf("%+v %v", d, err)
			}
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":409,"error":"deployment capacity wait changed"}`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "k").ResumeCapacity(t.Context(), "d", 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("a stale resume: %v", err)
	}
}

func TestRollbackRequests(t *testing.T) {
	var paths []string
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/p1/environments/production/rollback-target":
			_, _ = w.Write([]byte(`{"environment":"production","target":{"deployment_id":"dep_3","plan_id":"plan_3","kind":"release","status":"succeeded","finished_at":"2026-10-28T10:00:00Z"},"restore":{"deployment_id":"dep_1","plan_id":"plan_1","kind":"promotion","status":"succeeded","finished_at":"2026-10-27T10:00:00Z"},"commits":[{"alias":"web","from":"bbbbbbb","to":"aaaaaaa"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/projects/p1/rollbacks":
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"plan_r","status":"planning","kind":"rollback","rollback":{"reason":"5xx","trigger":"manual","target_deployment_id":"dep_3"},"leftovers":[{"subject":"resource","name":"cache","op":"delete","data_bearing":true,"reason":"Created by the release you're rolling back."}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "k")

	target, err := c.RollbackTarget(t.Context(), "p1", "production", "")
	if err != nil {
		t.Fatal(err)
	}
	if target.Target.DeploymentID != "dep_3" || target.Restore.DeploymentID != "dep_1" || target.Restore.Kind != "promotion" ||
		target.Restore.FinishedAt.IsZero() || len(target.Commits) != 1 || target.Commits[0] != (RollbackCommit{Alias: "web", From: "bbbbbbb", To: "aaaaaaa"}) {
		t.Fatalf("target %+v", target)
	}
	if _, err = c.RollbackTarget(t.Context(), "p1", "production", "dep_3"); err != nil {
		t.Fatal(err)
	}
	plan, err := c.CreateRollback(t.Context(), "p1", NewRollback{Environment: "production", Reason: "5xx", DeploymentID: "dep_3"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != "rollback" || plan.Rollback == nil || plan.Rollback.Reason != "5xx" || len(plan.Leftovers) != 1 || !plan.Leftovers[0].DataBearing {
		t.Fatalf("plan %+v", plan)
	}
	if want := map[string]string{"environment": "production", "reason": "5xx", "deployment_id": "dep_3"}; !maps.Equal(body, want) {
		t.Fatalf("sent %v", body)
	}
	want := []string{
		"GET /api/v1/projects/p1/environments/production/rollback-target",
		"GET /api/v1/projects/p1/environments/production/rollback-target?deployment_id=dep_3",
		"POST /api/v1/projects/p1/rollbacks",
	}
	if !slices.Equal(paths, want) {
		t.Fatalf("requests %v", paths)
	}
}
