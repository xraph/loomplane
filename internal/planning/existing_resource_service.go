package planning

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
)

// ExistingResourceResolver checks provider identity and resolves only an
// explicitly authorized immutable resource/credential revision.
type ExistingResourceResolver interface {
	Verified(context.Context, security.Principal, string) (*providerresources.ProviderResource, error)
	Resolve(context.Context, security.Principal, string, int64) (*providerresources.ProviderResource, *catalogconnect.Endpoint, string, error)
}

// ExistingProjectResourceResolver uses an approved binding as a narrowly scoped
// project/environment grant. It never authorizes a first attachment.
type ExistingProjectResourceResolver interface {
	VerifyProject(context.Context, security.Principal, string, string, string, int64) (*providerresources.ProviderResource, *catalogconnect.Endpoint, error)
	ResolveProject(context.Context, security.Principal, string, string, string, int64) (*providerresources.ProviderResource, *catalogconnect.Endpoint, string, error)
}

func (s *Service) checkExistingResources(ctx context.Context, plan *Plan, m *manifest.Manifest, options []ConnectionOption, chosen map[string]string, placed map[string]CloudPlacement, g *graph.Graph, bindings []*Binding) (inventoryChecks map[string]ExistingResourceCheck, networkChecks map[string]EndpointCheck) {
	checks := map[string]ExistingResourceCheck{}
	networks := map[string]EndpointCheck{}
	principal := security.Principal{OrgID: plan.OrgID, UserID: plan.CreatedBy}
	for _, name := range m.ResourceNames() {
		r := m.Resources[name]
		if r.ExistingResource == "" || !resourceOn(s.kinds, r, plan.Environment, m.Defaults) {
			continue
		}
		if _, ok := checks[r.ExistingResource]; ok {
			continue
		}
		check := ExistingResourceCheck{}
		if s.existingResources == nil || s.endpoints == nil || s.authz == nil {
			check.Error = "Provider resource access is unavailable on this Loomplane server."
		} else if err := s.authz.Require(ctx, principal, security.DeploymentCreate, plan.ProjectID); err != nil {
			check.Error = "You no longer have permission to attach resources to this project's deployment."
		} else {
			var record *providerresources.ProviderResource
			var endpoint *catalogconnect.Endpoint
			var err error
			if scoped, ok := s.existingResources.(ExistingProjectResourceResolver); ok {
				for _, b := range bindings {
					if b.Name != name || !ExistingResourceBinding(b) {
						continue
					}
					var old ResourceConfig
					if json.Unmarshal(b.Config, &old) == nil && old.ExistingResource != nil && old.ExistingResource.ResourceID == r.ExistingResource {
						record, endpoint, err = scoped.VerifyProject(ctx, principal, plan.ProjectID, plan.EnvironmentID, r.ExistingResource, old.ExistingResource.Revision)
					}
				}
			}
			if record == nil {
				if permissionErr := s.authz.Require(ctx, principal, security.SecretManage, ""); permissionErr != nil {
					err = permissionErr
				} else {
					record, err = s.existingResources.Verified(ctx, principal, r.ExistingResource)
				}
			}
			if err != nil {
				check.Error = "The provider resource could not be verified. Check your access, sync the account and review again."
			} else {
				check.Resource = record
				check.Endpoint = endpoint
				if check.Endpoint == nil && record.EndpointID != "" {
					check.Endpoint, err = s.endpoints.Endpoint(ctx, plan.OrgID, record.EndpointID)
				}
				if err != nil {
					check.Error = "This resource's saved connection is unavailable. Add its application credentials again."
				}
			}
		}
		checks[r.ExistingResource] = check
	}
	if len(checks) == 0 {
		return checks, networks
	}
	// The pure planner already resolves explicit target, Cloud and environment
	// placement. Reuse that result instead of duplicating account selection.
	preliminary := Compute(Input{ProjectID: plan.ProjectID, Catalog: plan.Catalog, Environment: plan.Environment, Graph: g, Manifest: m, Providers: s.providers, Connections: options, Chosen: chosen, CloudPlacements: placed, Kinds: s.kinds, ExistingResources: checks, Features: s.features})
	for _, action := range preliminary.Actions {
		if action.Resource != nil && action.Resource.Composition != nil {
			s.checkExistingCompositionEndpoints(ctx, plan.OrgID, action, m, checks, networks)
		}
		if action.Service == nil || action.Subject != ServiceSubject {
			continue
		}
		service, ok := m.Services[action.Name]
		if !ok {
			continue
		}
		for _, need := range service.Needs {
			resource, ok := m.Resources[need]
			if !ok || resource.ExistingResource == "" || !resourceOn(s.kinds, resource, plan.Environment, m.Defaults) {
				continue
			}
			check := checks[resource.ExistingResource]
			if check.Resource == nil || check.Endpoint == nil || check.Error != "" {
				continue
			}
			network := EndpointCheck{ID: check.Endpoint.ID, Engine: check.Endpoint.Engine, Found: true}
			connection := action.Service.Target
			if connection == "" {
				connection = action.ConnectionID
			}
			if connection == "" {
				if prov, err := s.providers.Get(action.Provider); err == nil {
					connection = chosen[prov.Cloud]
					if connection == "" {
						matches := slices.DeleteFunc(slices.Clone(options), func(c ConnectionOption) bool { return c.Cloud != prov.Cloud || !c.Ready })
						if len(matches) == 1 {
							connection = matches[0].ID
						}
					}
				}
			}
			if s.dialer == nil || connection == "" {
				network.Err = "Choose a checked deployment target that can verify this external connection."
			} else {
				dial, closer, err := s.dialer.TargetDial(ctx, plan.OrgID, connection)
				if err != nil {
					network.Err = "This deployment target cannot verify the external connection. Use a target with an installed network verifier."
				} else {
					network.Report, err = s.endpoints.Check(ctx, plan.OrgID, check.Endpoint.ID, recipes.Requirement{Engine: check.Endpoint.Engine, TLS: []string{catalogconnect.TLSRequire, catalogconnect.TLSVerifyFull}}, dial)
					if err != nil {
						network.Err = "The external connection could not be checked. Verify the credentials and deployment network."
					}
				}
				if closer != nil {
					if err := closer.Close(); err != nil {
						s.warn("close the external resource verifier", err)
					}
				}
			}
			networks[action.Name+":"+need] = network
		}
	}
	return checks, networks
}

func (s *Service) checkExistingCompositionEndpoints(ctx context.Context, org string, action Action, m *manifest.Manifest, verified map[string]ExistingResourceCheck, checks map[string]EndpointCheck) {
	kinds := s.kinds
	if kinds == nil {
		kinds = recipes.Default()
	}
	kind, ok := kinds.Kind(action.Resource.Kind)
	if !ok {
		return
	}
	resource := m.Resources[action.Name]
	for _, dep := range kind.Dependencies {
		choice := resource.Uses[dep.Name]
		linked, ok := m.Resources[choice.Resource]
		if !ok || linked.ExistingResource == "" || dep.External == nil {
			continue
		}
		check := verified[linked.ExistingResource]
		if check.Resource == nil || check.Endpoint == nil || check.Error != "" {
			continue
		}
		consumer := catalogconnect.ConsumerOf(kind, dep.Name)
		key := endpointKey(check.Endpoint.ID, action.Resource.Target, *dep.External, consumer)
		if _, done := checks[key]; done {
			continue
		}
		observed := EndpointCheck{ID: check.Endpoint.ID, Engine: check.Endpoint.Engine, Found: true}
		if s.dialer == nil || action.Resource.Target == "" {
			observed.Err = "Choose a checked target for this external catalog dependency."
		} else {
			dial, closer, err := s.dialer.TargetDial(ctx, org, action.Resource.Target)
			if err != nil {
				observed.Err = "The catalog deployment target could not verify this external connection."
			} else {
				observed.Report, err = s.endpoints.Check(catalogconnect.WithConsumer(ctx, consumer), org, check.Endpoint.ID, *dep.External, dial)
				if err != nil {
					observed.Err = "The external catalog connection could not be checked."
				}
			}
			if closer != nil {
				if err := closer.Close(); err != nil {
					s.warn("close the external catalog resource verifier", err)
				}
			}
		}
		checks[key] = observed
	}
}
