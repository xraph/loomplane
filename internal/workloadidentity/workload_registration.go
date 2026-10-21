package workloadidentity

import (
	"context"
	"time"

	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// WorkloadBinding associates checked workload evidence with one application
// service. The controller must resolve the service from owned deployment records,
// retain revocation intent before issuance, and recheck current destination state.
// Never construct this value directly from a management request. Binding.Agent
// must be empty; application registrations do not grant observer authority.
type WorkloadBinding struct {
	Binding    Binding
	Workload   domain.WorkloadSelector
	PruneAfter time.Time
}

// RegisterWorkload requests short-lived certificates and sets SPIRE housekeeping
// metadata. PruneAfter is not an issuance or authorization deadline: SPIRE may
// serve identities beyond it. Controllers must explicitly revoke registrations,
// and access policy must independently limit and cancel active requests.
func (i *SPIREIssuer) RegisterWorkload(ctx context.Context, b WorkloadBinding) (*spiretypes.Entry, error) {
	entry, err := i.workloadEntry(b)
	if err != nil {
		return nil, err
	}
	return i.registerEntry(ctx, entry)
}

func (i *SPIREIssuer) workloadEntry(b WorkloadBinding) (*spiretypes.Entry, error) {
	binding := b.Binding
	now := i.now()
	if binding.Agent != (domain.AgentIdentity{}) || b.Workload.Scope != binding.Destination.Scope || b.Workload.DestinationID != binding.Destination.ID || b.PruneAfter.IsZero() || b.PruneAfter.Nanosecond() != 0 || !b.PruneAfter.After(now) || b.PruneAfter.After(now.Add(5*time.Minute)) || b.PruneAfter.After(binding.CheckedAt.Add(5*time.Minute)) || binding.TTL > 5*time.Minute {
		return nil, ErrIdentity
	}
	uri, err := WorkloadURI(i.domain, b.Workload)
	if err != nil {
		return nil, err
	}
	entry, err := i.attestedEntry(binding, uri)
	if err != nil {
		return nil, err
	}
	entry.ExpiresAt = b.PruneAfter.Unix()
	return entry, nil
}

func (a *Admin) RegisterWorkload(ctx context.Context, binding WorkloadBinding) (*spiretypes.Entry, error) {
	if binding.Workload.OrgID != a.org || binding.Binding.Destination.OrgID != a.org {
		return nil, ErrIdentity
	}
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	if err := a.Health(ctx); err != nil {
		return nil, err
	}
	return a.issuer.RegisterWorkload(ctx, binding)
}

func (r *CleanupRuntime) RegisterWorkload(ctx context.Context, binding WorkloadBinding) (*spiretypes.Entry, error) {
	admin, err := r.authority()
	if err != nil {
		return nil, err
	}
	return admin.RegisterWorkload(ctx, binding)
}
