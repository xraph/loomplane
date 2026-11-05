package cloudinfra

import (
	"context"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/security"
)

// authorizeCapacityPrincipal checks saved manager credentials as well as current grants.
func (s *Service) authorizeCapacityPrincipal(ctx context.Context, p security.Principal, cloud string) (*cloudprofiles.Profile, error) {
	if p.KeyID != "" {
		if s.CapacityPrincipal == nil {
			return nil, security.ErrForbidden
		}
		if err := s.CapacityPrincipal.ValidatePrincipal(ctx, p); err != nil {
			return nil, err
		}
	}
	return s.authorize(ctx, p, cloud)
}

// authorizeCapacityChange is authorizeCapacityPrincipal for a path that
// changes the Cloud's capacity, which the platform-owned Cloud never takes.
func (s *Service) authorizeCapacityChange(ctx context.Context, p security.Principal, cloud string) (*cloudprofiles.Profile, error) {
	profile, err := s.authorizeCapacityPrincipal(ctx, p, cloud)
	if err != nil {
		return nil, err
	}
	if profile.PlatformOwned {
		return nil, cloudprofiles.ErrPlatformOwned
	}
	return profile, nil
}
