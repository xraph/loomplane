package cloudinfra

import "context"

// CapacityCloudWakeup is a post-commit hint for current waits in one Cloud.
// A failed hint leaves policy and inventory commits intact; durable timers retry.
type CapacityCloudWakeup interface {
	WakeCapacityCloud(context.Context, string, string, string) error
}

func (s *Service) wakeCapacityCloud(ctx context.Context, org, cloud, change string) {
	if wake, ok := s.CapacityDemandWakeup.(CapacityCloudWakeup); ok {
		if err := wake.WakeCapacityCloud(ctx, org, cloud, change); err != nil {
			return
		}
	}
}

func (s *Service) WakeCapacityCloud(ctx context.Context, org, cloud, change string) error {
	s.wakeCapacityCloud(ctx, org, cloud, change)
	return nil
}
