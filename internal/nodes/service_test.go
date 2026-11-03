package nodes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
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
	mu       sync.Mutex
	nodes    map[string]*Node
	epochs   map[string]int64
	sessions map[string]Session
	nonces   map[string]time.Time
	touches  map[string]int
	fail     error
	// allowErr makes AllowEndpoints fail, as a store outage would.
	allowErr error
	// afterClaim runs after ClaimSession commits, outside the lock.
	afterClaim func(nodeID string)
	// skipPrecheck makes KeyKnown and NameTaken answer false, as a join
	// racing another one would see.
	skipPrecheck bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{nodes: map[string]*Node{}, epochs: map[string]int64{}, sessions: map[string]Session{},
		nonces: map[string]time.Time{}, touches: map[string]int{}}
}

func clone(n *Node) *Node {
	c := *n
	c.Endpoints = slices.Clone(n.Endpoints)
	return &c
}

func (f *fakeStore) Create(_ context.Context, n *Node) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	for _, o := range f.nodes {
		if n.KeyFingerprint != "" && o.KeyFingerprint == n.KeyFingerprint {
			return ErrKeyKnown
		}
		if o.OrgID == n.OrgID && o.Name == n.Name && o.Status != StatusRevoked {
			return ErrNameTaken
		}
	}
	f.nodes[n.ID] = clone(n)
	return nil
}

func (f *fakeStore) KeyKnown(_ context.Context, fingerprint string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.skipPrecheck {
		return false, nil
	}
	for _, o := range f.nodes {
		if o.KeyFingerprint == fingerprint {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) NameTaken(_ context.Context, org, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.skipPrecheck {
		return false, nil
	}
	for _, o := range f.nodes {
		if o.OrgID == org && o.Name == name && o.Status != StatusRevoked {
			return true, nil
		}
	}
	return false, nil
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
func (f *fakeStore) Update(ctx context.Context, n *Node) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
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
func (f *fakeStore) UpdateSettings(_ context.Context, org, id string, u SettingsUpdate) (*Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.nodes[id]
	if !ok || n.OrgID != org || n.RevokedAt != nil {
		return nil, ErrNotFound
	}
	if u.Endpoints != nil {
		for _, e := range u.Endpoints {
			if !slices.Contains(n.Endpoints, e) {
				return nil, ErrWidenRemotely
			}
		}
		n.Endpoints = slices.Clone(u.Endpoints)
	}
	if u.Name != nil {
		n.Name = *u.Name
	}
	if u.Labels != nil {
		n.Labels = u.Labels
	}
	if u.IngressMode != nil {
		n.IngressMode = *u.IngressMode
	}
	return clone(n), nil
}
func (f *fakeStore) Touch(_ context.Context, id string, at time.Time, _ nodeproto.Usage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.nodes[id]
	if !ok {
		return ErrNotFound
	}
	n.LastSeenAt = &at
	f.touches[id]++
	return nil
}
func (f *fakeStore) SetStatus(_ context.Context, id string, s Status) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.nodes[id]
	if !ok || (n.RevokedAt != nil && s != StatusRevoked) {
		return ErrNotFound
	}
	n.Status = s
	return nil
}
func (f *fakeStore) ClaimSession(_ context.Context, s Session) (int64, error) {
	f.mu.Lock()
	n, ok := f.nodes[s.NodeID]
	if !ok || n.RevokedAt != nil {
		f.mu.Unlock()
		return 0, ErrNotFound
	}
	f.epochs[s.NodeID]++
	s.Epoch = f.epochs[s.NodeID]
	f.sessions[s.NodeID] = s
	hook := f.afterClaim
	f.mu.Unlock()
	if hook != nil {
		hook(s.NodeID)
	}
	return s.Epoch, nil
}
func (f *fakeStore) MarkOfflineIfEpoch(_ context.Context, id string, epoch int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n, ok := f.nodes[id]; ok && n.RevokedAt == nil && f.epochs[id] == epoch {
		n.Status = StatusOffline
	}
	return nil
}
func (f *fakeStore) Session(_ context.Context, id string) (*Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}
	return &s, nil
}
func (f *fakeStore) ReleaseSession(_ context.Context, id string, epoch int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[id]; ok && s.Epoch == epoch {
		delete(f.sessions, id)
	}
	return nil
}
func (f *fakeStore) MarkStaleOffline(_ context.Context, before time.Time) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := []string{}
	for _, n := range f.nodes {
		seen := n.UpdatedAt
		if n.LastSeenAt != nil {
			seen = *n.LastSeenAt
		}
		if n.Status == StatusOnline && seen.Before(before) {
			n.Status = StatusOffline
			ids = append(ids, n.ID)
		}
	}
	return ids, nil
}
func (f *fakeStore) ApplyInventory(_ context.Context, id string, inv nodeproto.Inventory, version string, proto int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.nodes[id]
	if !ok || n.RevokedAt != nil {
		return ErrNotFound
	}
	n.Inventory, n.Version, n.ProtocolVersion = inv, version, proto
	return nil
}
func (f *fakeStore) AllowEndpoints(_ context.Context, id string, add []nodeproto.Endpoint) (*Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.allowErr != nil {
		return nil, f.allowErr
	}
	n, ok := f.nodes[id]
	if !ok || n.RevokedAt != nil {
		return nil, ErrNotFound
	}
	for _, e := range add {
		if !slices.Contains(n.Endpoints, e) {
			n.Endpoints = append(n.Endpoints, e)
		}
	}
	return clone(n), nil
}
func (f *fakeStore) UseNonce(_ context.Context, nonce string, exp time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.nonces[nonce]; ok {
		return ErrNonceUsed
	}
	f.nonces[nonce] = exp
	return nil
}
func (f *fakeStore) DeleteExpiredNonces(_ context.Context, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, exp := range f.nonces {
		if exp.Before(now) {
			delete(f.nonces, k)
		}
	}
	return nil
}

type fakeTokens struct {
	claim   *apikeys.NodeJoinClaim
	err     error
	peekErr error
	calls   int
}

func (t *fakeTokens) PeekNodeJoinToken(context.Context, string) (*apikeys.NodeJoinClaim, error) {
	if t.peekErr != nil {
		return nil, t.peekErr
	}
	if t.err != nil {
		return nil, t.err
	}
	return t.claim, nil
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

type fakeAudit struct {
	mu     sync.Mutex
	events []auditEvent
}

func (a *fakeAudit) Record(_ context.Context, action string, p security.Principal, _ Node, _ map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, auditEvent{action, p.UserID})
}

func (a *fakeAudit) list() []auditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.events)
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
	res, err := r.svc.Join(context.Background(), in2)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := r.store.GetByID(context.Background(), res.NodeID); n.Name != "web" {
		t.Fatalf("name %q", n.Name)
	}
	in3 := joinReq()
	in3.Name = "web"
	// The name is checked before the token is spent, so the operator keeps
	// the token and is told why.
	_, err = r.svc.Join(context.Background(), in3)
	if !errors.Is(err, ErrNameTaken) || errors.Is(err, ErrMintNewToken) {
		t.Fatalf("err %v", err)
	}
}

func TestJoinNameFromHostname(t *testing.T) {
	cases := map[string]string{"Box_1.Local": "box-1-local", "  web01 ": "web01", strings.Repeat("a", 80): strings.Repeat("a", 63)}
	for host, want := range cases {
		r := newRig(t, apikeys.NodeJoinScope{}, true)
		in := joinReq()
		in.Inventory.Hostname = host
		res, err := r.svc.Join(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		if n, _ := r.store.GetByID(context.Background(), res.NodeID); n.Name != want {
			t.Errorf("%q: name %q, want %q", host, n.Name, want)
		}
	}
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	in := joinReq()
	in.Inventory.Hostname = "___"
	res, _ := r.svc.Join(context.Background(), in)
	if n, _ := r.store.GetByID(context.Background(), res.NodeID); !strings.HasPrefix(n.Name, "node-") {
		t.Fatalf("name %q", n.Name)
	}
}

func TestJoinValidatesInput(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	bad := []func(*JoinRequest){
		func(in *JoinRequest) { in.Name = "Not A Label" },
		func(in *JoinRequest) { in.Name = strings.Repeat("a", 64) },
		func(in *JoinRequest) { in.Labels = map[string]string{strings.Repeat("k", 64): "v"} },
		func(in *JoinRequest) { in.Labels = map[string]string{"k": strings.Repeat("v", 256)} },
		func(in *JoinRequest) {
			in.Labels = map[string]string{}
			for i := 0; i < 33; i++ {
				in.Labels[strings.Repeat("k", 1)+string(rune('a'+i%26))+string(rune('a'+i/26))] = "v"
			}
		},
		func(in *JoinRequest) { in.PublicKey = []byte{1, 2, 3} },
	}
	for i, mut := range bad {
		in := joinReq()
		mut(&in)
		if _, err := r.svc.Join(context.Background(), in); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d: err %v", i, err)
		}
	}
	if r.tokens.calls != 0 {
		t.Fatal("invalid input must not burn the token")
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
		{"v1.8.0", "weird", false},
		{"v1.8.0", "", true},
		{"v1.8.0", "0.1.0-dev", true},
		{"0.1.0-dev", "v1.2.3", true},
		{"0.1.0-dev", "0.1.0-dev", true},
		{"1.8.0", "1.7.0", true},
		{"weird", "v1.0.0", true},
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
		if !c.ok && r.tokens.calls != 0 {
			t.Errorf("%s vs %s: a refused node burned its token", c.svc, c.node)
		}
	}
}

func TestJoinRefusesNewerProtocol(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	in := joinReq()
	in.ProtocolVersion = nodeproto.Version + 1
	_, err := r.svc.Join(context.Background(), in)
	if !errors.Is(err, ErrNodeNewer) || err.Error() != "this node is newer than Loomplane; update Loomplane first" || r.tokens.calls != 0 {
		t.Fatalf("err %v calls %d", err, r.tokens.calls)
	}
}

func TestJoinAdoptFailureRollsBackAndFreesName(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{CloudID: "cld_1"}, true)
	r.svc.Adopt = func(context.Context, *Node, *apikeys.NodeJoinClaim) error { return errors.New("cloud down") }
	in := joinReq()
	in.Name = "web"
	_, err := r.svc.Join(context.Background(), in)
	if !errors.Is(err, ErrMintNewToken) {
		t.Fatalf("err %v", err)
	}
	l, _ := r.store.List(context.Background(), "org_1")
	if len(l) != 1 || l[0].Status != StatusRevoked || l[0].RevokedBy != "system:join-rollback" || l[0].RevokedAt == nil {
		t.Fatalf("nodes %+v", l)
	}
	r.svc.Adopt = nil
	in2 := joinReq()
	in2.Name = "web"
	if _, err := r.svc.Join(context.Background(), in2); err != nil {
		t.Fatalf("retry: %v", err)
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

func TestJoinAdoptRefusedJoinsWithoutCloud(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{CloudID: "cld_other_org"}, true)
	r.svc.Adopt = func(context.Context, *Node, *apikeys.NodeJoinClaim) error {
		return fmt.Errorf("refused: %w", ErrAdoptionRefused)
	}
	res, err := r.svc.Join(context.Background(), joinReq())
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if res.CloudID != "" {
		t.Fatalf("join result names the refused Cloud %q", res.CloudID)
	}
	n, err := r.store.GetByID(context.Background(), res.NodeID)
	if err != nil || n.Status == StatusRevoked || n.RevokedAt != nil {
		t.Fatalf("node %+v err %v", n, err)
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

func TestUpdateIngressNotifies(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	id := seed(r, nodeproto.EndpointDocker)
	mode := "relay"
	if _, err := r.svc.Update(context.Background(), admin, id, Update{IngressMode: &mode}); err != nil {
		t.Fatal(err)
	}
	if len(r.sent) != 1 || r.sent[0].msg != (nodeproto.SetIngress{Mode: "relay"}) {
		t.Fatalf("sent %+v", r.sent)
	}
	r.sent = nil
	if _, err := r.svc.Update(context.Background(), admin, id, Update{IngressMode: &mode}); err != nil || len(r.sent) != 0 {
		t.Fatalf("unchanged: %v %+v", err, r.sent)
	}
}

func TestUpdateNothingChangedIsNotAudited(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	id := seed(r, nodeproto.EndpointDocker)
	if _, err := r.svc.Update(context.Background(), admin, id, Update{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}}); err != nil {
		t.Fatal(err)
	}
	if len(r.audit.events) != 0 {
		t.Fatalf("audit %+v", r.audit.events)
	}
	// Ruling R13: any endpoints update is sent, changed or not, so a node
	// that missed an earlier narrow converges.
	if len(r.sent) != 1 || !reflect.DeepEqual(r.sent[0].msg, nodeproto.Narrow{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}}) {
		t.Fatalf("sent %+v", r.sent)
	}
	r.sent = nil
	name := "renamed"
	if _, err := r.svc.Update(context.Background(), admin, id, Update{Name: &name}); err != nil || len(r.sent) != 0 {
		t.Fatalf("rename sent %+v %v", r.sent, err)
	}
}

func TestJoinHostnameCollisionRetriesWithSuffix(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	first := joinReq()
	first.Inventory.Hostname = "box"
	if _, err := r.svc.Join(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := joinReq()
	second.Inventory.Hostname = "box"
	res, err := r.svc.Join(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := r.store.GetByID(context.Background(), res.NodeID)
	want := "box-" + strings.ReplaceAll(strings.TrimPrefix(res.NodeID, "node_"), "-", "")[:4]
	if n.Name != want {
		t.Fatalf("name %q, want %q", n.Name, want)
	}
	// A long hostname still gives a DNS label.
	long := joinReq()
	long.Inventory.Hostname = strings.Repeat("a", 80)
	if _, err = r.svc.Join(context.Background(), long); err != nil {
		t.Fatal(err)
	}
	long2 := joinReq()
	long2.Inventory.Hostname = strings.Repeat("a", 80)
	res, err = r.svc.Join(context.Background(), long2)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := r.store.GetByID(context.Background(), res.NodeID); len(n.Name) > 63 || !dnsLabel.MatchString(n.Name) {
		t.Fatalf("long name %q", n.Name)
	}
}

func TestJoinExplicitNameCollisionDoesNotRetry(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	in := joinReq()
	in.Name = "web"
	if _, err := r.svc.Join(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	in2 := joinReq()
	in2.Name = "web"
	in2.Inventory.Hostname = "box"
	if _, err := r.svc.Join(context.Background(), in2); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("err %v", err)
	}
}

func TestJoinRollbackSurvivesCancelAndLogsFailure(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The caller goes away while adopting; the rollback still lands.
	r.svc.Adopt = func(context.Context, *Node, *apikeys.NodeJoinClaim) error { cancel(); return errors.New("cloud down") }
	if _, err := r.svc.Join(ctx, joinReq()); !errors.Is(err, ErrMintNewToken) {
		t.Fatalf("err %v", err)
	}
	l, _ := r.store.List(context.Background(), "org_1")
	if len(l) != 1 || l[0].Status != StatusRevoked {
		t.Fatalf("nodes %+v", l)
	}

	var buf bytes.Buffer
	r2 := newRig(t, apikeys.NodeJoinScope{}, true)
	r2.svc.Log = slog.New(slog.NewTextHandler(&buf, nil))
	r2.svc.Adopt = func(context.Context, *Node, *apikeys.NodeJoinClaim) error {
		r2.store.mu.Lock()
		r2.store.fail = errors.New("db down")
		r2.store.mu.Unlock()
		return errors.New("cloud down")
	}
	if _, err := r2.svc.Join(context.Background(), joinReq()); !errors.Is(err, ErrMintNewToken) {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(buf.String(), "db down") {
		t.Fatalf("rollback failure not logged: %q", buf.String())
	}
}

func TestUpdateEmptyIsARealChange(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	id := seed(r, nodeproto.EndpointDocker)
	n, err := r.svc.Update(context.Background(), admin, id, Update{Endpoints: []nodeproto.Endpoint{}, Labels: map[string]string{}})
	if err != nil || len(n.Endpoints) != 0 || len(n.Labels) != 0 {
		t.Fatalf("err %v node %+v", err, n)
	}
	if len(r.sent) != 1 {
		t.Fatalf("sent %+v", r.sent)
	}
}

func TestUpdateKeepsConcurrentStatus(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	id := seed(r, nodeproto.EndpointDocker)
	name := "renamed"
	// A heartbeat flips the status between the read and the write.
	r.store.nodes[id].Status = StatusOffline
	n, err := r.svc.Update(context.Background(), admin, id, Update{Name: &name})
	if err != nil || n.Status != StatusOffline {
		t.Fatalf("err %v node %+v", err, n)
	}
}

func TestRevokeTwiceNotifiesAgain(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	id := seed(r)
	for i := 0; i < 2; i++ {
		if err := r.svc.Revoke(context.Background(), admin, id); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.sent) != 2 || len(r.audit.events) != 1 {
		t.Fatalf("sent %d audit %d", len(r.sent), len(r.audit.events))
	}
}

func TestOtherOrgIsNotFound(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	id := seed(r)
	other := security.Principal{UserID: "usr_9", OrgID: "org_2"}
	if _, err := r.svc.Get(context.Background(), other, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get: %v", err)
	}
	if err := r.svc.Revoke(context.Background(), other, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke: %v", err)
	}
	if n, _ := r.store.GetByID(context.Background(), id); n.Status == StatusRevoked {
		t.Fatal("revoked across orgs")
	}
}

func TestReadOnlyPrincipalCannotWrite(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	id := seed(r, nodeproto.EndpointDocker)
	ro := security.Principal{UserID: "usr_1", OrgID: "org_1", ReadOnly: true}
	if err := r.svc.Revoke(context.Background(), ro, id); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := r.svc.Update(context.Background(), ro, id, Update{}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("update: %v", err)
	}
	if _, err := r.svc.Get(context.Background(), ro, id); err != nil {
		t.Fatalf("get: %v", err)
	}
}

func TestJoinRefusesAKnownKeyBeforeSpending(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	first := joinReq()
	res, err := r.svc.Join(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.svc.Revoke(context.Background(), admin, res.NodeID); err != nil {
		t.Fatal(err)
	}
	spent := r.tokens.calls
	again := joinReq()
	again.PublicKey = first.PublicKey
	_, err = r.svc.Join(context.Background(), again)
	var jc *JoinConflictError
	if !errors.As(err, &jc) || !errors.Is(err, ErrKeyKnown) || jc.Spent {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(err.Error(), "identity.key") || !strings.Contains(err.Error(), "not used") {
		t.Fatalf("message %q", err)
	}
	if r.tokens.calls != spent {
		t.Fatal("the token was spent on a key Loomplane already knows")
	}
}

func TestJoinKnownKeyRaceAfterSpending(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	r.store.skipPrecheck = true
	first := joinReq()
	if _, err := r.svc.Join(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	again := joinReq()
	again.PublicKey = first.PublicKey
	_, err := r.svc.Join(context.Background(), again)
	var jc *JoinConflictError
	if !errors.As(err, &jc) || !errors.Is(err, ErrKeyKnown) || !jc.Spent || !strings.Contains(err.Error(), "mint a new join token") {
		t.Fatalf("err %v", err)
	}
}

func TestJoinTakenNameRefusedBeforeSpending(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	in := joinReq()
	in.Name = "web"
	if _, err := r.svc.Join(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	spent := r.tokens.calls
	in2 := joinReq()
	in2.Name = "web"
	_, err := r.svc.Join(context.Background(), in2)
	var jc *JoinConflictError
	if !errors.As(err, &jc) || !errors.Is(err, ErrNameTaken) || jc.Spent {
		t.Fatalf("err %v", err)
	}
	for _, want := range []string{`"web"`, "--name", "not used"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message %q lacks %q", err, want)
		}
	}
	if r.tokens.calls != spent {
		t.Fatal("the token was spent on a name that is taken")
	}
}

func TestJoinTakenNameRaceSaysSoAfterSpending(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	in := joinReq()
	in.Name = "web"
	if _, err := r.svc.Join(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	r.store.skipPrecheck = true
	in2 := joinReq()
	in2.Name = "web"
	_, err := r.svc.Join(context.Background(), in2)
	var jc *JoinConflictError
	if !errors.As(err, &jc) || !errors.Is(err, ErrNameTaken) || !jc.Spent {
		t.Fatalf("err %v", err)
	}
	for _, want := range []string{`"web"`, "--name", "mint a new join token"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message %q lacks %q", err, want)
		}
	}
}

func TestJoinPeekErrorsPassThroughWithoutSpending(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	r.tokens.peekErr = apikeys.ErrRejected
	if _, err := r.svc.Join(context.Background(), joinReq()); !errors.Is(err, apikeys.ErrRejected) {
		t.Fatalf("err %v", err)
	}
	if r.tokens.calls != 0 {
		t.Fatal("a token the peek refused was spent")
	}
}

func TestJoinBoundsWhatTheMachineReports(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	in := joinReq()
	in.NodeVersion = "v1.8.0-" + strings.Repeat("x", 500)
	in.Inventory.Kernel = strings.Repeat("k", 5000)
	res, err := r.svc.Join(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := r.store.GetByID(context.Background(), res.NodeID)
	if len(n.Version) > nodeproto.MaxVersionLen || len(n.Inventory.Kernel) > nodeproto.MaxFieldLen {
		t.Fatalf("stored a %d byte version and a %d byte kernel", len(n.Version), len(n.Inventory.Kernel))
	}
}
