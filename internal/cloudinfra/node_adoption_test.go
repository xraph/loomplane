package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/nodes/nodestest"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// adoptionStore keeps operations, servers, pools and node adoptions in
// memory. What adoption doesn't use stays the nil Store and panics.
type adoptionStore struct {
	Store
	mu        sync.Mutex
	locks     map[string]bool
	ops       map[string]*Operation
	servers   map[string]*Server
	pools     []*Pool
	adoptions map[string]*NodeAdoption
	previews  int
	accepts   int
}

func newAdoptionStore() *adoptionStore {
	return &adoptionStore{locks: map[string]bool{}, ops: map[string]*Operation{}, servers: map[string]*Server{}, adoptions: map[string]*NodeAdoption{}}
}

func (s *adoptionStore) WithOperationLock(_ context.Context, id string, run func() error) error {
	s.mu.Lock()
	if s.locks[id] {
		s.mu.Unlock()
		return ErrBusy
	}
	s.locks[id] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.locks, id)
		s.mu.Unlock()
	}()
	return run()
}

func (s *adoptionStore) CreatePreview(_ context.Context, o *Operation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := *o
	s.ops[o.ID] = &copied
	s.previews++
	return nil
}

func (s *adoptionStore) GetOperation(_ context.Context, org, cloud, id string) (*Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.ops[id]
	if !ok || o.OrgID != org || o.CloudID != cloud {
		return nil, ErrNotFound
	}
	copied := *o
	return &copied, nil
}

func (s *adoptionStore) Accept(_ context.Context, o *Operation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := *o
	s.ops[o.ID] = &copied
	s.accepts++
	// The real store claims the host in the same transaction.
	s.servers[o.ServerID] = &Server{ID: o.ServerID, OrgID: o.OrgID, CloudID: o.CloudID, Name: o.Input.Name, Ownership: o.Ownership, Provider: o.Provider, ConnectionID: o.ConnectionID, RuntimeConnectionID: o.ConnectionID, AccountID: o.AccountID, ResourceID: o.ResourceID, Status: "provisioning", Revision: 1, CreationOperationID: o.ID}
	return nil
}

func (s *adoptionStore) GetServer(_ context.Context, org, cloud, id string) (*Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.servers[id]
	if !ok || v.OrgID != org || v.CloudID != cloud {
		return nil, ErrNotFound
	}
	copied := *v
	return &copied, nil
}

func (s *adoptionStore) ListServers(_ context.Context, org, cloud string) ([]*Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*Server{}
	for _, v := range s.servers {
		if v.OrgID == org && v.CloudID == cloud {
			copied := *v
			out = append(out, &copied)
		}
	}
	return out, nil
}

func (s *adoptionStore) ListPools(_ context.Context, org, cloud string) ([]*Pool, error) {
	out := []*Pool{}
	for _, p := range s.pools {
		if p.OrgID == org && p.CloudID == cloud {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *adoptionStore) GetPool(context.Context, string, string, string) (*Pool, error) {
	return nil, ErrNotFound
}

func (s *adoptionStore) SavePool(context.Context, *Pool, int64) error { return ErrInvalid }

func (s *adoptionStore) CreateNodeAdoption(_ context.Context, a *NodeAdoption) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.adoptions[a.NodeID]; ok {
		return nil
	}
	copied := *a
	s.adoptions[a.NodeID] = &copied
	return nil
}

func (s *adoptionStore) GetNodeAdoption(_ context.Context, org, nodeID string) (*NodeAdoption, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.adoptions[nodeID]
	if !ok || a.OrgID != org {
		return nil, ErrNotFound
	}
	copied := *a
	return &copied, nil
}

func (s *adoptionStore) SaveNodeAdoption(_ context.Context, a *NodeAdoption) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.adoptions[a.NodeID]; !ok {
		return ErrNotFound
	}
	copied := *a
	s.adoptions[a.NodeID] = &copied
	return nil
}

func (s *adoptionStore) adoption(nodeID string) *NodeAdoption {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.adoptions[nodeID]; ok {
		copied := *a
		return &copied
	}
	return nil
}

// adoptionClouds holds one Cloud in org and one in another org. It hands
// the other org's Cloud to anyone who asks for its ID, as a store that
// forgot the org would, so the adoption's own org check is what refuses it.
type adoptionClouds struct{}

func (adoptionClouds) GetProfile(_ context.Context, org, _, id string) (*cloudprofiles.Profile, error) {
	switch {
	case org == "org" && id == "cloud":
		return &cloudprofiles.Profile{ID: "cloud", OrgID: "org", Name: "Production", Revision: 3}, nil
	case id == "cloud_other":
		return &cloudprofiles.Profile{ID: "cloud_other", OrgID: "other", Name: "Theirs", Revision: 1}, nil
	}
	return nil, cloudprofiles.ErrNotFound
}

// adoptionAuthz allows everyone except the users in denied.
type adoptionAuthz struct{ denied map[string]bool }

func (a adoptionAuthz) Require(_ context.Context, p security.Principal, _ security.Permission, _ string) error {
	if a.denied[p.UserID] {
		return security.ErrForbidden
	}
	return nil
}

// adoptionConnections is the connections service as adoption sees it: it
// writes node connections, lists them, and synthesizes their credential.
type adoptionConnections struct {
	connections.Store
	mu           sync.Mutex
	conns        map[string]*connections.Connection
	creates      []connections.NodeConnectionInput
	principals   []security.Principal
	unavailable  int // the next creates that find Docker not answering
	architecture string
	deleted      int
}

func newAdoptionConnections(arch string) *adoptionConnections {
	return &adoptionConnections{conns: map[string]*connections.Connection{}, architecture: arch}
}

func (c *adoptionConnections) CreateNodeConnection(_ context.Context, p security.Principal, in connections.NodeConnectionInput) (*connections.Connection, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.creates = append(c.creates, in)
	c.principals = append(c.principals, p)
	if c.unavailable > 0 {
		c.unavailable--
		return nil, fmt.Errorf("%w: docker:access", connections.ErrUnavailable)
	}
	now := time.Now().UTC()
	details := map[string]string{"node_id": in.NodeID, "identity_generation": fmt.Sprint(in.IdentityGeneration), "architecture": c.architecture, "docker_id": "daemon-1", "docker_socket": "/var/run/docker.sock"}
	maps.Copy(details, in.Settings)
	conn := &connections.Connection{ID: "conn_" + in.NodeID, OrgID: p.OrgID, Cloud: connections.Server, Name: in.Name, Method: connections.MethodNode, Account: "daemon-1", Details: details, Status: connections.StatusReady, Generation: 1, CreatedAt: now, UpdatedAt: now}
	c.conns[conn.ID] = conn
	return conn, nil
}

func (c *adoptionConnections) NodeConnections(_ context.Context, org, nodeID string) ([]*connections.Connection, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*connections.Connection
	for _, conn := range c.conns {
		if conn.OrgID == org && conn.Method == connections.MethodNode && conn.Details["node_id"] == nodeID {
			out = append(out, conn)
		}
	}
	return out, nil
}

func (c *adoptionConnections) Credential(_ context.Context, org, id string) (*connections.Connection, []byte, error) {
	conn, err := c.GetConnection(context.Background(), org, id)
	if err != nil {
		return nil, nil, err
	}
	return conn, server.NodeCredential(conn.OrgID, conn.Details["node_id"]), nil
}

func (c *adoptionConnections) GetConnection(_ context.Context, org, id string) (*connections.Connection, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	conn, ok := c.conns[id]
	if !ok || conn.OrgID != org {
		return nil, connections.ErrNotFound
	}
	copied := *conn
	copied.Details = maps.Clone(conn.Details)
	return &copied, nil
}

func (c *adoptionConnections) DeleteConnection(context.Context, string, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleted++
	return nil
}

func (c *adoptionConnections) created() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.creates)
}

// adoptionRuntime is the node's Docker host. vacancy is what CheckVacant
// says on an adoption check.
type adoptionRuntime struct {
	mu       sync.Mutex
	vacancy  error
	arch     string
	inspects int
}

func (r *adoptionRuntime) Inspect(_ context.Context, raw []byte, settings map[string]string, vacant bool) (RuntimeHealth, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inspects++
	if !server.IsNodeCredential(raw) || settings["node_id"] == "" {
		return RuntimeHealth{}, errors.New("not a node connection")
	}
	if vacant && r.vacancy != nil {
		return RuntimeHealth{}, r.vacancy
	}
	return RuntimeHealth{DockerID: "daemon-1", Architecture: r.arch, CPUs: 4, MemoryBytes: 8 << 30, CheckedAt: time.Now().UTC()}, nil
}

type adoptionWorkflow struct {
	mu      sync.Mutex
	started []string
}

func (w *adoptionWorkflow) StartInfrastructure(_ context.Context, _ string, id string, _ int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.started = append(w.started, id)
	return nil
}

func (w *adoptionWorkflow) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.started)
}

type adoptionAudit struct {
	mu     sync.Mutex
	events []projects.AuditEvent
}

func (a *adoptionAudit) Record(_ context.Context, e projects.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

func (a *adoptionAudit) actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.events))
	for _, e := range a.events {
		out = append(out, e.Action)
	}
	return out
}

type adoptionRig struct {
	svc      *Service
	store    *adoptionStore
	conns    *adoptionConnections
	runtime  *adoptionRuntime
	workflow *adoptionWorkflow
	audit    *adoptionAudit
	authz    adoptionAuthz
	node     *nodes.Node
}

func newAdoptionRig(t *testing.T, arch string) *adoptionRig {
	t.Helper()
	r := &adoptionRig{store: newAdoptionStore(), conns: newAdoptionConnections(arch), runtime: &adoptionRuntime{arch: arch}, workflow: &adoptionWorkflow{}, audit: &adoptionAudit{}, authz: adoptionAuthz{denied: map[string]bool{}}}
	r.svc = NewService(Deps{Store: r.store, Clouds: adoptionClouds{}, Connections: r.conns, RuntimeConnections: r.conns, Authz: r.authz, Workflow: r.workflow, Runtime: r.runtime, Audit: r.audit, Nodes: &nodes.Adopter{Connections: r.conns}, NodeConnections: r.conns})
	r.node = &nodes.Node{ID: "node_1", OrgID: "org", Name: "web-1", IdentityGeneration: 1, Status: nodes.StatusOnline, Role: "node", Endpoints: []nodeproto.Endpoint{nodeproto.EndpointControl, nodeproto.EndpointProbe, nodeproto.EndpointDocker, nodeproto.EndpointDial}, Inventory: nodeproto.Inventory{Arch: arch}}
	return r
}

func cloudClaim(cloud string) *apikeys.NodeJoinClaim {
	return &apikeys.NodeJoinClaim{OrgID: "org", MintedBy: "user_admin", MintedByKeyID: "key_admin", Scope: apikeys.NodeJoinScope{CloudID: cloud}}
}

// join records the claim the way the Adopt hook does and connects once.
func (r *adoptionRig) join(t *testing.T, claim *apikeys.NodeJoinClaim) *NodeAdoption {
	t.Helper()
	if err := r.svc.RecordNodeAdoption(t.Context(), r.node, claim); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := r.svc.AdoptOnline(t.Context(), r.node); err != nil {
		t.Fatalf("adopt online: %v", err)
	}
	return r.store.adoption(r.node.ID)
}

func TestCleanNodeAdoptsOnFirstConnect(t *testing.T) {
	r := newAdoptionRig(t, "amd64")
	// Without a key ID the approval is the minter's session.
	r.svc.CapacityPrincipal = nil
	claim := cloudClaim("cloud")
	claim.MintedByKeyID = ""
	a := r.join(t, claim)
	if a == nil || a.Status != NodeAdoptionApplied || a.Reason != "" {
		t.Fatalf("adoption %+v, want applied", a)
	}
	if a.CloudID != "cloud" || a.ApprovedBy != "user_admin" || a.OperationID == "" || a.ServerID == "" {
		t.Fatalf("adoption %+v", a)
	}
	o := r.store.ops[a.OperationID]
	if o == nil || o.Status != "pending" || o.Input.Kind != "adopt" || !o.Input.ConfirmAdoption || o.Input.Name != "web-1" || o.ServerID != a.ServerID {
		t.Fatalf("operation %+v", o)
	}
	if o.Ownership != "adopted" {
		t.Fatalf("ownership %q, want adopted", o.Ownership)
	}
	want := security.Principal{UserID: "user_admin", OrgID: "org"}
	if o.Principal != want {
		t.Fatalf("applied as %+v, want the token's minter %+v", o.Principal, want)
	}
	if o.ConnectionID != "conn_node_1" || o.AccountID != "daemon-1" {
		t.Fatalf("operation connection %q account %q", o.ConnectionID, o.AccountID)
	}
	if r.workflow.count() != 1 {
		t.Fatalf("workflow started %d times", r.workflow.count())
	}
	if !slices.Contains(r.audit.actions(), "cloud.node_adoption.applied") {
		t.Fatalf("audit %v", r.audit.actions())
	}
}

func TestAdoptDockerWritesServerNodeConnection(t *testing.T) {
	w := newAdoptionConnections("amd64")
	a := &nodes.Adopter{Connections: w}
	n := &nodes.Node{ID: "node_9", OrgID: "org", Name: "edge-9", IdentityGeneration: 4, Endpoints: []nodeproto.Endpoint{nodeproto.EndpointControl, nodeproto.EndpointDocker}}
	p := security.Principal{UserID: "user_admin", OrgID: "org", KeyID: "key_admin"}
	conn, err := a.AdoptDocker(t.Context(), p, n, map[string]string{"build_workers": "dedicated"})
	if err != nil {
		t.Fatal(err)
	}
	if conn.Method != connections.MethodNode || conn.Cloud != connections.Server {
		t.Fatalf("connection %+v", conn)
	}
	in := w.creates[0]
	if in.Cloud != connections.Server || in.NodeID != "node_9" || in.Name != "edge-9" || in.IdentityGeneration != 4 || in.Settings["build_workers"] != "dedicated" {
		t.Fatalf("input %+v", in)
	}
	if w.principals[0] != p {
		t.Fatalf("written as %+v", w.principals[0])
	}
	// A node that doesn't serve Docker has nothing to adopt.
	n.Endpoints = []nodeproto.Endpoint{nodeproto.EndpointControl, nodeproto.EndpointProbe}
	if _, err = a.AdoptDocker(t.Context(), p, n, nil); !errors.Is(err, nodes.ErrDockerNotAllowed) {
		t.Fatalf("docker not allowed: %v", err)
	}
	// Nor is a node of one org adopted by a principal of another.
	n.Endpoints = []nodeproto.Endpoint{nodeproto.EndpointDocker}
	if _, err = a.AdoptDocker(t.Context(), security.Principal{UserID: "user_x", OrgID: "other"}, n, nil); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("cross-org adopt: %v", err)
	}
	if len(w.creates) != 1 {
		t.Fatalf("%d creates, want 1", len(w.creates))
	}
}

func TestBusyPortsLeavesReview(t *testing.T) {
	r := newAdoptionRig(t, "amd64")
	r.svc.CapacityPrincipal = acceptKeys{}
	r.runtime.vacancy = server.ErrIngressInUse
	a := r.join(t, cloudClaim("cloud"))
	if a.Status != NodeAdoptionReview || a.Reason != "ports 80/443 are in use" {
		t.Fatalf("adoption %+v", a)
	}
	if r.store.accepts != 0 || r.workflow.count() != 0 {
		t.Fatalf("applied: %d accepts, %d starts", r.store.accepts, r.workflow.count())
	}
}

func TestArchMismatchLeavesReview(t *testing.T) {
	r := newAdoptionRig(t, "arm64")
	r.svc.CapacityPrincipal = acceptKeys{}
	r.store.servers["server_a"] = &Server{ID: "server_a", OrgID: "org", CloudID: "cloud", Name: "web-0", Ownership: "managed", Status: "ready", Health: RuntimeHealth{Architecture: "amd64"}}
	r.store.pools = []*Pool{{ID: "pool_web", OrgID: "org", CloudID: "cloud", Name: "web", Members: []PoolMember{{ServerID: "server_a"}}}}
	a := r.join(t, cloudClaim("cloud"))
	if a.Status != NodeAdoptionReview || a.Reason != "arm64 doesn't match pool web (amd64)" {
		t.Fatalf("adoption %+v", a)
	}
	// The preview stays open in the Cloud's Changes tab for an admin.
	o := r.store.ops[a.OperationID]
	if o == nil || o.Status != "preview" || o.Input.Kind != "adopt" {
		t.Fatalf("preview %+v", o)
	}
	if r.store.accepts != 0 || r.workflow.count() != 0 {
		t.Fatalf("applied: %d accepts, %d starts", r.store.accepts, r.workflow.count())
	}
}

func TestAdoptNameClashLeavesReview(t *testing.T) {
	r := newAdoptionRig(t, "amd64")
	r.svc.CapacityPrincipal = acceptKeys{}
	r.store.servers["server_a"] = &Server{ID: "server_a", OrgID: "org", CloudID: "cloud", Name: "web-1", Ownership: "managed", Status: "ready", Health: RuntimeHealth{Architecture: "amd64"}}
	a := r.join(t, cloudClaim("cloud"))
	if a.Status != NodeAdoptionReview || a.Reason != "a server named web-1 exists" {
		t.Fatalf("adoption %+v", a)
	}
	if r.store.accepts != 0 {
		t.Fatalf("applied %d", r.store.accepts)
	}
}

func TestApproverLostRoleLeavesReview(t *testing.T) {
	r := newAdoptionRig(t, "amd64")
	r.svc.CapacityPrincipal = acceptKeys{}
	r.authz.denied["user_admin"] = true
	a := r.join(t, cloudClaim("cloud"))
	if a.Status != NodeAdoptionReview || a.Reason != "the person who approved this can no longer manage Clouds" {
		t.Fatalf("adoption %+v", a)
	}
	if r.conns.created() != 0 || r.store.previews != 0 || r.store.accepts != 0 {
		t.Fatalf("acted for a principal without the role: %d connections, %d previews, %d accepts", r.conns.created(), r.store.previews, r.store.accepts)
	}
	// A revoked API key behind the approval counts the same.
	r2 := newAdoptionRig(t, "amd64")
	r2.svc.CapacityPrincipal = rejectKeys{}
	if a2 := r2.join(t, cloudClaim("cloud")); a2.Status != NodeAdoptionReview || a2.Reason != "the person who approved this can no longer manage Clouds" {
		t.Fatalf("revoked key: %+v", a2)
	}
}

func TestNoCloudNoAdoption(t *testing.T) {
	r := newAdoptionRig(t, "amd64")
	if a := r.join(t, cloudClaim("")); a != nil {
		t.Fatalf("recorded %+v", a)
	}
	if err := r.svc.RecordNodeAdoption(t.Context(), r.node, nil); err != nil {
		t.Fatal(err)
	}
	if r.conns.created() != 0 || r.store.previews != 0 {
		t.Fatal("a node without a Cloud was adopted")
	}
}

func TestClaimCloudFromAnotherOrgRefused(t *testing.T) {
	for _, cloud := range []string{"cloud_other", "cloud_missing"} {
		t.Run(cloud, func(t *testing.T) {
			r := newAdoptionRig(t, "amd64")
			r.svc.CapacityPrincipal = acceptKeys{}
			a := r.join(t, cloudClaim(cloud))
			if a == nil || a.Status != NodeAdoptionFailed || a.CloudID != "" || !strings.Contains(a.Reason, "isn't one of this organization's Clouds") {
				t.Fatalf("adoption %+v", a)
			}
			if r.conns.created() != 0 || r.store.previews != 0 || r.store.accepts != 0 {
				t.Fatal("a node was placed in a Cloud outside its organization")
			}
			if !slices.Contains(r.audit.actions(), "cloud.node_adoption.refused") {
				t.Fatalf("audit %v", r.audit.actions())
			}
		})
	}
}

func TestSecondConnectDoesNotReadopt(t *testing.T) {
	r := newAdoptionRig(t, "amd64")
	r.svc.CapacityPrincipal = acceptKeys{}
	first := r.join(t, cloudClaim("cloud"))
	if first.Status != NodeAdoptionApplied {
		t.Fatalf("first %+v", first)
	}
	// The join's Adopt hook never runs twice, but a second record is harmless.
	if err := r.svc.RecordNodeAdoption(t.Context(), r.node, cloudClaim("cloud")); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := r.svc.AdoptOnline(t.Context(), r.node); err != nil {
			t.Fatal(err)
		}
	}
	if r.conns.created() != 1 || r.store.previews != 1 || r.store.accepts != 1 || r.workflow.count() != 1 {
		t.Fatalf("readopted: %d connections, %d previews, %d accepts, %d starts", r.conns.created(), r.store.previews, r.store.accepts, r.workflow.count())
	}
	if got := r.store.adoption(r.node.ID); *got != *first {
		t.Fatalf("row changed: %+v -> %+v", first, got)
	}
}

func TestAdoptWaitsForDockerAndRetries(t *testing.T) {
	r := newAdoptionRig(t, "amd64")
	r.svc.CapacityPrincipal = acceptKeys{}
	// Online, but Docker isn't allowed yet: nothing to adopt, so it waits.
	r.node.Endpoints = []nodeproto.Endpoint{nodeproto.EndpointControl, nodeproto.EndpointProbe}
	if a := r.join(t, cloudClaim("cloud")); a.Status != NodeAdoptionWaiting || a.Reason == "" {
		t.Fatalf("docker not allowed: %+v", a)
	}
	// Allowed, but the daemon doesn't answer the first check yet.
	r.node.Endpoints = append(r.node.Endpoints, nodeproto.EndpointDocker)
	r.conns.unavailable = 1
	if err := r.svc.AdoptOnline(t.Context(), r.node); err != nil {
		t.Fatal(err)
	}
	if a := r.store.adoption(r.node.ID); a.Status != NodeAdoptionWaiting {
		t.Fatalf("docker not answering: %+v", a)
	}
	// The next connect, or the next inventory, tries again.
	if err := r.svc.AdoptOnline(t.Context(), r.node); err != nil {
		t.Fatal(err)
	}
	if a := r.store.adoption(r.node.ID); a.Status != NodeAdoptionApplied || a.Reason != "" {
		t.Fatalf("retry: %+v", a)
	}
}

func TestAdoptReusesTheNodeConnection(t *testing.T) {
	r := newAdoptionRig(t, "amd64")
	r.svc.CapacityPrincipal = acceptKeys{}
	// The connection was written, then the check after it failed.
	r.runtime.vacancy = errors.New("node web-1 is offline")
	if a := r.join(t, cloudClaim("cloud")); a.Status != NodeAdoptionWaiting {
		t.Fatalf("offline mid-adoption: %+v", a)
	}
	r.runtime.vacancy = nil
	if err := r.svc.AdoptOnline(t.Context(), r.node); err != nil {
		t.Fatal(err)
	}
	if a := r.store.adoption(r.node.ID); a.Status != NodeAdoptionApplied {
		t.Fatalf("retry: %+v", a)
	}
	if r.conns.created() != 1 {
		t.Fatalf("%d connections written, want 1", r.conns.created())
	}
}

func TestAdoptLeavesANodeToTheAttemptHoldingIt(t *testing.T) {
	r := newAdoptionRig(t, "amd64")
	r.svc.CapacityPrincipal = acceptKeys{}
	if err := r.svc.RecordNodeAdoption(t.Context(), r.node, cloudClaim("cloud")); err != nil {
		t.Fatal(err)
	}
	// OnOnline and OnInventory fire together on a node's first report.
	err := r.store.WithOperationLock(t.Context(), "node-adoption/"+r.node.ID, func() error {
		return r.svc.AdoptOnline(t.Context(), r.node)
	})
	if err != nil {
		t.Fatal(err)
	}
	if a := r.store.adoption(r.node.ID); a.Status != NodeAdoptionWaiting || r.conns.created() != 0 {
		t.Fatalf("a second attempt ran: %+v", a)
	}
	if err = r.svc.AdoptOnline(t.Context(), r.node); err != nil {
		t.Fatal(err)
	}
	if a := r.store.adoption(r.node.ID); a.Status != NodeAdoptionApplied {
		t.Fatalf("adoption %+v", a)
	}
}

func TestAdoptRevokedNodeFails(t *testing.T) {
	r := newAdoptionRig(t, "amd64")
	r.svc.CapacityPrincipal = acceptKeys{}
	if err := r.svc.RecordNodeAdoption(t.Context(), r.node, cloudClaim("cloud")); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r.node.Status, r.node.RevokedAt = nodes.StatusRevoked, &now
	if err := r.svc.AdoptOnline(t.Context(), r.node); err != nil {
		t.Fatal(err)
	}
	if a := r.store.adoption(r.node.ID); a.Status != NodeAdoptionFailed || r.conns.created() != 0 {
		t.Fatalf("revoked node: %+v, %d connections", a, r.conns.created())
	}
}

func TestRevokedNodeServerDisconnectsNotDeletes(t *testing.T) {
	r := newAdoptionRig(t, "amd64")
	r.svc.CapacityPrincipal = acceptKeys{}
	a := r.join(t, cloudClaim("cloud"))
	if a.Status != NodeAdoptionApplied {
		t.Fatalf("adoption %+v", a)
	}
	v := r.store.servers[a.ServerID]
	if v == nil || v.Ownership != "adopted" {
		t.Fatalf("server %+v, want adopted", v)
	}
	// Revoking the node changes nothing in the Cloud.
	store := nodestest.NewStore()
	if err := store.Create(t.Context(), r.node); err != nil {
		t.Fatal(err)
	}
	ns := nodestest.NewService(store, &nodestest.Tokens{})
	if err := ns.Revoke(t.Context(), security.Principal{UserID: "user_admin", OrgID: "org"}, r.node.ID); err != nil {
		t.Fatal(err)
	}
	if r.conns.deleted != 0 || len(r.store.servers) != 1 || r.store.servers[a.ServerID].Status == "deleted" {
		t.Fatalf("revoke removed something: %d connections deleted, server %+v", r.conns.deleted, r.store.servers[a.ServerID])
	}
	// And the server can only ever be disconnected, never deleted.
	admin := security.Principal{UserID: "user_admin", OrgID: "org"}
	r.store.servers[a.ServerID].Status = "ready"
	_, err := r.svc.PreviewServer(t.Context(), admin, "cloud", ServerInput{Kind: "delete", ServerID: a.ServerID, ServerRevision: 1, CloudRevision: 3})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "can only be disconnected") {
		t.Fatalf("delete preview: %v", err)
	}
	o, err := r.svc.PreviewServer(t.Context(), admin, "cloud", ServerInput{Kind: "disconnect", ServerID: a.ServerID, ServerRevision: 1, CloudRevision: 3})
	if err != nil || o.Ownership != "adopted" {
		t.Fatalf("disconnect preview: %+v %v", o, err)
	}
}

// acceptKeys and rejectKeys stand in for the API keys service.
type acceptKeys struct{}

func (acceptKeys) ValidatePrincipal(context.Context, security.Principal) error { return nil }

type rejectKeys struct{}

func (rejectKeys) ValidatePrincipal(context.Context, security.Principal) error {
	return apikeys.ErrRejected
}
