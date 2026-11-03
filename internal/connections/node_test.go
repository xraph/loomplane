package connections

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

const (
	nodeA = "node_6f1c1b8e-3f7a-4c2a-9d7e-1a2b3c4d5e6f"
	nodeB = "node_0d9e8f7a-6b5c-4d3e-8f1a-2b3c4d5e6f70"
)

// fakeNodes is a nodes hub whose nodes each serve a Docker API reporting
// one daemon ID. It records every stream opened.
type fakeNodes struct {
	mu      sync.Mutex
	daemons map[string]string
	opened  []string
}

func (f *fakeNodes) Open(_ context.Context, orgID, nodeID string, hdr nodeproto.StreamHeader) (net.Conn, error) {
	f.mu.Lock()
	f.opened = append(f.opened, orgID+"/"+nodeID+"/"+string(hdr.Endpoint))
	daemon, ok := f.daemons[nodeID]
	f.mu.Unlock()
	if !ok || hdr.Endpoint != nodeproto.EndpointDocker || hdr.Target != nodeproto.DockerTargetSocket {
		return nil, errors.New("the fake node serves only its Docker socket")
	}
	client, conn := net.Pipe()
	go serveFakeDocker(conn, daemon)
	return client, nil
}

func (f *fakeNodes) streams() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.opened)
}

func serveFakeDocker(conn net.Conn, daemon string) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	for {
		req, err := http.ReadRequest(r)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, req.Body)
		status, body := http.StatusOK, ""
		switch req.URL.Path {
		case "/version":
			body = `{"Version":"27.3.1","ApiVersion":"1.47","Os":"linux","Arch":"amd64"}`
		case "/info":
			body = fmt.Sprintf(`{"ID":%q,"OSType":"linux","Architecture":"x86_64"}`, daemon)
		default:
			status, body = http.StatusNotFound, `{}`
		}
		resp := &http.Response{StatusCode: status, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{"Content-Type": {"application/json"}},
			ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body))}
		if resp.Write(conn) != nil {
			return
		}
	}
}

// sshStub is the server checker with SSH answered from memory: a key it
// is pasted reaches a daemon with the ID it holds. Node markers go to the
// real ServerChecker and its streams.
type sshStub struct {
	ServerChecker
	daemon string
}

func (c *sshStub) Parse(raw []byte, settings map[string]string) (Check, error) {
	if server.IsNodeCredential(raw) {
		return c.ServerChecker.Parse(raw, settings)
	}
	return Check{Account: "deploy@web.example.com:22", Details: map[string]string{"host": "web.example.com", "port": "22", "user": "deploy", "host_key": "ssh-ed25519 AAAA", "docker_socket": "/var/run/docker.sock", "advertise_host": "web.example.com"}}, nil
}

func (c *sshStub) Check(ctx context.Context, raw []byte, settings map[string]string) (Check, error) {
	if server.IsNodeCredential(raw) {
		return c.ServerChecker.Check(ctx, raw, settings)
	}
	res, err := c.Parse(raw, settings)
	res.Details["docker_id"], res.Details["architecture"] = c.daemon, "amd64"
	return res, err
}

// nodeStore keeps connections in memory and records the locks taken.
type nodeStore struct {
	mu    sync.Mutex
	rows  map[string]Connection
	locks []string
}

func (m *nodeStore) CreateConnection(_ context.Context, c *Connection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, row := range m.rows {
		if row.OrgID == c.OrgID && row.Name == c.Name {
			return ErrExists
		}
	}
	c.normalize()
	row := *c
	row.Details = maps.Clone(c.Details)
	m.rows[c.ID] = row
	return nil
}

func (m *nodeStore) GetConnection(_ context.Context, org, id string) (*Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.rows[id]
	if !ok || c.OrgID != org {
		return nil, ErrNotFound
	}
	c.Details = maps.Clone(c.Details)
	return &c, nil
}

func (m *nodeStore) ListConnections(_ context.Context, org string) ([]*Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Connection
	for _, id := range slices.Sorted(maps.Keys(m.rows)) {
		if c := m.rows[id]; c.OrgID == org {
			c.Details = maps.Clone(c.Details)
			out = append(out, &c)
		}
	}
	return out, nil
}

func (m *nodeStore) UpdateConnection(_ context.Context, c *Connection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := *c
	row.Details = maps.Clone(c.Details)
	m.rows[c.ID] = row
	return nil
}

func (m *nodeStore) DeleteConnection(_ context.Context, org, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.rows[id]; !ok || c.OrgID != org {
		return ErrNotFound
	}
	delete(m.rows, id)
	return nil
}

func (m *nodeStore) ReadyServersOnDaemon(_ context.Context, daemon string) ([]*Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Connection
	for _, id := range slices.Sorted(maps.Keys(m.rows)) {
		if c := m.rows[id]; c.Cloud == Server && c.Status == StatusReady && c.Details["docker_id"] == daemon {
			c.Details = maps.Clone(c.Details)
			out = append(out, &c)
		}
	}
	return out, nil
}

func (m *nodeStore) WithConnectionLock(_ context.Context, org, id string, fn func() error) error {
	m.mu.Lock()
	m.locks = append(m.locks, org+"/"+id)
	m.mu.Unlock()
	return fn()
}

type refuseAuthz struct{}

func (refuseAuthz) Require(context.Context, security.Principal, security.Permission, string) error {
	return security.ErrForbidden
}

func nodeRig(t *testing.T) (*Service, *nodeStore, *memSecrets, *fakeNodes, *sshStub) {
	t.Helper()
	nodes := &fakeNodes{daemons: map[string]string{nodeA: "daemon-a", nodeB: "daemon-b"}}
	checker := &sshStub{ServerChecker: ServerChecker{Streams: nodes}, daemon: "daemon-ssh"}
	store, secrets := &nodeStore{rows: map[string]Connection{}}, &memSecrets{kept: map[string][]byte{}}
	return NewService(store, secrets, map[string]Checker{Server: checker}, identityAuthz{}, nil, nil), store, secrets, nodes, checker
}

func nodeInput(nodeID, name string) NodeConnectionInput {
	return NodeConnectionInput{Cloud: Server, NodeID: nodeID, Name: name, IdentityGeneration: 1, Settings: map[string]string{"advertise_host": "web-1.example.net"}}
}

func seed(store *nodeStore, c Connection) {
	c.normalize()
	store.rows[c.ID] = c
}

func TestCreateNodeConnectionStoresNoSecret(t *testing.T) {
	s, store, secrets, nodes, _ := nodeRig(t)
	c, err := s.CreateNodeConnection(t.Context(), orgPrincipal, nodeInput(nodeA, "web-1"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Method != MethodNode || c.Cloud != Server || c.Status != StatusReady || c.SignedInBy != "" {
		t.Fatalf("wrong connection: %+v", c)
	}
	if c.Details["node_id"] != nodeA || c.Details["identity_generation"] != "1" || c.Details["docker_id"] != "daemon-a" || c.Details["advertise_host"] != "web-1.example.net" {
		t.Fatalf("details: %v", c.Details)
	}
	if secrets.puts != 0 || len(secrets.kept) != 0 {
		t.Fatal("a node connection wrote to Vault")
	}
	if _, ok := store.rows[c.ID]; !ok {
		t.Fatal("connection not stored")
	}
	if opened := nodes.streams(); len(opened) == 0 || opened[0] != "org_1/"+nodeA+"/docker" {
		t.Fatalf("the check didn't reach the node's Docker: %v", opened)
	}
}

func TestCreateNodeConnectionNeedsManage(t *testing.T) {
	s, store, secrets, nodes, _ := nodeRig(t)
	s.authz = refuseAuthz{}
	if _, err := s.CreateNodeConnection(t.Context(), orgPrincipal, nodeInput(nodeA, "web-1")); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("got %v", err)
	}
	if len(store.rows) != 0 || secrets.puts != 0 || len(nodes.streams()) != 0 {
		t.Fatal("a refused principal reached the node or the store")
	}
}

func TestCreateNodeConnectionOtherCloudInvalid(t *testing.T) {
	s, store, secrets, nodes, _ := nodeRig(t)
	for _, cloud := range []string{Kubernetes, Firecracker, GCP, ""} {
		in := nodeInput(nodeA, "web-1")
		in.Cloud = cloud
		if _, err := s.CreateNodeConnection(t.Context(), orgPrincipal, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: got %v", cloud, err)
		}
	}
	if len(store.rows) != 0 || secrets.puts != 0 || len(secrets.kept) != 0 || len(nodes.streams()) != 0 {
		t.Fatal("another cloud's node connection got as far as a check or a write")
	}
}

func TestNodeCredentialSynthesized(t *testing.T) {
	s, _, secrets, nodes, _ := nodeRig(t)
	c, err := s.CreateNodeConnection(t.Context(), orgPrincipal, nodeInput(nodeA, "web-1"))
	if err != nil {
		t.Fatal(err)
	}
	// Anything Vault might hold under the ID is never what's handed out.
	secrets.kept["org_1/"+c.ID] = []byte("stale key")
	_, cred, err := s.Credential(t.Context(), "org_1", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(cred) != string(server.NodeCredential("org_1", nodeA)) {
		t.Fatalf("credential %q", cred)
	}
	before := len(nodes.streams())
	again, err := s.Recheck(t.Context(), orgPrincipal, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != StatusReady || again.Details["node_id"] != nodeA || len(nodes.streams()) == before {
		t.Fatalf("recheck didn't go through the node: %+v", again)
	}
}

func TestPastedNodeMarkerRefused(t *testing.T) {
	marker := server.NodeCredential("org_1", nodeA)
	if !(ServerChecker{}).IsGrant(marker) {
		t.Fatal("the server checker doesn't know a node marker for a grant")
	}
	s, store, secrets, nodes, _ := nodeRig(t)
	settings := map[string]string{"node_id": nodeA, "identity_generation": "1"}
	_, err := s.Create(t.Context(), orgPrincipal, CreateInput{Cloud: Server, Name: "borrowed", Credential: string(marker), Settings: settings})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "join the machine as a node") {
		t.Fatalf("pasted node marker: %v", err)
	}
	if len(store.rows) != 0 || secrets.puts != 0 || len(nodes.streams()) != 0 {
		t.Fatal("a pasted node marker got as far as a check")
	}
	ssh, err := s.Create(t.Context(), orgPrincipal, CreateInput{Cloud: Server, Name: "over ssh", Credential: "-----BEGIN OPENSSH PRIVATE KEY-----"})
	if err != nil {
		t.Fatal(err)
	}
	if _, replaceErr := s.Replace(t.Context(), orgPrincipal, ssh.ID, ReplaceInput{Credential: string(marker), Settings: settings}); !errors.Is(replaceErr, ErrInvalid) {
		t.Fatalf("replacing an SSH key with a node marker: %v", replaceErr)
	}
	node, err := s.CreateNodeConnection(t.Context(), orgPrincipal, nodeInput(nodeB, "web-2"))
	if err != nil {
		t.Fatal(err)
	}
	puts := secrets.puts
	for _, cred := range []string{string(marker), "-----BEGIN OPENSSH PRIVATE KEY-----"} {
		if _, err := s.Replace(t.Context(), orgPrincipal, node.ID, ReplaceInput{Credential: cred}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("replacing a node connection's credential: %v", err)
		}
	}
	if secrets.puts != puts || store.rows[node.ID].Method != MethodNode {
		t.Fatal("a node connection's credential was replaced")
	}
}

func TestSameDaemonOverSSHRefused(t *testing.T) {
	s, store, secrets, nodes, _ := nodeRig(t)
	seed(store, Connection{ID: "conn_ssh", OrgID: "org_1", Cloud: Server, Name: "web over ssh", Method: MethodToken, Status: StatusReady, Details: map[string]string{"host": "web.example.com", "docker_id": "daemon-a"}})
	_, err := s.CreateNodeConnection(t.Context(), orgPrincipal, nodeInput(nodeA, "web-1"))
	if !errors.Is(err, ErrInvalid) || !strings.HasSuffix(err.Error(), ": this Docker daemon is already connected as web over ssh over SSH; disconnect it first") {
		t.Fatalf("got %v", err)
	}
	if len(store.rows) != 1 || secrets.puts != 0 {
		t.Fatal("the second connection to one daemon was kept")
	}
	if len(nodes.streams()) == 0 {
		t.Fatal("the daemon was never asked who it is")
	}
	// Only a ready connection holds the daemon.
	seed(store, Connection{ID: "conn_ssh", OrgID: "org_1", Cloud: Server, Name: "web over ssh", Method: MethodToken, Status: StatusFailed, Details: map[string]string{"host": "web.example.com", "docker_id": "daemon-a"}})
	if _, err := s.CreateNodeConnection(t.Context(), orgPrincipal, nodeInput(nodeA, "web-1")); err != nil {
		t.Fatalf("a failed connection held the daemon: %v", err)
	}
}

func TestSameDaemonInAnotherOrgRefused(t *testing.T) {
	const other = ": this Docker daemon is already connected in another organization"
	theirs := security.Principal{OrgID: "org_2", UserID: "user_2"}
	// Their node, then our SSH.
	s, store, secrets, _, checker := nodeRig(t)
	if _, err := s.CreateNodeConnection(t.Context(), theirs, nodeInput(nodeA, "their-node")); err != nil {
		t.Fatal(err)
	}
	checker.daemon = "daemon-a"
	_, err := s.Create(t.Context(), orgPrincipal, CreateInput{Cloud: Server, Name: "ours", Credential: "-----BEGIN OPENSSH PRIVATE KEY-----"})
	if !errors.Is(err, ErrInvalid) || !strings.HasSuffix(err.Error(), other) || strings.Contains(err.Error(), "their-node") {
		t.Fatalf("our SSH next to their node: %v", err)
	}
	if len(store.rows) != 1 || secrets.puts != 0 {
		t.Fatal("our SSH connection was kept")
	}
	// Their SSH, then our node.
	s, store, _, _, checker = nodeRig(t)
	checker.daemon = "daemon-a"
	if _, err = s.Create(t.Context(), theirs, CreateInput{Cloud: Server, Name: "their ssh", Credential: "-----BEGIN OPENSSH PRIVATE KEY-----"}); err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateNodeConnection(t.Context(), orgPrincipal, nodeInput(nodeA, "web-1"))
	if !errors.Is(err, ErrInvalid) || !strings.HasSuffix(err.Error(), other) || strings.Contains(err.Error(), "their ssh") {
		t.Fatalf("our node next to their SSH: %v", err)
	}
	// SSH next to SSH shares the host's journal, whoever holds it.
	if _, err = s.Create(t.Context(), orgPrincipal, CreateInput{Cloud: Server, Name: "ours", Credential: "-----BEGIN OPENSSH PRIVATE KEY-----"}); err != nil {
		t.Fatalf("our SSH next to their SSH: %v", err)
	}
	if len(store.rows) != 2 {
		t.Fatalf("%d connections kept, want 2", len(store.rows))
	}
}

// markFailed stores connection id as failed, as a check that lost its
// daemon would.
func markFailed(store *nodeStore, id string) {
	c := store.rows[id]
	c.Status, c.Message = StatusFailed, "the daemon went away"
	store.rows[id] = c
}

func TestRecheckNextToNodeStaysNotReady(t *testing.T) {
	s, store, _, _, checker := nodeRig(t)
	checker.daemon = "daemon-a"
	ssh, err := s.Create(t.Context(), orgPrincipal, CreateInput{Cloud: Server, Name: "over ssh", Credential: "-----BEGIN OPENSSH PRIVATE KEY-----"})
	if err != nil {
		t.Fatal(err)
	}
	markFailed(store, ssh.ID)
	if _, err = s.CreateNodeConnection(t.Context(), orgPrincipal, nodeInput(nodeA, "web-1")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Recheck(t.Context(), orgPrincipal, ssh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == StatusReady || got.Message != "this Docker daemon is already connected as node web-1; remove it from the node first" {
		t.Fatalf("recheck next to a node: %s %q", got.Status, got.Message)
	}
	if row := store.rows[ssh.ID]; row.Status != got.Status || row.Message != got.Message {
		t.Fatalf("stored %s %q", row.Status, row.Message)
	}
	if !slices.Contains(store.locks, "/docker/daemon-a") {
		t.Fatalf("the recheck didn't hold the daemon lock: %v", store.locks)
	}
}

func TestRecheckNodeNextToSSHStaysNotReady(t *testing.T) {
	s, store, _, _, checker := nodeRig(t)
	node, err := s.CreateNodeConnection(t.Context(), orgPrincipal, nodeInput(nodeA, "web-1"))
	if err != nil {
		t.Fatal(err)
	}
	markFailed(store, node.ID)
	checker.daemon = "daemon-a"
	if _, err = s.Create(t.Context(), orgPrincipal, CreateInput{Cloud: Server, Name: "over ssh", Credential: "-----BEGIN OPENSSH PRIVATE KEY-----"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Recheck(t.Context(), orgPrincipal, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == StatusReady || got.Message != "this Docker daemon is already connected as over ssh over SSH; disconnect it first" {
		t.Fatalf("node recheck next to SSH: %s %q", got.Status, got.Message)
	}
	// Alone on its daemon, a recheck finds only itself and stays ready.
	again, err := s.CreateNodeConnection(t.Context(), orgPrincipal, nodeInput(nodeB, "web-2"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err = s.Recheck(t.Context(), orgPrincipal, again.ID); err != nil || got.Status != StatusReady {
		t.Fatalf("recheck alone: %+v %v", got, err)
	}
}

func TestRecheckNextToAnotherOrgsNodeStaysNotReady(t *testing.T) {
	s, store, _, _, checker := nodeRig(t)
	checker.daemon = "daemon-a"
	ssh, err := s.Create(t.Context(), orgPrincipal, CreateInput{Cloud: Server, Name: "over ssh", Credential: "-----BEGIN OPENSSH PRIVATE KEY-----"})
	if err != nil {
		t.Fatal(err)
	}
	markFailed(store, ssh.ID)
	if _, err = s.CreateNodeConnection(t.Context(), security.Principal{OrgID: "org_2", UserID: "user_2"}, nodeInput(nodeA, "their-node")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Recheck(t.Context(), orgPrincipal, ssh.ID)
	if err != nil || got.Status == StatusReady || got.Message != "this Docker daemon is already connected in another organization" {
		t.Fatalf("recheck next to their node: %+v %v", got, err)
	}
}

func TestReplaceNextToNodeStaysNotReady(t *testing.T) {
	for _, org := range []string{"org_1", "org_2"} {
		s, store, _, _, checker := nodeRig(t)
		checker.daemon = "daemon-a"
		ssh, err := s.Create(t.Context(), orgPrincipal, CreateInput{Cloud: Server, Name: "over ssh", Credential: "-----BEGIN OPENSSH PRIVATE KEY-----"})
		if err != nil {
			t.Fatal(err)
		}
		markFailed(store, ssh.ID)
		if _, err = s.CreateNodeConnection(t.Context(), security.Principal{OrgID: org, UserID: "user_2"}, nodeInput(nodeA, "web-1")); err != nil {
			t.Fatal(err)
		}
		got, err := s.Replace(t.Context(), orgPrincipal, ssh.ID, ReplaceInput{Credential: "-----BEGIN OPENSSH PRIVATE KEY----- rotated"})
		if err != nil {
			t.Fatalf("%s: %v", org, err)
		}
		want := "this Docker daemon is already connected as node web-1; remove it from the node first"
		if org != "org_1" {
			want = "this Docker daemon is already connected in another organization"
		}
		if got.Status == StatusReady || got.Message != want || store.rows[ssh.ID].Status != got.Status {
			t.Fatalf("%s: replace next to a node: %s %q", org, got.Status, got.Message)
		}
	}
}

func TestSameDaemonAsNodeRefused(t *testing.T) {
	s, store, secrets, _, checker := nodeRig(t)
	if _, err := s.CreateNodeConnection(t.Context(), orgPrincipal, nodeInput(nodeA, "web-1")); err != nil {
		t.Fatal(err)
	}
	checker.daemon = "daemon-a"
	_, err := s.Create(t.Context(), orgPrincipal, CreateInput{Cloud: Server, Name: "web over ssh", Credential: "-----BEGIN OPENSSH PRIVATE KEY-----"})
	if !errors.Is(err, ErrInvalid) || !strings.HasSuffix(err.Error(), ": this Docker daemon is already connected as node web-1; remove it from the node first") {
		t.Fatalf("got %v", err)
	}
	if len(store.rows) != 1 || secrets.puts != 0 || len(secrets.kept) != 0 {
		t.Fatal("the SSH connection to a node's daemon was kept")
	}
	// A second node on one daemon would keep a second fence journal too.
	_, err = s.CreateNodeConnection(t.Context(), orgPrincipal, nodeInput(nodeA, "web-1b"))
	if !errors.Is(err, ErrInvalid) || !strings.HasSuffix(err.Error(), ": this Docker daemon is already connected as node web-1; remove it from the node first") {
		t.Fatalf("second node connection: %v", err)
	}
}

func TestSameDaemonTwiceOverSSHStillAllowed(t *testing.T) {
	s, _, _, _, _ := nodeRig(t)
	for _, name := range []string{"deploy", "deploy again"} {
		if _, err := s.Create(t.Context(), orgPrincipal, CreateInput{Cloud: Server, Name: name, Credential: "-----BEGIN OPENSSH PRIVATE KEY-----"}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestDaemonRuleHoldsTheDaemonLock(t *testing.T) {
	s, store, _, _, _ := nodeRig(t)
	if _, err := s.CreateNodeConnection(t.Context(), orgPrincipal, nodeInput(nodeA, "web-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(t.Context(), orgPrincipal, CreateInput{Cloud: Server, Name: "over ssh", Credential: "-----BEGIN OPENSSH PRIVATE KEY-----"}); err != nil {
		t.Fatal(err)
	}
	// The lock is one per daemon, not per organization.
	want := []string{"/docker/daemon-a", "/docker/daemon-ssh"}
	if !slices.Equal(store.locks, want) {
		t.Fatalf("locks %v, want %v", store.locks, want)
	}
}

func TestRefreshNodeGeneration(t *testing.T) {
	s, store, _, _, _ := nodeRig(t)
	a, err := s.CreateNodeConnection(t.Context(), orgPrincipal, nodeInput(nodeA, "web-1"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateNodeConnection(t.Context(), orgPrincipal, nodeInput(nodeB, "web-2"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshNodeGeneration(t.Context(), "org_1", nodeA, 2); err != nil {
		t.Fatal(err)
	}
	if got := store.rows[a.ID].Details["identity_generation"]; got != "2" {
		t.Fatalf("rejoined node's connection has generation %q", got)
	}
	if got := store.rows[b.ID].Details["identity_generation"]; got != "1" {
		t.Fatalf("another node's connection moved to generation %q", got)
	}
	// A late call for an older generation never rolls it back.
	if err := s.RefreshNodeGeneration(t.Context(), "org_1", nodeA, 1); err != nil {
		t.Fatal(err)
	}
	if got := store.rows[a.ID].Details["identity_generation"]; got != "2" {
		t.Fatalf("generation rolled back to %q", got)
	}
	// Another organization's node of the same ID is untouched.
	if err := s.RefreshNodeGeneration(t.Context(), "org_2", nodeB, 5); err != nil {
		t.Fatal(err)
	}
	if got := store.rows[b.ID].Details["identity_generation"]; got != "1" {
		t.Fatalf("another organization's refresh reached generation %q", got)
	}
	if err := s.RefreshNodeGeneration(t.Context(), "org_1", nodeA, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("generation 0: %v", err)
	}
}

func TestNodeAccountIdentityIgnoresAdvertiseHost(t *testing.T) {
	details := map[string]string{"node_id": nodeA, "identity_generation": "1", "docker_socket": "/var/run/docker.sock", "docker_id": "daemon-a", "advertise_host": "203.0.113.7"}
	moved := maps.Clone(details)
	moved["advertise_host"] = "198.51.100.9"
	before, after := AccountIdentity(Server, "node/"+nodeA, details), AccountIdentity(Server, "node/"+nodeA, moved)
	if before == "" || before != after {
		t.Fatalf("a node's new public address changed its identity: %q %q", before, after)
	}
	other := maps.Clone(details)
	other["node_id"] = nodeB
	if AccountIdentity(Server, "node/"+nodeA, other) == before {
		t.Fatal("another node has the same identity")
	}
	rejoined := maps.Clone(details)
	rejoined["identity_generation"] = "2"
	if AccountIdentity(Server, "node/"+nodeA, rejoined) == before {
		t.Fatal("a node that rejoined with a new key kept its identity")
	}
}

func TestCreateNodeConnectionNeedsTheDaemonID(t *testing.T) {
	s, store, secrets, nodes, _ := nodeRig(t)
	// The fake serves no Docker for this node, as an offline node or one
	// whose docker endpoint isn't allowed.
	_, err := s.CreateNodeConnection(t.Context(), orgPrincipal, nodeInput("node_11111111-2222-4333-8444-555555555555", "dark"))
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "docker:access") {
		t.Fatalf("got %v", err)
	}
	if len(store.rows) != 0 || secrets.puts != 0 || len(nodes.streams()) == 0 {
		t.Fatal("a node connection with no daemon identity was kept, or never tried")
	}
}
