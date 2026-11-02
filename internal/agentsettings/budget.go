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
	limit, err := s.allocation(ctx, orgID)
	if err != nil {
		return BudgetState{}, err
	}
	st, err := s.store.Get(ctx, orgID)
	if err != nil {
		return BudgetState{}, err
	}
	spent := "0.00"
	if st.NexusTenantID != "" {
		gw, _, _, _ := s.bound()
		if gw == nil {
			return BudgetState{}, errors.New("agentsettings: no nexus gateway to read the agent's spend from")
		}
		if spent, err = gw.MonthlySpend(ctx, st.NexusTenantID); err != nil {
			return BudgetState{}, fmt.Errorf("read the agent's spend: %w", err)
		}
	}
	l, ok := new(big.Rat).SetString(limit)
	if !ok {
		return BudgetState{}, fmt.Errorf("agentsettings: the agent's allocation %q isn't an amount", limit)
	}
	sp, ok := new(big.Rat).SetString(spent)
	if !ok {
		return BudgetState{}, fmt.Errorf("agentsettings: nexus reported a spend that isn't an amount (%q)", spent)
	}
	return BudgetState{LimitUSD: limit, SpentUSD: spent, Exhausted: sp.Cmp(l) >= 0}, nil
}

// RequireBudget refuses with ErrBudgetExhausted once the agent has spent
// its month's budget.
func (s *Service) RequireBudget(ctx context.Context, orgID string) error {
	b, err := s.Budget(ctx, orgID)
	if err != nil {
		return err
	}
	if b.Exhausted {
		return ErrBudgetExhausted
	}
	return nil
}
