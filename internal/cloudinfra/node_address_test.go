package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// RuntimeServers lists the servers that run a connection, as the real store does.
func (s *adoptionStore) RuntimeServers(_ context.Context, org string) ([]*Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*Server{}
	for _, v := range s.servers {
		if v.OrgID == org && v.RuntimeConnectionID != "" && v.Status != "deleted" && v.Status != "disconnected" {
			copied := *v
			out = append(out, &copied)
		}
	}
	return out, nil
}

// SetNodeAdvertiseHost writes the confirmed address on a node connection.
func (c *adoptionConnections) SetNodeAdvertiseHost(_ context.Context, org, id, address string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	conn, ok := c.conns[id]
	if !ok || conn.OrgID != org {
		return connections.ErrNotFound
	}
	conn.Details = maps.Clone(conn.Details)
	conn.Details["advertise_host"] = address
	return nil
}

type ingressCall struct{ node, mode, address string }

// fakeNodeIngress is the nodes service as ConfirmNodeAddress sees it.
type fakeNodeIngress struct {
	mu      sync.Mutex
	calls   []ingressCall
	details []string
	err     error
}

func (f *fakeNodeIngress) SetIngress(_ context.Context, nodeID, mode, address string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ingressCall{nodeID, mode, address})
	return f.err
}

func (f *fakeNodeIngress) RecordIngressDetail(_ context.Context, nodeID, detail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.details = append(f.details, nodeID+": "+detail)
	return nil
}

func (f *fakeNodeIngress) list() []ingressCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// nonceProxy stands in for the server's Caddy: it serves the nonce while
// the check runs and records when the temporary route came and went.
type nonceProxy struct {
	mu      sync.Mutex
	serving string
	served  []string
	removed []string
	targets []nodeNonceTarget
	err     error
}

func (p *nonceProxy) serve(ctx context.Context, target nodeNonceTarget, nonce string, check func(context.Context) error) error {
	p.mu.Lock()
	if p.err != nil {
		p.mu.Unlock()
		return p.err
	}
	p.serving = nonce
	p.served = append(p.served, nonce)
	p.targets = append(p.targets, target)
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.serving = ""
		p.removed = append(p.removed, nonce)
		p.mu.Unlock()
	}()
	return check(ctx)
}

func (p *nonceProxy) current() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.serving
}

// addressRig is a node's ready server connection with a fake proxy and a
// fake outside network: answering holds the addresses that reach the proxy.
type addressRig struct {
	*adoptionRig
	ingress   *fakeNodeIngress
	proxy     *nonceProxy
	mu        sync.Mutex
	answering map[string]bool
	fetched   []string
	conn      *connections.Connection
}

func newAddressRig(t *testing.T) *addressRig {
	t.Helper()
	r := &addressRig{adoptionRig: newAdoptionRig(t, "amd64"), ingress: &fakeNodeIngress{}, proxy: &nonceProxy{}, answering: map[string]bool{}}
	r.svc.NodeIngress, r.svc.NodeAddresses = r.ingress, r.conns
	r.svc.serveNodeNonce = r.proxy.serve
	r.svc.fetchNodeNonce = func(ctx context.Context, url string) ([]byte, error) {
		if d, ok := ctx.Deadline(); !ok || time.Until(d) > nodeAddressTimeout {
			t.Errorf("fetch of %s has no %v deadline", url, nodeAddressTimeout)
		}
		host := strings.TrimPrefix(url, "http://")
		host, path, _ := strings.Cut(host, "/")
		host = strings.Trim(host, "[]")
		r.mu.Lock()
		r.fetched = append(r.fetched, host)
		ok := r.answering[host]
		r.mu.Unlock()
		nonce := r.proxy.current()
		if !ok || nonce == "" || "/"+path != server.NodeAddressPath+nonce {
			return nil, errors.New("connection refused")
		}
		return []byte(nonce), nil
	}
	conn, err := r.conns.CreateNodeConnection(t.Context(), security.Principal{UserID: "user_admin", OrgID: "org"}, connections.NodeConnectionInput{Cloud: connections.Server, NodeID: r.node.ID, Name: r.node.Name, IdentityGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	r.conn = conn
	r.store.servers["srv_1"] = &Server{ID: "srv_1", OrgID: "org", CloudID: "cloud", Name: "web-1", RuntimeConnectionID: conn.ID, ResourceID: "daemon-1", Status: "ready"}
	return r
}

func (r *addressRig) fetches() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.fetched)
}

func (r *addressRig) advertised(t *testing.T) string {
	t.Helper()
	c, err := r.conns.GetConnection(t.Context(), "org", r.conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	return c.Details["advertise_host"]
}

func TestConfirmPicksAnsweringAddress(t *testing.T) {
	r := newAddressRig(t)
	r.answering["198.51.100.9"] = true
	got, err := r.svc.ConfirmNodeAddress(t.Context(), "org", r.conn.ID, []string{"203.0.113.7", "198.51.100.9"})
	if err != nil || got != "198.51.100.9" {
		t.Fatalf("confirmed %q %v", got, err)
	}
	if f := r.fetches(); !slices.Equal(f, []string{"203.0.113.7", "198.51.100.9"}) {
		t.Fatalf("fetched %v", f)
	}
	if a := r.advertised(t); a != "198.51.100.9" {
		t.Fatalf("advertise_host %q", a)
	}
	if c := r.ingress.list(); !slices.Equal(c, []ingressCall{{"node_1", nodeproto.IngressDirect, "198.51.100.9"}}) {
		t.Fatalf("ingress %+v", c)
	}
	if len(r.proxy.targets) != 1 || r.proxy.targets[0].ServerID != "srv_1" || r.proxy.targets[0].OrgID != "org" || r.proxy.targets[0].Connection.ID != r.conn.ID {
		t.Fatalf("served on %+v", r.proxy.targets)
	}
	if n := r.proxy.served[0]; len(n) != 32 || strings.Trim(n, "0123456789abcdef") != "" {
		t.Fatalf("nonce %q", n)
	}
}

func TestConfirmSkipsPrivateRanges(t *testing.T) {
	r := newAddressRig(t)
	candidates := []string{"10.0.0.5", "192.168.1.2", "100.64.0.1", "172.16.4.4", "127.0.0.1", "169.254.169.254", "0.0.0.0", "224.0.0.1", "255.255.255.255",
		"0.1.2.3", "192.0.0.8", "198.18.0.1", "198.19.255.1", "240.0.0.1", "64:ff9b::a00:5", "64:ff9b:1::1", "2002:a00:5::1", "::10.0.0.5",
		"fd00::1", "fe80::1", "::1", "::", "ff02::1", "::ffff:10.0.0.9", "fe80::1%eth0", "web.example.com", "2001:db8::5", " 203.0.113.7 ", "203.0.113.7"}
	_, err := r.svc.ConfirmNodeAddress(t.Context(), "org", r.conn.ID, candidates)
	if !errors.Is(err, ErrNoPublicAddress) {
		t.Fatalf("err %v", err)
	}
	// Public IPv4 first, then IPv6, each once.
	if f := r.fetches(); !slices.Equal(f, []string{"203.0.113.7", "2001:db8::5"}) {
		t.Fatalf("fetched %v", f)
	}
}

func TestConfirmRemovesTemporaryRoute(t *testing.T) {
	for _, answers := range []bool{true, false} {
		r := newAddressRig(t)
		r.answering["203.0.113.7"] = answers
		_, err := r.svc.ConfirmNodeAddress(t.Context(), "org", r.conn.ID, []string{"203.0.113.7"})
		if answers != (err == nil) {
			t.Fatalf("answers %v: %v", answers, err)
		}
		if len(r.proxy.served) != 1 || !slices.Equal(r.proxy.removed, r.proxy.served) || r.proxy.current() != "" {
			t.Fatalf("answers %v: served %v removed %v", answers, r.proxy.served, r.proxy.removed)
		}
	}
}

func TestNoAddressSetsNone(t *testing.T) {
	r := newAddressRig(t)
	if _, err := r.svc.ConfirmNodeAddress(t.Context(), "org", r.conn.ID, []string{"203.0.113.7"}); !errors.Is(err, ErrNoPublicAddress) {
		t.Fatalf("err %v", err)
	}
	if c := r.ingress.list(); !slices.Equal(c, []ingressCall{{"node_1", nodeproto.IngressNone, ""}}) {
		t.Fatalf("ingress %+v", c)
	}
	if a := r.advertised(t); a != "" {
		t.Fatalf("advertise_host %q", a)
	}
	if ErrNoPublicAddress.Error() != "no public address answered on port 80; relay arrives in a later release" {
		t.Fatalf("text %q", ErrNoPublicAddress)
	}

	// With no public candidate at all, nothing is served or fetched.
	r = newAddressRig(t)
	if _, err := r.svc.ConfirmNodeAddress(t.Context(), "org", r.conn.ID, []string{"10.0.0.5"}); !errors.Is(err, ErrNoPublicAddress) {
		t.Fatalf("err %v", err)
	}
	if len(r.proxy.served) != 0 || len(r.fetches()) != 0 {
		t.Fatalf("served %v fetched %v", r.proxy.served, r.fetches())
	}
	if c := r.ingress.list(); !slices.Equal(c, []ingressCall{{"node_1", nodeproto.IngressNone, ""}}) {
		t.Fatalf("ingress %+v", c)
	}
}

func TestConfirmChangesNothingWhenTheProxyFails(t *testing.T) {
	r := newAddressRig(t)
	r.proxy.err = errors.New("the node went offline")
	if _, err := r.svc.ConfirmNodeAddress(t.Context(), "org", r.conn.ID, []string{"203.0.113.7"}); err == nil || errors.Is(err, ErrNoPublicAddress) {
		t.Fatalf("err %v", err)
	}
	if c := r.ingress.list(); len(c) != 0 {
		t.Fatalf("ingress %+v", c)
	}
}

func TestConfirmWaitsForAReadyServer(t *testing.T) {
	r := newAddressRig(t)
	r.store.servers["srv_1"].Status = "provisioning"
	if _, err := r.svc.ConfirmNodeAddress(t.Context(), "org", r.conn.ID, []string{"203.0.113.7"}); !errors.Is(err, ErrPending) {
		t.Fatalf("err %v", err)
	}
	if len(r.proxy.served) != 0 || len(r.ingress.list()) != 0 {
		t.Fatalf("served %v ingress %+v", r.proxy.served, r.ingress.list())
	}
}

func TestConfirmRefusesAConnectionThatIsNotANodes(t *testing.T) {
	r := newAddressRig(t)
	ssh := &connections.Connection{ID: "conn_ssh", OrgID: "org", Cloud: connections.Server, Method: "ssh", Details: map[string]string{"host": "203.0.113.7", "docker_id": "daemon-2"}, Status: connections.StatusReady}
	r.conns.conns[ssh.ID] = ssh
	if _, err := r.svc.ConfirmNodeAddress(t.Context(), "org", ssh.ID, []string{"203.0.113.7"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err %v", err)
	}
}

func TestConfirmFetchFollowsNoRedirectAndBoundsTheBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/moved":
			http.Redirect(w, req, "/answer", http.StatusFound)
		case "/answer":
			_, _ = w.Write([]byte("nonce"))
		default:
			_, _ = w.Write([]byte(strings.Repeat("x", 1<<20)))
		}
	}))
	defer srv.Close()
	// The test server is on loopback, which the real client refuses.
	if _, err := fetchNodeNonce(t.Context(), srv.URL+"/answer"); err == nil || !strings.Contains(err.Error(), "refusing to fetch") {
		t.Fatalf("loopback fetched: %v", err)
	}
	client := newNodeAddressClient(nil)
	if _, err := fetchNodeNonceWith(t.Context(), client, srv.URL+"/moved"); err == nil {
		t.Fatal("a redirect was followed")
	}
	if body, err := fetchNodeNonceWith(t.Context(), client, srv.URL+"/answer"); err != nil || string(body) != "nonce" {
		t.Fatalf("body %q %v", body, err)
	}
	if body, err := fetchNodeNonceWith(t.Context(), client, srv.URL+"/big"); err != nil || len(body) > maxNodeNonceBody {
		t.Fatalf("read %d bytes, %v", len(body), err)
	}
}

// readyWorkflow finishes the adopt operation it starts at once, so the
// server is ready when the adoption looks for it.
type readyWorkflow struct{ store *adoptionStore }

func (w readyWorkflow) StartInfrastructure(_ context.Context, _ string, id string, _ int) error {
	w.store.mu.Lock()
	defer w.store.mu.Unlock()
	if o := w.store.ops[id]; o != nil {
		if v := w.store.servers[o.ServerID]; v != nil {
			v.Status, v.ResourceID = "ready", "daemon-1"
		}
	}
	return nil
}

func adoptedAddressRig(t *testing.T) *addressRig {
	t.Helper()
	r := &addressRig{adoptionRig: newAdoptionRig(t, "amd64"), ingress: &fakeNodeIngress{}, proxy: &nonceProxy{}, answering: map[string]bool{}}
	r.svc.Workflow = readyWorkflow{store: r.store}
	r.svc.NodeIngress, r.svc.NodeAddresses = r.ingress, r.conns
	r.svc.serveNodeNonce = r.proxy.serve
	r.svc.serverReadyPoll = time.Millisecond
	r.svc.fetchNodeNonce = func(_ context.Context, url string) ([]byte, error) {
		if strings.HasPrefix(url, "http://203.0.113.7/") && r.proxy.current() != "" {
			return []byte(r.proxy.current()), nil
		}
		return nil, errors.New("connection refused")
	}
	r.node.Inventory.PublicAddresses = []string{"10.0.0.5", "203.0.113.7"}
	r.node.IngressMode = nodeproto.IngressNone
	// Without a key ID the approval is the minter's session.
	r.svc.CapacityPrincipal = nil
	// What waits for a server runs inline, so a second check would show.
	r.svc.background = func(f func()) { f() }
	return r
}

// storedNode makes the rig's node the one the follows read as stored.
func (r *addressRig) storedNode() *ddnsNodes {
	stored := *r.node
	records := &ddnsNodes{nodes: map[string]*nodes.Node{stored.ID: &stored}}
	r.svc.NodeRecords = records
	return records
}

// A node whose first check found nothing isn't left in mode none: a
// reconcile checks it again once the window is over.
func TestReconcileConfirmsANodeWhoseFirstCheckFailed(t *testing.T) {
	r := adoptedAddressRig(t)
	answers := false
	r.svc.fetchNodeNonce = func(_ context.Context, url string) ([]byte, error) {
		if answers && strings.HasPrefix(url, "http://203.0.113.7/") && r.proxy.current() != "" {
			return []byte(r.proxy.current()), nil
		}
		return nil, errors.New("connection refused")
	}
	r.storedNode()
	if a := r.join(t, sessionClaim()); a.Status != NodeAdoptionApplied {
		t.Fatalf("adoption %+v", a)
	}
	if c := r.ingress.list(); !slices.Equal(c, []ingressCall{{"node_1", nodeproto.IngressNone, ""}}) {
		t.Fatalf("first check: %+v", c)
	}
	answers = true
	r.svc.Store = &behindStore{adoptionStore: r.store, behind: []*nodes.Node{{ID: "node_1", OrgID: "org"}}}
	if err := r.svc.ReconcileNodeAddresses(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(r.ingress.list()) != 1 {
		t.Fatalf("checked again within the window: %+v", r.ingress.list())
	}
	later := time.Now().Add(nodeAddressRecheckEvery)
	r.svc.now = func() time.Time { return later }
	if err := r.svc.ReconcileNodeAddresses(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c := r.ingress.list(); !slices.Equal(c, []ingressCall{{"node_1", nodeproto.IngressNone, ""}, {"node_1", nodeproto.IngressDirect, "203.0.113.7"}}) {
		t.Fatalf("reconcile: %+v", c)
	}
	if c, _ := r.conns.GetConnection(t.Context(), "org", "conn_node_1"); c.Details["advertise_host"] != "203.0.113.7" {
		t.Fatalf("connection %+v", c.Details)
	}
}

// An adoption left for review that an admin adopts by hand is settled
// with the real server and operation once the server is ready, and the
// node's address is checked.
func TestManualAdoptSettlesTheAdoptionAndConfirms(t *testing.T) {
	r := adoptedAddressRig(t)
	r.storedNode()
	r.authz.denied["user_admin"] = true
	if a := r.join(t, sessionClaim()); a.Status != NodeAdoptionReview {
		t.Fatalf("adoption %+v", a)
	}
	ops := security.Principal{UserID: "user_ops", OrgID: "org"}
	conn, err := r.conns.CreateNodeConnection(t.Context(), ops, connections.NodeConnectionInput{Cloud: connections.Server, NodeID: r.node.ID, Name: r.node.Name, IdentityGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	o, err := r.svc.PreviewServer(t.Context(), ops, "cloud", ServerInput{Kind: "adopt", ConnectionID: conn.ID, Name: "web-1", CloudRevision: 3, ConfirmAdoption: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.svc.ApplyServerOperation(t.Context(), ops, "cloud", o.ID, ApplyInput{Review: o.Review}); err != nil {
		t.Fatal(err)
	}
	a := r.store.adoption(r.node.ID)
	if a.Status != NodeAdoptionApplied || a.Reason != "" || a.ServerID != o.ServerID || a.OperationID != o.ID {
		t.Fatalf("adoption %+v, want applied by %s on %s", a, o.ID, o.ServerID)
	}
	if !slices.Contains(r.audit.actions(), "cloud.node_adoption.applied") {
		t.Fatalf("audit %v", r.audit.actions())
	}
	if c := r.ingress.list(); !slices.Equal(c, []ingressCall{{"node_1", nodeproto.IngressDirect, "203.0.113.7"}}) {
		t.Fatalf("ingress %+v", c)
	}
}

// A connection whose node record belongs to another organization settles
// nothing and checks nothing.
func TestManualAdoptOfAnotherOrgsNodeSettlesNothing(t *testing.T) {
	r := adoptedAddressRig(t)
	records := r.storedNode()
	r.authz.denied["user_admin"] = true
	if a := r.join(t, sessionClaim()); a.Status != NodeAdoptionReview {
		t.Fatalf("adoption %+v", a)
	}
	records.set(func(n *nodes.Node) { n.OrgID = "other" })
	var warned []string
	r.svc.Warn = func(msg string, _ error) { warned = append(warned, msg) }
	ops := security.Principal{UserID: "user_ops", OrgID: "org"}
	conn, err := r.conns.CreateNodeConnection(t.Context(), ops, connections.NodeConnectionInput{Cloud: connections.Server, NodeID: r.node.ID, Name: r.node.Name, IdentityGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	o, err := r.svc.PreviewServer(t.Context(), ops, "cloud", ServerInput{Kind: "adopt", ConnectionID: conn.ID, Name: "web-1", CloudRevision: 3, ConfirmAdoption: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.svc.ApplyServerOperation(t.Context(), ops, "cloud", o.ID, ApplyInput{Review: o.Review}); err != nil {
		t.Fatal(err)
	}
	if a := r.store.adoption(r.node.ID); a.Status != NodeAdoptionReview || a.OperationID == o.ID {
		t.Fatalf("settled from another organization's node: %+v", a)
	}
	if len(r.ingress.list()) != 0 || len(warned) != 1 {
		t.Fatalf("ingress %+v warned %v", r.ingress.list(), warned)
	}
}

// Asking for direct ingress checks the address even when a check just ran.
func TestCheckNodeAddressRunsWhenAsked(t *testing.T) {
	r := adoptedAddressRig(t)
	records := r.storedNode()
	r.svc.fetchNodeNonce = func(context.Context, string) ([]byte, error) { return nil, errors.New("connection refused") }
	if a := r.join(t, sessionClaim()); a.Status != NodeAdoptionApplied || len(r.ingress.list()) != 1 {
		t.Fatalf("adoption %+v ingress %+v", a, r.ingress.list())
	}
	r.svc.fetchNodeNonce = func(_ context.Context, url string) ([]byte, error) {
		if strings.HasPrefix(url, "http://203.0.113.7/") && r.proxy.current() != "" {
			return []byte(r.proxy.current()), nil
		}
		return nil, errors.New("connection refused")
	}
	// The owner's update set direct, which settles nothing by itself.
	records.set(func(n *nodes.Node) { n.IngressMode = nodeproto.IngressDirect })
	if err := r.svc.FollowNodeAddress(t.Context(), r.node); err != nil || len(r.ingress.list()) != 1 {
		t.Fatalf("a follow checked within the window: %v %+v", err, r.ingress.list())
	}
	if err := r.svc.CheckNodeAddress(t.Context(), r.node); err != nil {
		t.Fatal(err)
	}
	if c := r.ingress.list(); len(c) != 2 || c[1] != (ingressCall{"node_1", nodeproto.IngressDirect, "203.0.113.7"}) {
		t.Fatalf("ingress %+v", c)
	}
}

func sessionClaim() *apikeys.NodeJoinClaim {
	claim := cloudClaim("cloud")
	claim.MintedByKeyID = ""
	return claim
}

func TestConfirmAfterAdoption(t *testing.T) {
	r := adoptedAddressRig(t)
	a := r.join(t, sessionClaim())
	if a.Status != NodeAdoptionApplied {
		t.Fatalf("adoption %+v", a)
	}
	if c := r.ingress.list(); !slices.Equal(c, []ingressCall{{"node_1", nodeproto.IngressDirect, "203.0.113.7"}}) {
		t.Fatalf("ingress %+v", c)
	}
	if c, _ := r.conns.GetConnection(t.Context(), "org", "conn_node_1"); c.Details["advertise_host"] != "203.0.113.7" {
		t.Fatalf("connection %+v", c.Details)
	}
}

func TestConfirmFailureNeverFailsTheAdoption(t *testing.T) {
	r := adoptedAddressRig(t)
	r.proxy.err = errors.New("ports 80/443 are in use")
	var warned []string
	r.svc.Warn = func(msg string, _ error) { warned = append(warned, msg) }
	a := r.join(t, sessionClaim())
	if a.Status != NodeAdoptionApplied || a.Reason != "" {
		t.Fatalf("adoption %+v", a)
	}
	if len(r.ingress.list()) != 0 || len(warned) != 1 {
		t.Fatalf("ingress %+v warned %v", r.ingress.list(), warned)
	}
}

func TestConfirmSaysWhenPortsAreTaken(t *testing.T) {
	r := newAddressRig(t)
	r.proxy.err = fmt.Errorf("%w: %w", errProxyPortsTaken, server.ErrIngressPortsTaken)
	if _, err := r.svc.ConfirmNodeAddress(t.Context(), "org", r.conn.ID, []string{"203.0.113.7"}); !errors.Is(err, errProxyPortsTaken) {
		t.Fatalf("err %v", err)
	}
	if len(r.ingress.calls) != 0 || len(r.ingress.details) != 1 || r.ingress.details[0] != "node_1: "+ProxyPortsTakenDetail {
		t.Fatalf("calls %+v details %v", r.ingress.calls, r.ingress.details)
	}
}
