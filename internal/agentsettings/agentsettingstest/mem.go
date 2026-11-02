// Package agentsettingstest keeps a workspace's AI settings in memory for
// tests: one store that is both aisettings.Store and agentsettings.Store
// over the same rows, so the agent's writes and the workspace's share one
// revision, as they share one row in Postgres. And provider key values in
// memory, with a switch that makes reading them fail.
package agentsettingstest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/aisettings"
)

// Store is both stores in memory, with the Postgres stores' rules: one
// revision per workspace that every settings write checks or bumps, a
// budget that can't go below its allocations, allocations that can't pass
// the budget, and one nexus tenant per workspace.
type Store struct {
	mu          sync.Mutex
	rows        map[string]*row
	keys        map[string]map[string]aisettings.ProviderKey
	allocations map[string]map[string]aisettings.Allocation
}

type row struct {
	ai    aisettings.Settings
	agent agentsettings.Settings
}

var (
	_ aisettings.Store    = (*Store)(nil)
	_ agentsettings.Store = (*Store)(nil)
)

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{
		rows:        map[string]*row{},
		keys:        map[string]map[string]aisettings.ProviderKey{},
		allocations: map[string]map[string]aisettings.Allocation{},
	}
}

func defaults(orgID string) *row {
	return &row{
		ai:    aisettings.Settings{OrgID: orgID, MonthlyBudgetUSD: "0.00", Embeddings: aisettings.LocalEmbeddings()},
		agent: agentsettings.Settings{OrgID: orgID},
	}
}

// get returns the workspace's row, or defaults it doesn't keep.
func (s *Store) get(orgID string) *row {
	if r, ok := s.rows[orgID]; ok {
		return r
	}
	return defaults(orgID)
}

// keep stores r with the next revision.
func (s *Store) keep(r *row) {
	r.ai.Revision++
	r.agent.Revision = r.ai.Revision
	r.ai.UpdatedAt = time.Now().UTC()
	r.agent.UpdatedAt = r.ai.UpdatedAt
	s.rows[r.ai.OrgID] = r
}

func cents(v string) int64 {
	r, ok := new(big.Rat).SetString(v)
	if !ok {
		panic("bad amount " + v)
	}
	r.Mul(r, big.NewRat(100, 1))
	return r.Num().Int64() / r.Denom().Int64()
}

func usd(c int64) string { return fmt.Sprintf("%d.%02d", c/100, c%100) }

// Settings implements aisettings.Store.
func (s *Store) Settings(_ context.Context, orgID string) (*aisettings.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.get(orgID).ai
	return &out, nil
}

// SaveSettings implements aisettings.Store.
func (s *Store) SaveSettings(_ context.Context, in *aisettings.Settings, expectRevision int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.get(in.OrgID)
	if r.ai.Revision != expectRevision {
		return aisettings.ErrStale
	}
	r.ai.Embeddings, r.ai.UpdatedBy = in.Embeddings, in.UpdatedBy
	s.keep(r)
	*in = r.ai
	return nil
}

// ProviderKeys implements aisettings.Store.
func (s *Store) ProviderKeys(_ context.Context, orgID string) ([]*aisettings.ProviderKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*aisettings.ProviderKey, 0, len(s.keys[orgID]))
	for _, name := range slices.Sorted(maps.Keys(s.keys[orgID])) {
		k := s.keys[orgID][name]
		out = append(out, &k)
	}
	return out, nil
}

// ProviderKey implements aisettings.Store.
func (s *Store) ProviderKey(_ context.Context, orgID, name string) (*aisettings.ProviderKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[orgID][name]
	if !ok {
		return nil, aisettings.ErrNotFound
	}
	k.Fields = maps.Clone(k.Fields)
	k.Projects = slices.Clone(k.Projects)
	return &k, nil
}

// SaveProviderKey implements aisettings.Store.
func (s *Store) SaveProviderKey(_ context.Context, k *aisettings.ProviderKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys[k.OrgID] == nil {
		s.keys[k.OrgID] = map[string]aisettings.ProviderKey{}
	}
	now := time.Now().UTC()
	if old, ok := s.keys[k.OrgID][k.Name]; ok {
		k.ID, k.CreatedBy, k.CreatedAt, k.Revision = old.ID, old.CreatedBy, old.CreatedAt, old.Revision+1
	} else {
		k.CreatedAt, k.Revision = now, 1
	}
	if k.Projects == nil {
		k.Projects = []string{}
	}
	k.UpdatedAt = now
	stored := *k
	stored.Fields = maps.Clone(k.Fields)
	stored.Projects = slices.Clone(k.Projects)
	s.keys[k.OrgID][k.Name] = stored
	return nil
}

// CreateProviderKey implements aisettings.Store.
func (s *Store) CreateProviderKey(ctx context.Context, k *aisettings.ProviderKey) error {
	s.mu.Lock()
	_, taken := s.keys[k.OrgID][k.Name]
	s.mu.Unlock()
	if taken {
		return aisettings.ErrStale
	}
	return s.SaveProviderKey(ctx, k)
}

// DeleteProviderKey implements aisettings.Store.
func (s *Store) DeleteProviderKey(_ context.Context, orgID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.keys[orgID][name]; !ok {
		return aisettings.ErrNotFound
	}
	delete(s.keys[orgID], name)
	return nil
}

// RestrictKey grants a stored key to projects alone, as a write from
// somewhere else might.
func (s *Store) RestrictKey(orgID, name string, projects ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.keys[orgID][name]
	k.Projects = projects
	s.keys[orgID][name] = k
}

// SetKeyField changes one non-secret field of a stored key, as a write
// from somewhere else might.
func (s *Store) SetKeyField(orgID, name, field, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.keys[orgID][name]
	k.Fields = maps.Clone(k.Fields)
	if k.Fields == nil {
		k.Fields = map[string]string{}
	}
	k.Fields[field] = value
	s.keys[orgID][name] = k
}

// Allocations implements aisettings.Store.
func (s *Store) Allocations(_ context.Context, orgID string) ([]*aisettings.Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*aisettings.Allocation, 0, len(s.allocations[orgID]))
	for _, scope := range slices.Sorted(maps.Keys(s.allocations[orgID])) {
		a := s.allocations[orgID][scope]
		out = append(out, &a)
	}
	return out, nil
}

// SetAllocation implements aisettings.Store.
func (s *Store) SetAllocation(_ context.Context, orgID string, a *aisettings.Allocation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var others int64
	for scope, b := range s.allocations[orgID] {
		if scope != a.Scope {
			others += cents(b.MonthlyBudgetUSD)
		}
	}
	left := cents(s.get(orgID).ai.MonthlyBudgetUSD) - others
	if cents(a.MonthlyBudgetUSD) > left {
		return fmt.Errorf("%w: %s USD of it is left to allocate", aisettings.ErrOverBudget, usd(max(left, 0)))
	}
	if s.allocations[orgID] == nil {
		s.allocations[orgID] = map[string]aisettings.Allocation{}
	}
	s.allocations[orgID][a.Scope] = *a
	return nil
}

// DeleteAllocation implements aisettings.Store.
func (s *Store) DeleteAllocation(_ context.Context, orgID, scope string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.allocations[orgID], scope)
	return nil
}

// SetBudget implements aisettings.Store.
func (s *Store) SetBudget(_ context.Context, orgID, budgetUSD, by string) (*aisettings.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var allocated int64
	for _, a := range s.allocations[orgID] {
		allocated += cents(a.MonthlyBudgetUSD)
	}
	if cents(budgetUSD) < allocated {
		return nil, fmt.Errorf("%w: %s USD is allocated", aisettings.ErrUnderAllocated, usd(allocated))
	}
	r := s.get(orgID)
	if r.ai.MonthlyBudgetUSD != budgetUSD {
		r.ai.MonthlyBudgetUSD, r.ai.UpdatedBy = budgetUSD, by
		s.keep(r)
	}
	out := r.ai
	return &out, nil
}

// SetHuggingFaceToken implements aisettings.Store.
func (s *Store) SetHuggingFaceToken(_ context.Context, orgID, hint, account string) (*aisettings.HuggingFaceToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.get(orgID)
	rev := int64(1)
	if r.ai.HuggingFace != nil {
		rev = r.ai.HuggingFace.Revision + 1
	}
	r.ai.HuggingFace = &aisettings.HuggingFaceToken{Hint: hint, Account: account, SetAt: time.Now().UTC(), Revision: rev}
	s.rows[orgID] = r
	out := *r.ai.HuggingFace
	return &out, nil
}

// ClearHuggingFaceToken implements aisettings.Store.
func (s *Store) ClearHuggingFaceToken(_ context.Context, orgID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rows[orgID]; ok {
		r.ai.HuggingFace = nil
	}
	return nil
}

// LockHuggingFaceToken implements aisettings.Store.
func (s *Store) LockHuggingFaceToken(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

// Get implements agentsettings.Store.
func (s *Store) Get(_ context.Context, orgID string) (*agentsettings.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.get(orgID).agent
	return &out, nil
}

// ByNexusTenant implements agentsettings.Store.
func (s *Store) ByNexusTenant(_ context.Context, tenantID string) (*agentsettings.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tenantID != "" {
		for _, r := range s.rows {
			if r.agent.NexusTenantID == tenantID {
				out := r.agent
				return &out, nil
			}
		}
	}
	return nil, agentsettings.ErrNotFound
}

// Save implements agentsettings.Store.
func (s *Store) Save(_ context.Context, in *agentsettings.Settings, expectRevision int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.get(in.OrgID)
	if r.agent.Revision != expectRevision {
		return agentsettings.ErrStale
	}
	if in.NexusTenantID != "" {
		for org, other := range s.rows {
			if org != in.OrgID && other.agent.NexusTenantID == in.NexusTenantID {
				return errors.New("agentsettingstest: two workspaces can't share a nexus tenant")
			}
		}
	}
	r.agent.Enabled, r.agent.ProviderKey, r.agent.Model = in.Enabled, in.ProviderKey, in.Model
	r.agent.Knowledge, r.agent.NexusTenantID, r.agent.UpdatedBy = in.Knowledge, in.NexusTenantID, in.UpdatedBy
	s.keep(r)
	*in = r.agent
	return nil
}

// Keys keeps provider key values in memory. After SetFail, every read
// fails.
type Keys struct {
	mu     sync.Mutex
	values map[string]map[string]string
	fail   error
}

var _ aisettings.Keys = (*Keys)(nil)

// NewKeys returns empty Keys.
func NewKeys() *Keys { return &Keys{values: map[string]map[string]string{}} }

// Put implements aisettings.Keys.
func (k *Keys) Put(_ context.Context, orgID, keyID string, secrets map[string]string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.values[orgID+"/"+keyID] = maps.Clone(secrets)
	return nil
}

// Reveal implements aisettings.Keys.
func (k *Keys) Reveal(_ context.Context, orgID, keyID string) (map[string]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fail != nil {
		return nil, k.fail
	}
	v, ok := k.values[orgID+"/"+keyID]
	if !ok {
		return nil, aisettings.ErrNotFound
	}
	return maps.Clone(v), nil
}

// Delete implements aisettings.Keys.
func (k *Keys) Delete(_ context.Context, orgID, keyID string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.values, orgID+"/"+keyID)
	return nil
}

// SetFail makes every read fail with err, or succeed again with nil.
func (k *Keys) SetFail(err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.fail = err
}
