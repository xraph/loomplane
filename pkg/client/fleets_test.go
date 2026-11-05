package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xraph/loomplane/pkg/fleet"
)

func TestFleetClientScopedReads(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer synthetic" {
			t.Errorf("request: %s %s", r.Method, r.URL)
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var body string
		switch r.URL.Path {
		case "/api/v1/projects/project/fleet":
			body = `{"id":"fleet","project_id":"project"}`
		case "/api/v1/projects/project/fleet/instances/instance":
			body = `{"id":"instance","fleet_id":"fleet","status":"waiting_approval"}`
		case "/api/v1/projects/project/fleet/operations/operation":
			body = `{"id":"operation","instance_id":"instance","status":"waiting_approval"}`
		case "/api/v1/projects/project/fleet/instances":
			if r.URL.Query().Get("external_ref") != "ref & value" || r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("after") != "cursor" {
				t.Errorf("query: %s", r.URL.RawQuery)
			}
			body = `{"items":[{"id":"instance"}],"next_cursor":"next"}`
		case "/api/v1/projects/project/fleet/releases/release":
			body = `{"id":"release","fleet_id":"fleet"}`
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	c := New(server.URL, "synthetic")
	f, err := c.GetFleet(t.Context(), "project")
	if err != nil || f.ID != "fleet" {
		t.Fatalf("fleet: %+v %v", f, err)
	}
	i, err := c.GetFleetInstance(t.Context(), "project", "instance")
	if err != nil || i.Status != "waiting_approval" {
		t.Fatalf("instance: %+v %v", i, err)
	}
	op, err := c.GetFleetOperation(t.Context(), "project", "operation")
	if err != nil || op.InstanceID != "instance" {
		t.Fatalf("operation: %+v %v", op, err)
	}
	page, err := c.ListFleetInstances(t.Context(), "project", fleet.ListInstancesInput{Limit: 2, After: "cursor", ExternalRef: "ref & value"})
	if err != nil || len(page.Items) != 1 || page.NextCursor != "next" {
		t.Fatalf("page: %+v %v", page, err)
	}
	release, err := c.GetFleetRelease(t.Context(), "project", "release")
	if err != nil || release.ID != "release" {
		t.Fatalf("release: %+v %v", release, err)
	}
}

func TestFleetClientCreatePreservesRequestIdentity(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/v1/projects/project/fleet/instances" {
			t.Errorf("request: %s %s", r.Method, r.URL)
		}
		var in fleet.CreateInstanceInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		if in.IdempotencyKey != "same-request" || in.ExternalRef != "sample" {
			t.Errorf("input: %+v", in)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		if _, err := w.Write([]byte(`{"instance":{"id":"instance","status":"waiting_approval"},"operation":{"id":"operation","status":"waiting_approval"}}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	c := New(server.URL, "synthetic")
	for range 2 {
		out, err := c.CreateFleetInstance(t.Context(), "project", fleet.CreateInstanceInput{ExternalRef: "sample", IdempotencyKey: "same-request"})
		if err != nil || out.Instance.ID != "instance" || out.Operation.ID != "operation" || out.Instance.Status != "waiting_approval" {
			t.Fatalf("result: %+v %v", out, err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}
