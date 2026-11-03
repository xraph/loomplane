// Package nodestest is an in-memory node store, a join token spender and
// helpers to build a real nodes.Hub over them, so the machine side can be
// tested against the real hub and the real handlers (extensions/nodes
// NodeHandler) without a database. Only tests may import it; nothing outside
// a _test.go file does.
package nodestest

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// Store is an in-memory nodes.Store.
type Store struct {
	// BeforeClaim, when set, runs at the start of ClaimSession, outside the
	// lock. Tests use it to revoke a node between authentication and claim.
	BeforeClaim func(nodeID string)

	mu       sync.Mutex
	allowErr error
	nodes    map[string]*nodes.Node
	epochs   map[string]int64
	sessions map[string]nodes.Session
	nonces   map[string]time.Time
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{nodes: map[string]*nodes.Node{}, epochs: map[string]int64{}, sessions: map[string]nodes.Session{}, nonces: map[string]time.Time{}}
}

func clone(n *nodes.Node) *nodes.Node {
	c := *n
	c.Endpoints = slices.Clone(n.Endpoints)
	return &c
}

// Node returns a copy of the stored node, or nil.
func (s *Store) Node(id string) *nodes.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.nodes[id]; ok {
		return clone(n)
	}
	return nil
}

// Revoke marks the node revoked, as the terminal state the service writes.
func (s *Store) Revoke(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.nodes[id]; ok {
		now := time.Now().UTC()
		n.Status, n.RevokedAt = nodes.StatusRevoked, &now
	}
}

func (s *Store) Create(_ context.Context, n *nodes.Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.nodes {
		if n.KeyFingerprint != "" && o.KeyFingerprint == n.KeyFingerprint {
			return nodes.ErrKeyKnown
		}
		if o.OrgID == n.OrgID && o.Name == n.Name && o.Status != nodes.StatusRevoked {
			return nodes.ErrNameTaken
		}
	}
	s.nodes[n.ID] = clone(n)
	return nil
}

func (s *Store) KeyKnown(_ context.Context, fingerprint string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.nodes {
		if o.KeyFingerprint == fingerprint {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) NameTaken(_ context.Context, org, name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.nodes {
		if o.OrgID == org && o.Name == name && o.Status != nodes.StatusRevoked {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) Get(_ context.Context, org, id string) (*nodes.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.nodes[id]; ok && n.OrgID == org {
		return clone(n), nil
	}
	return nil, nodes.ErrNotFound
}

func (s *Store) GetByID(_ context.Context, id string) (*nodes.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.nodes[id]; ok {
		return clone(n), nil
	}
	return nil, nodes.ErrNotFound
}

func (s *Store) List(_ context.Context, org string) ([]nodes.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []nodes.Node
	for _, n := range s.nodes {
		if n.OrgID == org {
			out = append(out, *clone(n))
		}
	}
	return out, nil
}

func (s *Store) ListJoinedByKey(_ context.Context, org, keyID string) ([]nodes.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []nodes.Node
	for _, n := range s.nodes {
		if n.OrgID == org && n.JoinedByKeyID == keyID {
			out = append(out, *clone(n))
		}
	}
	return out, nil
}

func (s *Store) Update(_ context.Context, n *nodes.Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.nodes[n.ID]
	if !ok || old.OrgID != n.OrgID {
		return nodes.ErrNotFound
	}
	c := clone(n)
	if old.RevokedAt != nil { // terminal, as in SQL
		c.RevokedAt, c.RevokedBy, c.Status = old.RevokedAt, old.RevokedBy, nodes.StatusRevoked
	} else if c.RevokedAt != nil {
		c.Status = nodes.StatusRevoked
	}
	s.nodes[n.ID] = c
	return nil
}

func (s *Store) UpdateSettings(_ context.Context, org, id string, u nodes.SettingsUpdate) (*nodes.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[id]
	if !ok || n.OrgID != org || n.RevokedAt != nil {
		return nil, nodes.ErrNotFound
	}
	if u.Endpoints != nil {
		for _, e := range u.Endpoints {
			if !slices.Contains(n.Endpoints, e) {
				return nil, nodes.ErrWidenRemotely
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
	if u.IngressMode != nil && *u.IngressMode != n.IngressMode {
		now := time.Now().UTC()
		n.IngressMode, n.IngressSentAt, n.IngressDetail = *u.IngressMode, &now, ""
	}
	return clone(n), nil
}

func (s *Store) SetIngress(_ context.Context, id string, in nodes.IngressSetting) (*nodes.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[id]
	if !ok || n.RevokedAt != nil {
		return nil, nodes.ErrNotFound
	}
	sent := in.SentAt
	n.IngressMode, n.PublicAddress, n.PublicAddressConfirmedAt, n.IngressDetail, n.IngressSentAt = in.Mode, in.PublicAddress, in.ConfirmedAt, in.Detail, &sent
	return clone(n), nil
}

func (s *Store) RecordIngressApplied(_ context.Context, id, mode string, epoch int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[id]
	if !ok || n.RevokedAt != nil || s.epochs[id] != epoch {
		return nodes.ErrNotFound
	}
	n.IngressApplied = mode
	return nil
}

func (s *Store) MarkIngressSent(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[id]
	if !ok || n.RevokedAt != nil {
		return nodes.ErrNotFound
	}
	n.IngressSentAt = &at
	return nil
}

func (s *Store) SetIngressDetail(_ context.Context, id, detail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[id]
	if !ok || n.RevokedAt != nil {
		return nodes.ErrNotFound
	}
	n.IngressDetail = detail
	return nil
}

func (s *Store) Touch(_ context.Context, id string, at time.Time, _ nodeproto.Usage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[id]
	if !ok {
		return nodes.ErrNotFound
	}
	n.LastSeenAt = &at
	return nil
}

func (s *Store) SetStatus(_ context.Context, id string, st nodes.Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[id]
	if !ok || (n.RevokedAt != nil && st != nodes.StatusRevoked) {
		return nodes.ErrNotFound
	}
	n.Status = st
	return nil
}

func (s *Store) ClaimSession(_ context.Context, sess nodes.Session) (int64, error) {
	if s.BeforeClaim != nil {
		s.BeforeClaim(sess.NodeID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[sess.NodeID]
	if !ok || n.RevokedAt != nil {
		return 0, nodes.ErrNotFound
	}
	s.epochs[sess.NodeID]++
	sess.Epoch = s.epochs[sess.NodeID]
	s.sessions[sess.NodeID] = sess
	return sess.Epoch, nil
}

func (s *Store) MarkOfflineIfEpoch(_ context.Context, id string, epoch int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.nodes[id]; ok && n.RevokedAt == nil && s.epochs[id] == epoch {
		n.Status = nodes.StatusOffline
	}
	return nil
}

func (s *Store) Session(_ context.Context, id string) (*nodes.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, nodes.ErrNotFound
	}
	return &sess, nil
}

func (s *Store) ReleaseSession(_ context.Context, id string, epoch int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[id]; ok && sess.Epoch == epoch {
		delete(s.sessions, id)
	}
	return nil
}

func (s *Store) MarkStaleOffline(_ context.Context, before time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := []string{}
	for _, n := range s.nodes {
		seen := n.UpdatedAt
		if n.LastSeenAt != nil {
			seen = *n.LastSeenAt
		}
		if n.Status == nodes.StatusOnline && seen.Before(before) {
			n.Status = nodes.StatusOffline
			ids = append(ids, n.ID)
		}
	}
	return ids, nil
}

func (s *Store) ApplyInventory(_ context.Context, id string, inv nodeproto.Inventory, version string, proto int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[id]
	if !ok || n.RevokedAt != nil {
		return nodes.ErrNotFound
	}
	n.Inventory, n.Version, n.ProtocolVersion = inv, version, proto
	return nil
}

// FailAllow makes AllowEndpoints return err, as a store outage would; nil
// restores it.
func (s *Store) FailAllow(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.allowErr = err
}

func (s *Store) AllowEndpoints(_ context.Context, id string, add []nodeproto.Endpoint) (*nodes.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.allowErr != nil {
		return nil, s.allowErr
	}
	n, ok := s.nodes[id]
	if !ok || n.RevokedAt != nil {
		return nil, nodes.ErrNotFound
	}
	for _, e := range add {
		if !slices.Contains(n.Endpoints, e) {
			n.Endpoints = append(n.Endpoints, e)
		}
	}
	return clone(n), nil
}

func (s *Store) UseNonce(_ context.Context, nonce string, exp time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nonces[nonce]; ok {
		return nodes.ErrNonceUsed
	}
	s.nonces[nonce] = exp
	return nil
}

func (s *Store) DeleteExpiredNonces(_ context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, exp := range s.nonces {
		if exp.Before(now) {
			delete(s.nonces, k)
		}
	}
	return nil
}

var _ nodes.Store = (*Store)(nil)
