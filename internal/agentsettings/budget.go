package agentsettings

import (
	"context"
	"errors"
	"fmt"
	"math/big"
)

// allocation is the agent's monthly budget: its allocation of the
// workspace budget, "0.00" when it has none.
func (s *Service) allocation(ctx context.Context, orgID string) (string, error) {
	a, err := s.ai.Allocation(ctx, orgID, AllocationScope)
	if errors.Is(err, ErrNotFound) {
		return "0.00", nil
	}
	if err != nil {
		return "", err
	}
	return a.MonthlyBudgetUSD, nil
}

// Budget is the agent's month so far: its allocation, and what its nexus
// tenant has spent. An agent with no allocation has nothing to spend.
func (s *Service) Budget(ctx context.Context, orgID string) (BudgetState, error) {
	b, _, err := s.budget(ctx, orgID)
	return b, err
}

// budget is Budget, and the settings it read the spend through.
func (s *Service) budget(ctx context.Context, orgID string) (BudgetState, *Settings, error) {
	limit, err := s.allocation(ctx, orgID)
	if err != nil {
		return BudgetState{}, nil, err
	}
	st, err := s.store.Get(ctx, orgID)
	if err != nil {
		return BudgetState{}, nil, err
	}
	spent := "0.00"
	if st.NexusTenantID != "" {
		gw, _, _, _ := s.bound()
		if gw == nil {
			return BudgetState{}, nil, errors.New("agentsettings: no nexus gateway to read the agent's spend from")
		}
		if spent, err = gw.MonthlySpend(ctx, st.NexusTenantID); err != nil {
			return BudgetState{}, nil, fmt.Errorf("read the agent's spend: %w", err)
		}
	}
	l, ok := new(big.Rat).SetString(limit)
	if !ok {
		return BudgetState{}, nil, fmt.Errorf("agentsettings: the agent's allocation %q isn't an amount", limit)
	}
	sp, ok := new(big.Rat).SetString(spent)
	if !ok {
		return BudgetState{}, nil, fmt.Errorf("agentsettings: nexus reported a spend that isn't an amount (%q)", spent)
	}
	return BudgetState{LimitUSD: limit, SpentUSD: spent, Exhausted: sp.Cmp(l) >= 0}, st, nil
}

// CheckBudget refuses with ErrBudgetExhausted once the agent has spent its
// month's budget, and never consults the metering gate. Knowledge's
// embeddings use it: they run on the server's embedding key, which stays
// free.
func (s *Service) CheckBudget(ctx context.Context, orgID string) error {
	b, _, err := s.budget(ctx, orgID)
	if err != nil {
		return err
	}
	if b.Exhausted {
		return ErrBudgetExhausted
	}
	return nil
}

// RequireBudget refuses with ErrBudgetExhausted once the agent has spent
// its month's budget, and, for a workspace on the server's key, with
// metering.ErrBlocked while the edition's gate won't let the platform's
// key be used. A workspace on its own key is never gated.
func (s *Service) RequireBudget(ctx context.Context, orgID string) error {
	b, st, err := s.budget(ctx, orgID)
	if err != nil {
		return err
	}
	if b.Exhausted {
		return ErrBudgetExhausted
	}
	if st.ProviderKey == "" {
		if err := s.canUseServerKey(ctx, orgID); err != nil {
			return err
		}
	}
	return nil
}
