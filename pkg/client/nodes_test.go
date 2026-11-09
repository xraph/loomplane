package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

type seen struct {
	method, path, auth, body string
}

func nodesServer(t *testing.T, status int, reply string) (*httptest.Server, *seen) {
	t.Helper()
	got := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.method, got.path, got.auth, got.body = r.Method, r.URL.Path, r.Header.Get("Authorization"), string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestCreateNodeJoinToken(t *testing.T) {
	srv, got := nodesServer(t, 201, `{"token":"lpj_live_x","expires_at":"2026-11-02T12:00:00Z","command":"curl | sh"}`)
	tok, err := New(srv.URL, "lp_key").CreateNodeJoinToken(t.Context(), NodeJoinScope{
		Labels: map[string]string{"zone": "a"}, Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe}, Edge: true, LifetimeMinutes: 30})
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "POST" || got.path != "/api/v1/nodes/join-tokens" || got.auth != "Bearer lp_key" {
		t.Fatalf("request: %+v", got)
	}
	var body map[string]any
	if err = json.Unmarshal([]byte(got.body), &body); err != nil {
		t.Fatal(err)
	}
	if body["edge"] != true || body["lifetime_minutes"] != float64(30) || body["labels"].(map[string]any)["zone"] != "a" {
		t.Fatalf("body: %s", got.body)
	}
	if tok.Token != "lpj_live_x" || tok.Command != "curl | sh" || !tok.ExpiresAt.Equal(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("token: %+v", tok)
	}
}

func TestNodeJoinSendsNoKey(t *testing.T) {
	srv, got := nodesServer(t, 201, `{"node_id":"node_1","org_id":"org_1","identity_generation":1,"endpoints":["probe"],"cloud_id":"c1"}`)
	res, err := NodeJoin(t.Context(), srv.URL+"/", JoinRequest{Token: "lpj", PublicKey: "AAAA", NodeVersion: "v1", ProtocolVersion: 1,
		Inventory: nodeproto.Inventory{Arch: "arm64"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "POST" || got.path != "/api/v1/nodes/join" || got.auth != "" {
		t.Fatalf("request: %+v", got)
	}
	var body map[string]any
	if err = json.Unmarshal([]byte(got.body), &body); err != nil {
		t.Fatal(err)
	}
	if body["token"] != "lpj" || body["public_key"] != "AAAA" || body["protocol_version"] != float64(1) || body["inventory"].(map[string]any)["arch"] != "arm64" {
		t.Fatalf("body: %s", got.body)
	}
	if res.NodeID != "node_1" || res.OrgID != "org_1" || res.IdentityGeneration != 1 || res.CloudID != "c1" || len(res.Endpoints) != 1 {
		t.Fatalf("result: %+v", res)
	}
}

func TestNodeJoinErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		msg    string
	}{
		{401, "that join token isn't valid; mint a new join token and try again"},
		{426, "update this node"},
		{503, "try again"},
	} {
		srv, _ := nodesServer(t, tc.status, `{"error":"`+tc.msg+`"}`)
		_, err := NodeJoin(t.Context(), srv.URL, JoinRequest{Token: "x"})
		var ce *Error
		if !errors.As(err, &ce) || ce.Status != tc.status || ce.Message != tc.msg {
			t.Fatalf("%d: %v", tc.status, err)
		}
	}
}

func TestNodesListRevokeUpdate(t *testing.T) {
	srv, got := nodesServer(t, 200, `{"nodes":[{"id":"node_1","name":"web-1","status":"online","endpoints":["probe"],"ingress_mode":"none","labels":{},"inventory":{"os":"linux","kernel":"6","arch":"amd64","cpus":2,"memory_mib":1,"disk_gib":1},"last_seen_at":null}]}`)
	c := New(srv.URL, "k")
	ns, err := c.Nodes(context.Background())
	if err != nil || len(ns) != 1 || ns[0].ID != "node_1" || ns[0].Status != "online" || ns[0].Inventory.Arch != "amd64" {
		t.Fatalf("%+v %v", ns, err)
	}
	if got.method != "GET" || got.path != "/api/v1/nodes" {
		t.Fatalf("list: %+v", got)
	}

	if err = c.RevokeNode(t.Context(), "node/1"); err != nil {
		t.Fatal(err)
	}
	if got.method != "POST" || got.path != "/api/v1/nodes/node/1/revoke" && got.path != "/api/v1/nodes/node%2F1/revoke" {
		t.Fatalf("revoke: %+v", got)
	}

	srv2, got2 := nodesServer(t, 200, `{"id":"node_1","name":"db","status":"online"}`)
	name := "db"
	n, err := New(srv2.URL, "k").UpdateNode(t.Context(), "node_1", NodeUpdate{Name: &name, Endpoints: []nodeproto.Endpoint{}})
	if err != nil || n.Name != "db" {
		t.Fatalf("%+v %v", n, err)
	}
	if got2.method != "PATCH" || got2.path != "/api/v1/nodes/node_1" {
		t.Fatalf("update: %+v", got2)
	}
	// An empty list narrows to nothing, so it is sent; an unset field is null.
	var body map[string]any
	if err = json.Unmarshal([]byte(got2.body), &body); err != nil {
		t.Fatal(err)
	}
	if eps, ok := body["endpoints"].([]any); !ok || len(eps) != 0 {
		t.Fatalf("endpoints: %s", got2.body)
	}
	if body["labels"] != nil || body["ingress_mode"] != nil {
		t.Fatalf("unset fields must be null: %s", got2.body)
	}
}
