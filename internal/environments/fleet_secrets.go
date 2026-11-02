package environments

import (
	"context"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/pkg/fleet"
)

// PinnedSecretScope is constructed from a verified instance snapshot inside the
// deployment workflow. It is never bound directly to an API request.
type PinnedSecretScope struct {
	OrgID, ProjectID, EnvironmentID, InstanceID, Service string
}

// ResolvePinnedSecret reads an explicit version after checking the destination.
// Only secrets owned by this instance environment qualify. Shared credentials
// require a separate attachment grant; knowing a project secret ID is insufficient.
func (s *Service) ResolvePinnedSecret(ctx context.Context, scope PinnedSecretScope, ref fleet.SecretRef) ([]byte, error) {
	if scope.OrgID == "" || scope.ProjectID == "" || scope.EnvironmentID == "" || scope.InstanceID == "" || scope.Service == "" || ref.ID == "" || ref.Version < 1 {
		return nil, ErrNotFound
	}
	reader, ok := s.projects.(interface {
		GetEnvironment(context.Context, string, string, string) (*projects.Environment, error)
	})
	if !ok {
		return nil, ErrNotFound
	}
	env, err := reader.GetEnvironment(ctx, scope.OrgID, scope.ProjectID, scope.EnvironmentID)
	if err != nil {
		return nil, err
	}
	if env.InstanceID != scope.InstanceID {
		return nil, ErrNotFound
	}
	v, err := s.store.GetVariable(ctx, scope.OrgID, ref.ID)
	if err != nil {
		return nil, err
	}
	if !v.Secret || v.OrgID != scope.OrgID || v.ProjectID != scope.ProjectID || (v.Scope != ScopeProject && v.Scope != ScopeService) {
		return nil, ErrNotFound
	}
	if v.environmentID != scope.EnvironmentID || v.Environment != env.Name {
		return nil, ErrNotFound
	}
	if v.Service != "" && v.Service != "svc:artifact/"+scope.Service {
		return nil, ErrNotFound
	}
	versions, ok := s.secrets.(interface {
		RevealVersion(context.Context, string, string, int64) ([]byte, error)
	})
	if !ok {
		return nil, ErrNotFound
	}
	return versions.RevealVersion(ctx, scope.OrgID, v.ID, ref.Version)
}
