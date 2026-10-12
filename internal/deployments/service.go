package deployments

import (
	"context"

	"github.com/xraph/loomplane/internal/security"
)

const listLimit = 30

// Authorizer decides whether a principal may act.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// Service is the deployment API: reading what deployments did.
type Service struct {
	store Store
	authz Authorizer
}

// NewService returns a Service.
func NewService(store Store, authz Authorizer) *Service {
	return &Service{store: store, authz: authz}
}

// List returns a project's latest deployments, newest first.
func (s *Service) List(ctx context.Context, p security.Principal, projectID string) ([]*Deployment, error) {
	if err := s.authz.Require(ctx, p, security.DeploymentRead, projectID); err != nil {
		return nil, err
	}
	return s.store.ListDeployments(ctx, p.OrgID, projectID, listLimit)
}

// Get returns one deployment.
func (s *Service) Get(ctx context.Context, p security.Principal, id string) (*Deployment, error) {
	if err := s.authz.Require(ctx, p, security.DeploymentRead, ""); err != nil {
		return nil, err
	}
	return s.store.GetDeployment(ctx, p.OrgID, id)
}

// ForPlan returns the deployment an approved plan started.
func (s *Service) ForPlan(ctx context.Context, p security.Principal, planID string) (*Deployment, error) {
	if err := s.authz.Require(ctx, p, security.DeploymentRead, ""); err != nil {
		return nil, err
	}
	return s.store.ForPlan(ctx, p.OrgID, planID)
}
