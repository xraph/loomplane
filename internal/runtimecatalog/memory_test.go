package runtimecatalog

import (
	"context"
	"encoding/json"
	"sync"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// MemoryStore is a transaction fixture. Production uses PgStore.
type MemoryStore struct {
	mu       sync.Mutex
	states   map[string]AgentState
	views    map[string]View
	sequence uint64
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{states: map[string]AgentState{}, views: map[string]View{}}
}
func clone[T any](in T) T {
	raw, err := json.Marshal(in)
	if err != nil {
		panic(err)
	}
	var out T
	if err = json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}
func (s *MemoryStore) Register(_ context.Context, reg Registration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[reg.Agent.AgentID] = AgentState{Registration: clone(reg), Records: map[string]Record{}}
	return nil
}
func (s *MemoryStore) Update(_ context.Context, agent domain.AgentIdentity, fn func(*AgentState) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := clone(s.states[agent.AgentID])
	if err := checkAgent(&state, agent); err != nil {
		return err
	}
	if err := fn(&state); err != nil {
		return err
	}
	s.states[agent.AgentID] = state
	s.sequence++
	return nil
}
func (s *MemoryStore) Read(_ context.Context, agent domain.AgentIdentity) ([]AgentState, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[agent.AgentID]
	if err := checkAgent(&state, agent); err != nil {
		return nil, 0, err
	}
	out := []AgentState{}
	for _, state := range s.states {
		if state.Registration.Agent.OrgID == agent.OrgID {
			out = append(out, clone(state))
		}
	}
	return out, s.sequence, nil
}
func (s *MemoryStore) SaveView(_ context.Context, view *View) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.views[view.ID] = clone(*view)
	return nil
}
func (s *MemoryStore) GetView(_ context.Context, agent domain.AgentIdentity, id string) (*View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[agent.AgentID]
	if err := checkAgent(&state, agent); err != nil {
		return nil, err
	}
	view, ok := s.views[id]
	if !ok || view.Agent != agent {
		return nil, ErrCursorExpired
	}
	out := clone(view)
	return &out, nil
}
