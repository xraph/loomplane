package cloudinfra

import (
	"context"

	"github.com/xraph/loomplane/internal/security"
)

// capacityUsageStore reads a Cloud's counted capacity and frontend slots.
type capacityUsageStore interface {
	GetCapacityUsage(context.Context, string, string) (*CapacityUsage, error)
	FrontendUsage(context.Context, string, string) ([]FrontendUsage, error)
}

var _ capacityUsageStore = (*PgStore)(nil)

// GetCapacityUsage reads what a Cloud counts against its ceilings without
// asking any provider. RefreshCapacity is the call that reads providers.
func (s *Service) GetCapacityUsage(ctx context.Context, p security.Principal, cloudID string) (*CapacityUsage, error) {
	if _, err := s.authorize(ctx, p, cloudID); err != nil {
		return nil, err
	}
	st, ok := s.Store.(capacityUsageStore)
	if !ok {
		return nil, ErrInvalid
	}
	usage, err := st.GetCapacityUsage(ctx, p.OrgID, cloudID)
	if err != nil {
		return nil, err
	}
	return withFrontends(ctx, st, p.OrgID, cloudID, usage)
}

// withFrontends adds frontend slots and the Cloud's ID, and gives clients
// lists, never null.
func withFrontends(ctx context.Context, st capacityUsageStore, org, cloud string, usage *CapacityUsage) (*CapacityUsage, error) {
	frontends, err := st.FrontendUsage(ctx, org, cloud)
	if err != nil {
		return nil, err
	}
	usage.CloudID = cloud
	usage.Frontends = frontends
	return withUsageLists(usage), nil
}

func withUsageLists(usage *CapacityUsage) *CapacityUsage {
	if usage.Accounts == nil {
		usage.Accounts = []CapacityAccountSnapshot{}
	}
	if usage.Records == nil {
		usage.Records = []CapacityRecord{}
	}
	if usage.GPUs == nil {
		usage.GPUs = []GPUUsage{}
	}
	if usage.Frontends == nil {
		usage.Frontends = []FrontendUsage{}
	}
	return usage
}
