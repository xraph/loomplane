package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/security"
)

// fakeServers is a node adopted as server srv_1 of Cloud cld_1, reached by
// connection con_1.
type fakeServers struct {
	server   *NodeServer
	err      error
	conns    []string
	lookedUp int
}

func (f *fakeServers) Server(_ context.Context, orgID, _ string) (*NodeServer, error) {
	f.lookedUp++
	if orgID != "org_1" {
		return nil, ErrNoServer
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.server == nil {
		return nil, ErrNoServer
	}
	c := *f.server
	return &c, nil
}

func (f *fakeServers) Connections(context.Context, string, string) ([]string, error) {
	return f.conns, nil
}

type fakePlaced struct {
	bindings []PlacedBinding
	policies []BackupPolicyRef
	// asked is what BindingsOn was called with.
	asked struct {
		conns  []string
		server string
	}
}

func (f *fakePlaced) BindingsOn(_ context.Context, _ string, conns []string, server string) ([]PlacedBinding, error) {
	f.asked.conns, f.asked.server = conns, server
	return f.bindings, nil
}

func (f *fakePlaced) BackupPolicies(context.Context, string, []string) ([]BackupPolicyRef, error) {
	return f.policies, nil
}

func cfg(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func workloadRig(t *testing.T) (*rig, *fakeServers, *fakePlaced) {
	t.Helper()
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	if err := r.store.Create(context.Background(), &Node{ID: "node_1", OrgID: "org_1", Name: "web-1", Status: StatusOnline}); err != nil {
		t.Fatal(err)
	}
	servers := &fakeServers{server: &NodeServer{CloudID: "cld_1", ServerID: "srv_1"}, conns: []string{"con_1"}}
	placed := &fakePlaced{}
	r.svc.Servers, r.svc.Placed = servers, placed
	return r, servers, placed
}

func TestWorkloadsFlagsDatabaseWithoutBackup(t *testing.T) {
	r, _, placed := workloadRig(t)
	placed.bindings = []PlacedBinding{
		{ID: "bnd_api", ProjectID: "prj_1", EnvironmentID: "env_1", Environment: "production", Subject: "service", Name: "api", Config: cfg(t, map[string]any{"provider": "server"})},
		{ID: "bnd_db", ProjectID: "prj_1", EnvironmentID: "env_1", Environment: "production", Subject: "resource", Name: "db", Config: cfg(t, map[string]any{"kind": "postgres"})},
		{ID: "bnd_cache", ProjectID: "prj_1", EnvironmentID: "env_1", Environment: "production", Subject: "resource", Name: "cache", Config: cfg(t, map[string]any{"kind": "redis"})},
		// A composed resource runs nothing itself; its components do.
		{ID: "bnd_wiki", ProjectID: "prj_1", EnvironmentID: "env_1", Environment: "production", Subject: "resource", Name: "wiki", Config: cfg(t, map[string]any{"kind": "wiki", "composition": map[string]any{"kind": "wiki"}})},
		{ID: "bnd_wiki_db", ProjectID: "prj_1", EnvironmentID: "env_1", Environment: "production", Subject: "resource", Name: "wiki--db",
			Config: cfg(t, map[string]any{"component": map[string]any{"resource": "wiki", "protection": "native", "component": map[string]any{"role": "process"}}})},
		{ID: "bnd_wiki_web", ProjectID: "prj_1", EnvironmentID: "env_1", Environment: "production", Subject: "resource", Name: "wiki--web",
			Config: cfg(t, map[string]any{"component": map[string]any{"resource": "wiki", "protection": "none", "component": map[string]any{"role": "process"}}})},
		{ID: "bnd_wiki_migrate", ProjectID: "prj_1", EnvironmentID: "env_1", Environment: "production", Subject: "resource", Name: "wiki--migrate",
			Config: cfg(t, map[string]any{"component": map[string]any{"resource": "wiki", "protection": "none", "component": map[string]any{"role": "job"}}})},
	}
	// The cache is backed up, and so is the composed wiki; the db isn't.
	// A policy for db in another environment counts for nothing here.
	placed.policies = []BackupPolicyRef{{EnvironmentID: "env_1", Resource: "cache"}, {EnvironmentID: "env_1", Resource: "wiki"}, {EnvironmentID: "env_2", Resource: "db"}}

	got, err := r.svc.Workloads(context.Background(), admin, "node_1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(placed.asked.conns, []string{"con_1"}) || placed.asked.server != "srv_1" {
		t.Fatalf("asked for bindings on %+v", placed.asked)
	}
	want := []NodeWorkload{
		{BindingID: "bnd_api", ProjectID: "prj_1", Environment: "production", Service: "api", Kind: WorkloadService},
		{BindingID: "bnd_db", ProjectID: "prj_1", Environment: "production", Service: "db", Kind: WorkloadDatabase, HasBackupPolicy: false},
		{BindingID: "bnd_cache", ProjectID: "prj_1", Environment: "production", Service: "cache", Kind: WorkloadDatabase, HasBackupPolicy: true},
		{BindingID: "bnd_wiki_db", ProjectID: "prj_1", Environment: "production", Service: "wiki--db", Kind: WorkloadDatabase, HasBackupPolicy: true},
		{BindingID: "bnd_wiki_web", ProjectID: "prj_1", Environment: "production", Service: "wiki--web", Kind: WorkloadComponent, HasBackupPolicy: true},
		{BindingID: "bnd_wiki_migrate", ProjectID: "prj_1", Environment: "production", Service: "wiki--migrate", Kind: WorkloadJob, HasBackupPolicy: true},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("workloads\n got %+v\nwant %+v", got, want)
	}
}

func TestWorkloadsNeedNodeReadAndTheOrg(t *testing.T) {
	r, _, _ := workloadRig(t)
	r.svc.Authz = denyAll{}
	if _, err := r.svc.Workloads(context.Background(), admin, "node_1"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("without nodes:read: %v", err)
	}
	r, _, _ = workloadRig(t)
	other := security.Principal{UserID: "usr_9", OrgID: "org_2"}
	if _, err := r.svc.Workloads(context.Background(), other, "node_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org's node: %v", err)
	}
}

// A node that is only a connection, never adopted, still lists what was
// deployed through it, and one that is neither runs nothing.
func TestWorkloadsWithoutAServer(t *testing.T) {
	r, servers, placed := workloadRig(t)
	servers.server = nil
	placed.bindings = []PlacedBinding{{ID: "bnd_api", Subject: "service", Name: "api"}}
	got, err := r.svc.Workloads(context.Background(), admin, "node_1")
	if err != nil || len(got) != 1 || placed.asked.server != "" {
		t.Fatalf("workloads %+v, %v; asked %+v", got, err, placed.asked)
	}
	servers.conns = nil
	placed.asked.conns = []string{"untouched"}
	got, err = r.svc.Workloads(context.Background(), admin, "node_1")
	if err != nil || got == nil || len(got) != 0 || placed.asked.conns[0] != "untouched" {
		t.Fatalf("workloads %+v, %v", got, err)
	}
}

func TestDrainTargetResolvesTheServer(t *testing.T) {
	r, servers, _ := workloadRig(t)
	n, srv, err := r.svc.DrainTarget(context.Background(), admin, "node_1")
	if err != nil || n.ID != "node_1" || srv.CloudID != "cld_1" || srv.ServerID != "srv_1" {
		t.Fatalf("target %+v %+v %v", n, srv, err)
	}
	servers.server = nil
	if _, _, err = r.svc.DrainTarget(context.Background(), admin, "node_1"); !errors.Is(err, ErrNoServer) {
		t.Fatalf("no server: %v", err)
	}
	r.svc.Authz = fakeAuthz{manage: false}
	if _, _, err = r.svc.DrainTarget(context.Background(), admin, "node_1"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("without nodes:manage: %v", err)
	}
	if _, _, err = r.svc.DrainTarget(context.Background(), security.Principal{UserID: "usr_1", OrgID: "org_1", ReadOnly: true}, "node_1"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("read-only: %v", err)
	}
}

// An online node whose server drains reads as draining; the stored status
// stays online, and an offline one stays offline.
func TestDrainingIsWorkedOutOnRead(t *testing.T) {
	r, servers, _ := workloadRig(t)
	servers.server.Draining = true
	n, err := r.svc.Get(context.Background(), admin, "node_1")
	if err != nil || n.Status != StatusDraining {
		t.Fatalf("get: %+v %v", n, err)
	}
	list, err := r.svc.List(context.Background(), admin)
	if err != nil || len(list) != 1 || list[0].Status != StatusDraining {
		t.Fatalf("list: %+v %v", list, err)
	}
	if stored, _ := r.store.GetByID(context.Background(), "node_1"); stored.Status != StatusOnline {
		t.Fatalf("stored %s", stored.Status)
	}
	r.store.mu.Lock()
	r.store.nodes["node_1"].Status = StatusOffline
	r.store.mu.Unlock()
	if n, _ = r.svc.Get(context.Background(), admin, "node_1"); n.Status != StatusOffline {
		t.Fatalf("offline read as %s", n.Status)
	}
	// A failed lookup leaves the status alone.
	r.store.mu.Lock()
	r.store.nodes["node_1"].Status = StatusOnline
	r.store.mu.Unlock()
	servers.err = errors.New("database gone")
	if n, err = r.svc.Get(context.Background(), admin, "node_1"); err != nil || n.Status != StatusOnline {
		t.Fatalf("failed lookup: %+v %v", n, err)
	}
}

type denyAll struct{}

func (denyAll) Allowed(context.Context, security.Principal, security.Permission) (bool, error) {
	return false, nil
}
