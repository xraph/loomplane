package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/fleet"
)

func TestFleetOperationClientPinsAndScope(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer synthetic" || !strings.HasPrefix(r.URL.EscapedPath(), "/api/v1/projects/project%2Fid/fleet/") {
			t.Errorf("unscoped request %s", r.URL.EscapedPath())
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/active-operation"):
			if r.Method != http.MethodGet {
				t.Errorf("method %s", r.Method)
			}
		case strings.HasSuffix(r.URL.Path, "/operations"):
			if r.Method != http.MethodPost {
				t.Errorf("method %s", r.Method)
			}
			var in fleet.ActionInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.IdempotencyKey != "stable-request" || in.ExpectedRevision != 7 || in.ExpectedGeneration != 3 || in.Suspend == nil {
				t.Errorf("pins %+v", in)
			}
			w.WriteHeader(http.StatusAccepted)
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			if r.Method != http.MethodPost || !strings.Contains(r.URL.EscapedPath(), "operation%2Fid") {
				t.Errorf("cancellation path %s", r.URL.EscapedPath())
			}
			var in struct {
				ExpectedInstanceRevision int64 `json:"expected_instance_revision"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.ExpectedInstanceRevision != 7 {
				t.Errorf("revision %d", in.ExpectedInstanceRevision)
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if _, err := w.Write([]byte(`{"id":"operation","instance_id":"instance","status":"waiting_approval"}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	c := New(server.URL, "synthetic")
	op, err := c.GetActiveFleetOperation(t.Context(), "project/id", "instance/id")
	if err != nil || op.ID != "operation" {
		t.Fatalf("active=%+v %v", op, err)
	}
	for range 2 {
		op, err = c.RequestFleetInstanceAction(t.Context(), "project/id", "instance/id", fleet.ActionInput{Kind: "suspend", IdempotencyKey: "stable-request", ExpectedRevision: 7, ExpectedGeneration: 3, Suspend: &fleet.SuspendInput{}})
		if err != nil || op.Status != "waiting_approval" {
			t.Fatalf("action=%+v %v", op, err)
		}
	}
	op, err = c.CancelFleetOperation(t.Context(), "project/id", "operation/id", 7)
	if err != nil || op.ID != "operation" {
		t.Fatalf("cancel=%+v %v", op, err)
	}
	if calls != 4 {
		t.Fatalf("calls=%d", calls)
	}
}
