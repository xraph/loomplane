package nodes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"io"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// memStore is the part of nodes.Store the routes and one session use. A
// method it does not implement panics through the nil embedded interface.
type memStore struct {
	nodes.Store
	mu       sync.Mutex
	nodes    map[string]*nodes.Node
	epochs   map[string]int64
	sessions map[string]nodes.Session
	nonces   map[string]bool
}

func newMemStore() *memStore {
	return &memStore{nodes: map[string]*nodes.Node{}, epochs: map[string]int64{}, sessions: map[string]nodes.Session{}, nonces: map[string]bool{}}
}

func (m *memStore) Create(_ context.Context, n *nodes.Node) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := *n
	m.nodes[n.ID] = &c
	return nil
}

func (m *memStore) Get(_ context.Context, org, id string) (*nodes.Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n, ok := m.nodes[id]; ok && n.OrgID == org {
		c := *n
		return &c, nil
	}
	return nil, nodes.ErrNotFound
}

func (m *memStore) GetByID(_ context.Context, id string) (*nodes.Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n, ok := m.nodes[id]; ok {
		c := *n
		return &c, nil
	}
	return nil, nodes.ErrNotFound
}

func (m *memStore) List(_ context.Context, org string) ([]nodes.Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []nodes.Node{}
	for _, n := range m.nodes {
		if n.OrgID == org {
			out = append(out, *n)
		}
	}
	return out, nil
}

func (m *memStore) Update(_ context.Context, n *nodes.Node) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := *n
	m.nodes[n.ID] = &c
	return nil
}

func (m *memStore) UpdateSettings(_ context.Context, org, id string, u nodes.SettingsUpdate) (*nodes.Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok || n.OrgID != org {
		return nil, nodes.ErrNotFound
	}
	if u.Name != nil {
		n.Name = *u.Name
	}
	if u.Endpoints != nil {
		n.Endpoints = u.Endpoints
	}
	c := *n
	return &c, nil
}

func (m *memStore) SetStatus(_ context.Context, id string, s nodes.Status) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n, ok := m.nodes[id]; ok {
		n.Status = s
	}
	return nil
}

func (m *memStore) Touch(context.Context, string, time.Time, nodeproto.Usage) error { return nil }

func (m *memStore) ClaimSession(_ context.Context, s nodes.Session) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n, ok := m.nodes[s.NodeID]; !ok || n.RevokedAt != nil {
		return 0, nodes.ErrNotFound
	}
	m.epochs[s.NodeID]++
	s.Epoch = m.epochs[s.NodeID]
	m.sessions[s.NodeID] = s
	return s.Epoch, nil
}

func (m *memStore) Session(_ context.Context, id string) (*nodes.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		return &s, nil
	}
	return nil, nodes.ErrNotFound
}

func (m *memStore) ReleaseSession(_ context.Context, id string, epoch int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok && s.Epoch == epoch {
		delete(m.sessions, id)
	}
	return nil
}

func (m *memStore) MarkOfflineIfEpoch(_ context.Context, id string, epoch int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n, ok := m.nodes[id]; ok && n.RevokedAt == nil && m.epochs[id] == epoch {
		n.Status = nodes.StatusOffline
	}
	return nil
}

func (m *memStore) UseNonce(_ context.Context, nonce string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nonces[nonce] {
		return nodes.ErrNonceUsed
	}
	m.nonces[nonce] = true
	return nil
}

func (m *memStore) status(id string) nodes.Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nodes[id].Status
}

type authz struct{ deny map[string]bool }

func (a authz) Allowed(_ context.Context, p security.Principal, perm security.Permission) (bool, error) {
	return !a.deny[p.UserID+":"+perm.Name()], nil
}

type tokens struct {
	claim *apikeys.NodeJoinClaim
	err   error
	spent int
}

func (t *tokens) ConsumeNodeJoinToken(context.Context, string) (*apikeys.NodeJoinClaim, error) {
	t.spent++
	return t.claim, t.err
}

type minter struct {
	scope apikeys.NodeJoinScope
	who   security.Principal
	life  time.Duration
	err   error
}

func (m *minter) CreateNodeJoinToken(_ context.Context, p security.Principal, scope apikeys.NodeJoinScope, life time.Duration) (*apikeys.NodeJoinToken, error) {
	m.who, m.scope, m.life = p, scope, life
	if m.err != nil {
		return nil, m.err
	}
	return &apikeys.NodeJoinToken{ID: "key_1", Secret: "lpj_live_abc123", ExpiresAt: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}, nil
}

const publicURL = "https://loom.example.com"

var member = security.Principal{UserID: "usr_1", OrgID: "org_1"}

type rig struct {
	ext    *Extension
	store  *memStore
	tokens *tokens
	minter *minter
	router forge.Router
}

func newRig(t *testing.T, denied ...string) *rig {
	t.Helper()
	r := &rig{store: newMemStore(), tokens: &tokens{claim: &apikeys.NodeJoinClaim{OrgID: "org_1", MintedBy: "usr_1", MintedByKeyID: "key_1"}}, minter: &minter{}}
	deny := map[string]bool{}
	for _, d := range denied {
		deny[d] = true
	}
	svc := &nodes.Service{Store: r.store, Tokens: r.tokens, Authz: authz{deny}, Version: "v1.8.0"}
	hub := nodes.NewHub(nodes.HubConfig{Audience: publicURL, Replica: "test", HopSecret: []byte("hop-secret-hop-secret-hop-secret!"), ChallengeSecret: []byte("challenge-secret-challenge-secret")})
	if err := hub.Bind(r.store, svc); err != nil {
		t.Fatal(err)
	}
	r.ext = &Extension{BaseExtension: forge.NewBaseExtension(Name, "test", ""), cfg: config.Config{PublicURL: publicURL}, hub: hub, svc: svc, minter: r.minter}
	r.ext.SetLogger(forge.NewNoopLogger())
	r.router = forge.NewRouter()
	if err := r.ext.routes(r.router); err != nil {
		t.Fatal(err)
	}
	if err := r.ext.mountNodeEndpoints(r.router); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) seed(t *testing.T, eps ...nodeproto.Endpoint) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if eps == nil {
		eps = []nodeproto.Endpoint{}
	}
	now := time.Now().UTC()
	if err = r.store.Create(t.Context(), &nodes.Node{ID: "node_1", OrgID: "org_1", Name: "web-1", PublicKey: pub, KeyFingerprint: nodeproto.Fingerprint(pub),
		Status: nodes.StatusPending, Endpoints: eps, IngressMode: "none", Role: "node", Version: "v1.8.0", ProtocolVersion: nodeproto.Version, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return priv
}

// do sends a request as p, or with no session when p is nil.
func (r *rig) do(t *testing.T, method, path, body string, p *security.Principal) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = bytes.NewBufferString(body)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if p != nil {
		req = req.WithContext(security.WithPrincipal(req.Context(), *p))
	}
	out := httptest.NewRecorder()
	r.router.ServeHTTP(out, req)
	return out
}
