package nodes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

type fakeStore struct {
	mu    sync.Mutex
	nodes map[string]*Node
	fail  error
}

func newFakeStore() *fakeStore { return &fakeStore{nodes: map[string]*Node{}} }

func clone(n *Node) *Node { c := *n; return &c }

func (f *fakeStore) Create(_ context.Context, n *Node) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	for _, o := range f.nodes {
		if o.OrgID == n.OrgID && o.Name == n.Name && o.Status != StatusRevoked {
			return ErrNameTaken
		}
	}
	f.nodes[n.ID] = clone(n)
	return nil
}
func (f *fakeStore) Get(_ context.Context, org, id string) (*Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n, ok := f.nodes[id]; ok && n.OrgID == org {
		return clone(n), nil
	}
	return nil, ErrNotFound
}
func (f *fakeStore) GetByID(_ context.Context, id string) (*Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n, ok := f.nodes[id]; ok {
		return clone(n), nil
	}
	return nil, ErrNotFound
}
func (f *fakeStore) List(_ context.Context, org string) ([]Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Node
	for _, n := range f.nodes {
		if n.OrgID == org {
			out = append(out, *n)
		}
	}
	return out, nil
}
func (f *fakeStore) ListJoinedByKey(_ context.Context, org, keyID string) ([]Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Node
	for _, n := range f.nodes {
		if n.OrgID == org && n.JoinedByKeyID == keyID {
			out = append(out, *n)
		}
	}
	return out, nil
}
func (f *fakeStore) Update(_ context.Context, n *Node) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	old, ok := f.nodes[n.ID]
	if !ok || old.OrgID != n.OrgID {
		return ErrNotFound
	}
	c := clone(n)
	if old.RevokedAt != nil { // terminal, as in SQL
		c.RevokedAt, c.RevokedBy, c.Status = old.RevokedAt, old.RevokedBy, StatusRevoked
	} else if c.RevokedAt != nil {
		c.Status = StatusRevoked
	}
	f.nodes[n.ID] = c
	return nil
}
func (f *fakeStore) Touch(context.Context, string, time.Time, nodeproto.Usage) error { return nil }
func (f *fakeStore) SetStatus(_ context.Context, id string, s Status) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nodes[id].Status = s
	return nil
}
func (f *fakeStore) ClaimSession(context.Context, Session) (int64, error) { return 1, nil }
func (f *fakeStore) Session(context.Context, string) (*Session, error)    { return nil, ErrNotFound }
func (f *fakeStore) ReleaseSession(context.Context, string, int64) error  { return nil }
func (f *fakeStore) MarkStaleOffline(context.Context, time.Time) ([]string, error) {
	return nil, nil
}

type fakeTokens struct {
	claim *apikeys.NodeJoinClaim
	err   error
	calls int
}

func (t *fakeTokens) ConsumeNodeJoinToken(context.Context, string) (*apikeys.NodeJoinClaim, error) {
	t.calls++
	return t.claim, t.err
}

type fakeAuthz struct{ manage bool }

func (a fakeAuthz) Allowed(_ context.Context, _ security.Principal, p security.Permission) (bool, error) {
	if p == security.NodeManage {
		return a.manage, nil
	}
	return true, nil
}

type sent struct {
	id  string
	msg any
}

type auditEvent struct{ action, actor string }

type fakeAudit struct{ events []auditEvent }

func (a *fakeAudit) Record(_ context.Context, action string, p security.Principal, _ Node, _ map[string]string) {
	a.events = append(a.events, auditEvent{action, p.UserID})
}

type rig struct {
	svc    *Service
	store  *fakeStore
	tokens *fakeTokens
	audit  *fakeAudit
	sent   []sent
}

func newRig(t *testing.T, scope apikeys.NodeJoinScope, manage bool) *rig {
	t.Helper()
	r := &rig{store: newFakeStore(), audit: &fakeAudit{}}
	r.tokens = &fakeTokens{claim: &apikeys.NodeJoinClaim{OrgID: "org_1", MintedBy: "usr_1", MintedByKeyID: "key_1", Scope: scope}}
	r.svc = &Service{Store: r.store, Tokens: r.tokens, Authz: fakeAuthz{manage: manage}, Audit: r.audit, Version: "v1.8.0",
		Notify: func(id string, msg any) { r.sent = append(r.sent, sent{id, msg}) }}
	return r
}

func joinReq(eps ...nodeproto.Endpoint) JoinRequest {
	pub, _, _ := ed25519.GenerateKey(nil)
	return JoinRequest{Token: "lpj_live_x", PublicKey: pub, Endpoints: eps, NodeVersion: "v1.8.0", ProtocolVersion: nodeproto.Version,
		Inventory: nodeproto.Inventory{OS: "linux"}}
}

var admin = security.Principal{UserID: "usr_1", OrgID: "org_1"}

func TestJoinCreatesPendingNode(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{CloudID: "cld_1", Labels: map[string]string{"zone": "a"}}, true)
	in := joinReq(nodeproto.EndpointDocker)
	res, err := r.svc.Join(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.OrgID != "org_1" || res.IdentityGeneration != 1 || res.CloudID != "cld_1" || res.NodeID == "" {
		t.Fatalf("result %+v", res)
	}
	n, _ := r.store.GetByID(context.Background(), res.NodeID)
	if n.Status != StatusPending || n.JoinedBy != "usr_1" || n.JoinedByKeyID != "key_1" || n.Role != "node" || n.Labels["zone"] != "a" {
		t.Fatalf("node %+v", n)
	}
	if !strings.HasPrefix(n.Name, "node-") || len(n.Name) != len("node-")+6 {
		t.Fatalf("name %q", n.Name)
	}
	if n.KeyFingerprint == "" || n.Version != "v1.8.0" {
		t.Fatalf("node %+v", n)
	}
	if len(r.audit.events) != 1 || r.audit.events[0].action != "node.joined" {
		t.Fatalf("audit %+v", r.audit.events)
	}
}

func TestJoinNameFromRequest(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	in2 := joinReq()
	in2.Name = "web"
	res, _ := r.svc.Join(context.Background(), in2)
	if n, _ := r.store.GetByID(context.Background(), res.NodeID); n.Name != "web" {
		t.Fatalf("name %q", n.Name)
	}
	in3 := joinReq()
	in3.Name = "web"
	if _, err := r.svc.Join(context.Background(), in3); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("err %v", err)
	}
}

func TestJoinEdgeComesFromScope(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{Edge: true, EdgeScope: apikeys.EdgeScopeOrg}, true)
	res, err := r.svc.Join(context.Background(), joinReq())
	if err != nil {
		t.Fatal(err)
	}
	n, _ := r.store.GetByID(context.Background(), res.NodeID)
	if n.Role != "edge" || n.EdgeScope != "org" {
		t.Fatalf("node %+v", n)
	}
}

func TestJoinIntersectsScope(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}}, true)
	res, err := r.svc.Join(context.Background(), joinReq(nodeproto.EndpointDocker, nodeproto.EndpointKube))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Endpoints, []nodeproto.Endpoint{nodeproto.EndpointDocker}) {
		t.Fatalf("endpoints %v", res.Endpoints)
	}
	r2 := newRig(t, apikeys.NodeJoinScope{}, true)
	res, _ = r2.svc.Join(context.Background(), joinReq(nodeproto.EndpointDocker, nodeproto.EndpointKube))
	if len(res.Endpoints) != 2 {
		t.Fatalf("empty scope should pass the request: %v", res.Endpoints)
	}
}

func TestJoinRefusesOldProtocol(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	in := joinReq()
	in.ProtocolVersion = 0
	if _, err := r.svc.Join(context.Background(), in); !errors.Is(err, ErrUpdateNode) {
		t.Fatalf("err %v", err)
	}
	if r.tokens.calls != 0 {
		t.Fatal("an old node must not burn its token")
	}
}

func TestJoinVersionSkew(t *testing.T) {
	cases := []struct {
		svc, node string
		ok        bool
	}{
		{"v1.8.0", "v1.7.4", true},
		{"v1.8.0", "v1.6.9", false},
		{"v1.8.0", "v1.9.0", true},
		{"v1.8.0", "v1.8.0", true},
		{"v1.8.0", "v0.8.0", false},
		{"v2.0.0", "v1.9.0", false},
		{"v1.8.0", "dev", true},
		{"dev", "v1.0.0", true},
		{"v1.8.0", "weird", true},
	}
	for _, c := range cases {
		r := newRig(t, apikeys.NodeJoinScope{}, true)
		r.svc.Version = c.svc
		in := joinReq()
		in.NodeVersion = c.node
		_, err := r.svc.Join(context.Background(), in)
		if c.ok && err != nil {
			t.Errorf("%s vs %s: %v", c.svc, c.node, err)
		}
		if !c.ok && (!errors.Is(err, ErrUpdateNode) || err.Error() != "update this node") {
			t.Errorf("%s vs %s: err %v", c.svc, c.node, err)
		}
	}
}

func TestJoinTokenErrorsPassThrough(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	r.tokens.err = apikeys.ErrRejected
	if _, err := r.svc.Join(context.Background(), joinReq()); !errors.Is(err, apikeys.ErrRejected) {
		t.Fatalf("err %v", err)
	}
}

func TestJoinCreateFailureAsksForNewToken(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	r.store.fail = errors.New("db down")
	_, err := r.svc.Join(context.Background(), joinReq())
	if !errors.Is(err, ErrMintNewToken) || !strings.Contains(err.Error(), "mint a new join token") {
		t.Fatalf("err %v", err)
	}
}

func TestJoinAdoptHook(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{CloudID: "cld_1"}, true)
	var got string
	r.svc.Adopt = func(_ context.Context, n *Node, c *apikeys.NodeJoinClaim) error {
		got = c.Scope.CloudID + n.ID
		return nil
	}
	res, err := r.svc.Join(context.Background(), joinReq())
	if err != nil || got != "cld_1"+res.NodeID {
		t.Fatalf("err %v got %q", err, got)
	}
}

func seed(r *rig, endpoints ...nodeproto.Endpoint) string {
	_ = r.store.Create(context.Background(), &Node{ID: "node_1", OrgID: "org_1", Name: "a", Status: StatusOnline, Endpoints: endpoints, JoinedByKeyID: "key_1"})
	return "node_1"
}

func TestUpdateRefusesWiden(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	id := seed(r, nodeproto.EndpointDocker)
	_, err := r.svc.Update(context.Background(), admin, id, Update{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker, nodeproto.EndpointKube}})
	if !errors.Is(err, ErrWidenRemotely) || err.Error() != "add endpoints on the machine with: loomctl node allow <endpoint>" {
		t.Fatalf("err %v", err)
	}
	if len(r.sent) != 0 {
		t.Fatal("nothing should be sent")
	}
}

func TestUpdateNarrowNotifies(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	id := seed(r, nodeproto.EndpointDocker, nodeproto.EndpointKube)
	n, err := r.svc.Update(context.Background(), admin, id, Update{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(n.Endpoints, []nodeproto.Endpoint{nodeproto.EndpointDocker}) {
		t.Fatalf("endpoints %v", n.Endpoints)
	}
	if len(r.sent) != 1 || r.sent[0].id != id {
		t.Fatalf("sent %+v", r.sent)
	}
	nm, ok := r.sent[0].msg.(nodeproto.Narrow)
	if !ok || !slices.Equal(nm.Endpoints, []nodeproto.Endpoint{nodeproto.EndpointDocker}) {
		t.Fatalf("msg %#v", r.sent[0].msg)
	}
	if got := r.audit.events; len(got) != 1 || got[0].action != "node.updated" {
		t.Fatalf("audit %+v", got)
	}
}

func TestUpdateRenameAndLabelsDoNotNotify(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	id := seed(r, nodeproto.EndpointDocker)
	name := "renamed"
	n, err := r.svc.Update(context.Background(), admin, id, Update{Name: &name, Labels: map[string]string{"a": "b"}})
	if err != nil || n.Name != "renamed" || n.Labels["a"] != "b" || len(r.sent) != 0 {
		t.Fatalf("err %v node %+v sent %v", err, n, r.sent)
	}
	bad := "sideways"
	if _, err := r.svc.Update(context.Background(), admin, id, Update{IngressMode: &bad}); err == nil {
		t.Fatal("bad ingress mode accepted")
	}
}

func TestRevokeNotifiesAndBlocks(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	id := seed(r, nodeproto.EndpointDocker)
	if err := r.svc.Revoke(context.Background(), admin, id); err != nil {
		t.Fatal(err)
	}
	n, _ := r.store.GetByID(context.Background(), id)
	if n.Status != StatusRevoked || n.RevokedAt == nil || n.RevokedBy != "usr_1" {
		t.Fatalf("node %+v", n)
	}
	if len(r.sent) != 1 || r.sent[0].msg != (nodeproto.Revoked{}) {
		t.Fatalf("sent %+v", r.sent)
	}
	if got := r.audit.events; len(got) != 1 || got[0].action != "node.revoked" {
		t.Fatalf("audit %+v", got)
	}
	if _, err := r.svc.Update(context.Background(), admin, id, Update{Labels: map[string]string{"x": "y"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of revoked node: %v", err)
	}
}

func TestLeaveRevokesAsTheNode(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	id := seed(r)
	if err := r.svc.Leave(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	n, _ := r.store.GetByID(context.Background(), id)
	if n.Status != StatusRevoked || n.RevokedBy != id || n.RevokedAt == nil {
		t.Fatalf("node %+v", n)
	}
	if got := r.audit.events; len(got) != 1 || got[0].action != "node.left" || got[0].actor != id {
		t.Fatalf("audit %+v", got)
	}
}

func TestJoinedByKey(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	seed(r)
	got, err := r.svc.JoinedByKey(context.Background(), admin, "key_1")
	if err != nil || len(got) != 1 {
		t.Fatalf("err %v got %v", err, got)
	}
	got, _ = r.svc.JoinedByKey(context.Background(), admin, "key_2")
	if len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestViewerCannotRevoke(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, false)
	id := seed(r, nodeproto.EndpointDocker)
	if err := r.svc.Revoke(context.Background(), admin, id); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := r.svc.Update(context.Background(), admin, id, Update{}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("update: %v", err)
	}
	if _, err := r.svc.JoinedByKey(context.Background(), admin, "key_1"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("joinedbykey: %v", err)
	}
	if _, err := r.svc.Get(context.Background(), admin, id); err != nil {
		t.Fatalf("a viewer can read: %v", err)
	}
	if l, err := r.svc.List(context.Background(), admin); err != nil || len(l) != 1 {
		t.Fatalf("list %v %v", l, err)
	}
	n, _ := r.store.GetByID(context.Background(), id)
	if n.Status == StatusRevoked {
		t.Fatal("revoked anyway")
	}
}
