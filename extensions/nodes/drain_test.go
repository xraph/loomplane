package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/security"
)

// fakeDrains is cloud infrastructure's server drain. It records who asked
// for which server, and drains it on apply.
type fakeDrains struct {
	who            security.Principal
	cloud, server  string
	review         string
	err            error
	draining       *bool
	previews, apps int
}

func (f *fakeDrains) PreviewDrain(_ context.Context, p security.Principal, cloud, id string) (*cloudinfra.DrainReview, error) {
	f.previews++
	f.who, f.cloud, f.server = p, cloud, id
	if f.err != nil {
		return nil, f.err
	}
	return &cloudinfra.DrainReview{ServerID: id, ServerRevision: 3, Review: "digest-1", Reservations: 2,
		Usage: &cloudinfra.ServerUsage{ServerID: id, Workloads: []cloudinfra.ServerWorkload{{ProjectID: "prj_1", Service: "api", Phase: "serving"}}}}, nil
}

func (f *fakeDrains) ApplyDrain(_ context.Context, p security.Principal, cloud, id string, in cloudinfra.ApplyInput) (*cloudinfra.Server, error) {
	f.apps++
	f.who, f.cloud, f.server, f.review = p, cloud, id, in.Review
	if f.err != nil {
		return nil, f.err
	}
	if in.Review != "digest-1" {
		return nil, cloudinfra.ErrConflict
	}
	*f.draining = true
	return &cloudinfra.Server{ID: id, CloudID: cloud, Draining: true}, nil
}

// fakeNodeServers says node_1 is server srv_1 of Cloud cld_1.
type fakeNodeServers struct {
	draining bool
	none     bool
}

func (f *fakeNodeServers) Server(_ context.Context, org, node string) (*nodes.NodeServer, error) {
	if f.none || org != "org_1" || node != "node_1" {
		return nil, nodes.ErrNoServer
	}
	return &nodes.NodeServer{CloudID: "cld_1", ServerID: "srv_1", Draining: f.draining}, nil
}

func (f *fakeNodeServers) Connections(context.Context, string, string) ([]string, error) {
	return []string{"con_1"}, nil
}

func drainRig(t *testing.T, denied ...string) (*rig, *fakeDrains, *fakeNodeServers) {
	t.Helper()
	r := newRig(t, denied...)
	r.seed(t)
	r.store.mu.Lock()
	r.store.nodes["node_1"].Status = nodes.StatusOnline
	r.store.mu.Unlock()
	servers := &fakeNodeServers{}
	drains := &fakeDrains{draining: &servers.draining}
	r.ext.svc.Servers = servers
	r.ext.drains = drains
	return r, drains, servers
}

func TestDrainRouteUsesServerDrain(t *testing.T) {
	r, drains, _ := drainRig(t)
	out := r.do(t, http.MethodPost, "/api/v1/nodes/node_1/drain", "", &member)
	if out.Code != http.StatusOK {
		t.Fatalf("drain: %d %s", out.Code, out.Body)
	}
	var review cloudinfra.DrainReview
	if err := json.Unmarshal(out.Body.Bytes(), &review); err != nil || review.Review != "digest-1" || review.ServerID != "srv_1" || review.Reservations != 2 || review.Usage == nil {
		t.Fatalf("review %s (%v)", out.Body, err)
	}
	// The caller's own principal reaches the server drain, which checks
	// its own permission.
	if drains.who != member || drains.cloud != "cld_1" || drains.server != "srv_1" {
		t.Fatalf("previewed %s/%s as %+v", drains.cloud, drains.server, drains.who)
	}

	out = r.do(t, http.MethodPost, "/api/v1/nodes/node_1/drain/apply", `{"review":"stale"}`, &member)
	if out.Code != http.StatusConflict {
		t.Fatalf("stale review: %d %s", out.Code, out.Body)
	}
	out = r.do(t, http.MethodPost, "/api/v1/nodes/node_1/drain/apply", `{"review":"digest-1"}`, &member)
	if out.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", out.Code, out.Body)
	}
	var n nodes.Node
	if err := json.Unmarshal(out.Body.Bytes(), &n); err != nil || n.ID != "node_1" || n.Status != nodes.StatusDraining {
		t.Fatalf("applied %s (%v)", out.Body, err)
	}
	if drains.who != member || drains.server != "srv_1" || drains.review != "digest-1" {
		t.Fatalf("applied %s with %q as %+v", drains.server, drains.review, drains.who)
	}
	// The node reads as draining from now on.
	out = r.do(t, http.MethodGet, "/api/v1/nodes/node_1", "", &member)
	if !strings.Contains(out.Body.String(), `"status":"draining"`) {
		t.Fatalf("get: %s", out.Body)
	}
}

func TestDrainRouteRefusals(t *testing.T) {
	// nodes:manage comes first: without it the server drain is never asked.
	r, drains, _ := drainRig(t, "usr_1:nodes:manage")
	for _, path := range []string{"/api/v1/nodes/node_1/drain", "/api/v1/nodes/node_1/drain/apply"} {
		if out := r.do(t, http.MethodPost, path, `{"review":"digest-1"}`, &member); out.Code != http.StatusForbidden {
			t.Fatalf("%s without nodes:manage: %d %s", path, out.Code, out.Body)
		}
	}
	if drains.previews+drains.apps != 0 {
		t.Fatal("asked the server drain without nodes:manage")
	}

	// The server drain's own refusal (no Cloud manage role) stands.
	r, drains, _ = drainRig(t)
	drains.err = security.ErrForbidden
	if out := r.do(t, http.MethodPost, "/api/v1/nodes/node_1/drain", "", &member); out.Code != http.StatusForbidden {
		t.Fatalf("server drain refused: %d %s", out.Code, out.Body)
	}

	// A node that isn't a server says so.
	r, _, servers := drainRig(t)
	servers.none = true
	out := r.do(t, http.MethodPost, "/api/v1/nodes/node_1/drain", "", &member)
	if out.Code != http.StatusConflict || !strings.Contains(out.Body.String(), "isn't a server in a Cloud") {
		t.Fatalf("no server: %d %s", out.Code, out.Body)
	}

	// Another org's node is not found, and its server is never asked.
	r, drains, _ = drainRig(t)
	other := security.Principal{UserID: "usr_9", OrgID: "org_2"}
	if out = r.do(t, http.MethodPost, "/api/v1/nodes/node_1/drain", "", &other); out.Code != http.StatusNotFound || drains.previews != 0 {
		t.Fatalf("another org's node: %d %s", out.Code, out.Body)
	}
	if out = r.do(t, http.MethodPost, "/api/v1/nodes/node_1/drain/apply", `not json`, &member); out.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", out.Code)
	}
}

type fakePlaced struct{ bindings []nodes.PlacedBinding }

func (f fakePlaced) BindingsOn(context.Context, string, []string, string) ([]nodes.PlacedBinding, error) {
	return f.bindings, nil
}

func (fakePlaced) BackupPolicies(context.Context, string, []string) ([]nodes.BackupPolicyRef, error) {
	return nil, nil
}

func TestWorkloadsRoute(t *testing.T) {
	r, _, _ := drainRig(t)
	r.ext.svc.Placed = fakePlaced{bindings: []nodes.PlacedBinding{{ID: "bnd_db", ProjectID: "prj_1", EnvironmentID: "env_1", Environment: "production", Subject: "resource", Name: "db", Config: json.RawMessage(`{"kind":"postgres"}`)}}}
	out := r.do(t, http.MethodGet, "/api/v1/nodes/node_1/workloads", "", &member)
	if out.Code != http.StatusOK {
		t.Fatalf("workloads: %d %s", out.Code, out.Body)
	}
	var got NodeWorkloadList
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || len(got.Workloads) != 1 || got.Workloads[0].Kind != "database" || got.Workloads[0].HasBackupPolicy {
		t.Fatalf("body %s (%v)", out.Body, err)
	}
	if !strings.Contains(out.Body.String(), `"has_backup_policy":false`) {
		t.Fatalf("the warning's field is missing: %s", out.Body)
	}
	r, _, _ = drainRig(t, "usr_1:nodes:read")
	if out = r.do(t, http.MethodGet, "/api/v1/nodes/node_1/workloads", "", &member); out.Code != http.StatusForbidden {
		t.Fatalf("without nodes:read: %d", out.Code)
	}
}

// fakeAdoptions and fakeServerStore stand in for cloud infrastructure's
// store; fakeConns for the connections service.
type fakeAdoptions struct {
	a   *cloudinfra.NodeAdoption
	err error
}

func (f fakeAdoptions) GetNodeAdoption(context.Context, string, string) (*cloudinfra.NodeAdoption, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.a == nil {
		return nil, cloudinfra.ErrNotFound
	}
	c := *f.a
	return &c, nil
}

type fakeServerStore struct{ srv *cloudinfra.Server }

func (f fakeServerStore) GetServer(_ context.Context, org, cloud, id string) (*cloudinfra.Server, error) {
	if f.srv == nil || f.srv.OrgID != org || f.srv.CloudID != cloud || f.srv.ID != id {
		return nil, cloudinfra.ErrNotFound
	}
	c := *f.srv
	return &c, nil
}

type fakeConns []*connections.Connection

func (f fakeConns) NodeConnections(context.Context, string, string) ([]*connections.Connection, error) {
	return f, nil
}

func TestNodeServersResolvesThroughTheAdoption(t *testing.T) {
	srv := &cloudinfra.Server{ID: "srv_1", OrgID: "org_1", CloudID: "cld_1", ConnectionID: "con_1", Status: "ready", Draining: true}
	s := nodeServers{
		adoptions: fakeAdoptions{a: &cloudinfra.NodeAdoption{NodeID: "node_1", OrgID: "org_1", CloudID: "cld_1", ServerID: "srv_1", Status: cloudinfra.NodeAdoptionApplied}},
		servers:   fakeServerStore{srv: srv},
		conns:     fakeConns{{ID: "con_1"}},
	}
	got, err := s.Server(t.Context(), "org_1", "node_1")
	if err != nil || got.CloudID != "cld_1" || got.ServerID != "srv_1" || !got.Draining {
		t.Fatalf("server %+v %v", got, err)
	}
	for name, change := range map[string]func(*nodeServers){
		"no adoption":             func(s *nodeServers) { s.adoptions = fakeAdoptions{} },
		"adoption without server": func(s *nodeServers) { s.adoptions = fakeAdoptions{a: &cloudinfra.NodeAdoption{CloudID: "cld_1"}} },
		"server gone":             func(s *nodeServers) { s.servers = fakeServerStore{} },
		"server deleted":          func(s *nodeServers) { d := *srv; d.Status = "deleted"; s.servers = fakeServerStore{srv: &d} },
		"another connection":      func(s *nodeServers) { s.conns = fakeConns{{ID: "con_2"}} },
		"no connection any more":  func(s *nodeServers) { s.conns = fakeConns(nil) },
	} {
		c := s
		change(&c)
		if _, serr := c.Server(t.Context(), "org_1", "node_1"); !errors.Is(serr, nodes.ErrNoServer) {
			t.Errorf("%s: %v", name, serr)
		}
	}
	boom := errors.New("database gone")
	c := s
	c.adoptions = fakeAdoptions{err: boom}
	if _, serr := c.Server(t.Context(), "org_1", "node_1"); !errors.Is(serr, boom) {
		t.Fatalf("store failure: %v", serr)
	}
	ids, err := s.Connections(t.Context(), "org_1", "node_1")
	if err != nil || len(ids) != 1 || ids[0] != "con_1" {
		t.Fatalf("connections %v %v", ids, err)
	}
}
