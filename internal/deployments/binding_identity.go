package deployments

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// bindingIdentity includes the provider's account scope. Resource names alone
// can repeat in another account or region.
func bindingIdentity(b *planning.Binding) string {
	parts := []string{b.Provider, b.Region, b.ConnectionID, b.ExternalID}
	if b.PhysicalID != "" {
		parts = append(parts, b.PhysicalID)
	}
	v, _ := json.Marshal(parts) //nolint:errcheck // A slice of strings always marshals.
	return string(v)
}

// Provider outcomes identify retained resources independently of requested region.
// A region change that returns the original identity is rejected before binding.
func (r *Runner) sameBindingResource(ctx context.Context, org string, a, b *planning.Binding) (bool, error) {
	if a == nil || b == nil || a.Provider != b.Provider || a.ExternalID == "" || a.ExternalID != b.ExternalID {
		return false, nil
	}
	if a.PhysicalID != "" && b.PhysicalID != "" && (a.PhysicalID != b.PhysicalID || a.Region != b.Region) {
		return false, nil
	}
	return r.sameBindingAccount(ctx, org, a, b)
}

// sameBindingAccount ignores requested region so aliases cannot disguise an
// executor retaining the original resource during a move to another region.
func (r *Runner) sameBindingAccount(ctx context.Context, org string, a, b *planning.Binding) (bool, error) {
	if a.Provider != b.Provider {
		return false, nil
	}
	if a.ConnectionID == b.ConnectionID {
		return true, nil
	}
	if a.ConnectionID == "" || b.ConnectionID == "" {
		return false, errors.New("resource identity is missing its original connection; verify the serving resource before cleanup")
	}
	left, _, err := r.credentials.Credential(ctx, org, a.ConnectionID)
	if err != nil {
		return false, err
	}
	right, _, err := r.credentials.Credential(ctx, org, b.ConnectionID)
	if err != nil {
		return false, err
	}
	if left == nil || right == nil {
		return false, errors.New("resource account identity is unavailable")
	}
	original := connections.AccountIdentity(left.Cloud, left.Account, left.Details)
	current := connections.AccountIdentity(right.Cloud, right.Account, right.Details)
	if original == "" || current == "" {
		return false, errors.New("resource account identity is unverified; reconnect the account before cleanup")
	}
	return original == current, nil
}
