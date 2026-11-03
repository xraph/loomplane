package providerresources

import (
	"context"
	"strings"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
)

// projectBinding requires actual persisted use in the selected project before
// provider credentials or inventory records are read on that project's behalf.
func (s *Service) projectBinding(ctx context.Context, p security.Principal, projectID, environmentID, id string, revision int64) (*ResourceBinding, error) {
	if p.OrgID == "" || projectID == "" || environmentID == "" || s.deps.Authorizer == nil || s.deps.Usage == nil {
		return nil, security.ErrForbidden
	}
	for _, perm := range []security.Permission{security.ProjectRead, security.DeploymentCreate} {
		if err := s.deps.Authorizer.Require(ctx, p, perm, projectID); err != nil {
			return nil, err
		}
	}
	// On the project, like every environment check: an environment ID
	// isn't a project namespace, so it checked at the root.
	if err := s.deps.Authorizer.Require(ctx, p, security.EnvironmentRead, projectID); err != nil {
		return nil, err
	}
	bindings, err := s.deps.Usage(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	for _, b := range bindings {
		if b.ProjectID == projectID && b.EnvironmentID == environmentID && b.InventoryResourceID == id {
			if b.Revision != revision || revision <= 0 || b.EndpointID == "" || b.EndpointRevision <= 0 {
				return nil, ErrChanged
			}
			return &b, nil
		}
	}
	return nil, security.ErrForbidden
}
func projectPinMatches(b *ResourceBinding, r *ProviderResource) bool {
	externalIDMatches := b.ExternalID == r.ExternalID
	if r.Provider == connections.Azure {
		externalIDMatches = strings.EqualFold(b.ExternalID, r.ExternalID)
	}
	return b.Provider == r.Provider && b.AccountIdentity != "" && b.AccountIdentity == r.AccountIdentity && externalIDMatches && AttachableIdentity(r) && b.Incarnation == r.Incarnation && b.Revision == r.Revision && b.EndpointID == r.EndpointID && b.EndpointRevision == r.EndpointRevision
}

// VerifyProject verifies an already approved external binding under its
// project's deployment grant. It cannot authorize a first attachment or reveal
// another project's resource list, consumers or access material.
func (s *Service) VerifyProject(ctx context.Context, p security.Principal, projectID, environmentID, id string, revision int64) (*ProviderResource, *catalogconnect.Endpoint, error) {
	binding, err := s.projectBinding(ctx, p, projectID, environmentID, id, revision)
	if err != nil {
		return nil, nil, err
	}
	r, err := s.deps.Store.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, nil, err
	}
	if !projectPinMatches(binding, r) {
		return nil, nil, ErrChanged
	}
	run, err := s.sync(ctx, p, r.ConnectionID, false)
	if err != nil {
		return nil, nil, err
	}
	r, err = s.deps.Store.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, nil, err
	}
	if !projectPinMatches(binding, r) {
		return nil, nil, ErrChanged
	}
	s.decorate(r)
	if proofErr := s.verifySelectedObservation(ctx, r, run); proofErr != nil {
		return nil, nil, proofErr
	}
	if r.EndpointProofRequired {
		r, err = s.verifyEndpointAccess(ctx, p, r, r.Host, r.Port, false)
		if err != nil {
			return nil, nil, err
		}
	}
	if s.deps.Endpoints == nil {
		return nil, nil, ErrUnverified
	}
	e, err := s.deps.Endpoints.Get(ctx, p.OrgID, r.EndpointID)
	if err != nil {
		return nil, nil, ErrUnverified
	}
	if !endpointMatches(r, e) || e.Revision != r.EndpointRevision {
		return nil, nil, ErrChanged
	}
	// Only the caller's approved project relation is returned here.
	r.Consumers = []ProviderResourceConsumer{{ID: binding.ID, BindingID: binding.ID, ProjectID: binding.ProjectID, EnvironmentID: binding.EnvironmentID, Name: binding.Name}}
	return r, e, nil
}

// ResolveProject reads the access material of an existing approved project
// binding. The caller still verifies the actual deployment target and network.
func (s *Service) ResolveProject(ctx context.Context, p security.Principal, projectID, environmentID, id string, revision int64) (*ProviderResource, *catalogconnect.Endpoint, string, error) {
	r, _, err := s.VerifyProject(ctx, p, projectID, environmentID, id, revision)
	if err != nil {
		return nil, nil, "", err
	}
	binding, err := s.projectBinding(ctx, p, projectID, environmentID, id, revision)
	if err != nil {
		return nil, nil, "", err
	}
	if !projectPinMatches(binding, r) {
		return nil, nil, "", ErrChanged
	}
	if pinErr := s.preflightEndpoint(ctx, r); pinErr != nil {
		return nil, nil, "", pinErr
	}
	e, dsn, err := s.deps.Endpoints.Resolve(ctx, p.OrgID, r.EndpointID)
	if err != nil {
		return nil, nil, "", ErrUnverified
	}
	if !endpointMatches(r, e) || e.Revision != r.EndpointRevision || !matchesDSN(r, dsn) {
		return nil, nil, "", ErrChanged
	}
	current, err := s.deps.Store.Get(ctx, p.OrgID, id)
	if err != nil || !projectPinMatches(binding, current) {
		return nil, nil, "", ErrChanged
	}
	finalBinding, err := s.projectBinding(ctx, p, projectID, environmentID, id, revision)
	if err != nil {
		return nil, nil, "", err
	}
	latest, proofErr := s.latestSelectedObservation(ctx, current)
	if proofErr != nil {
		return nil, nil, "", proofErr
	}
	if !projectPinMatches(finalBinding, latest) || !endpointMatches(latest, e) || e.Revision != latest.EndpointRevision || !matchesDSN(latest, dsn) {
		return nil, nil, "", ErrChanged
	}
	s.decorate(latest)
	latest.Consumers = []ProviderResourceConsumer{{ID: finalBinding.ID, BindingID: finalBinding.ID, ProjectID: finalBinding.ProjectID, EnvironmentID: finalBinding.EnvironmentID, Name: finalBinding.Name}}
	return latest, e, dsn, nil
}
