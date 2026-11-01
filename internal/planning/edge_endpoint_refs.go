package planning

import (
	"context"
	"strings"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
)

// EndpointPin is the non-secret endpoint metadata reviewed by a resource plan.
type EndpointPin struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	Engine   string `json:"engine"`
	Host     string `json:"host"`
	Database string `json:"database,omitempty"`
}

// resourceEndpointPins reads metadata only. Missing pins become safe planner findings.
func (s *Service) resourceEndpointPins(ctx context.Context, plan *Plan, m *manifest.Manifest) map[string]EndpointPin {
	pins := map[string]EndpointPin{}
	if plan == nil || m == nil || s.authz == nil || s.endpoints == nil {
		return pins
	}
	actor := plan.DecidedBy
	if actor == "" {
		actor = plan.CreatedBy
	}
	if actor == "" {
		return pins
	}
	refs := map[string]bool{}
	for _, resource := range m.Resources {
		selected := resource.For(plan.Environment, m.Defaults)
		if selected.Enabled != nil && !*selected.Enabled {
			continue
		}
		if selected.Provider != "cloudflare-workers" && !strings.HasPrefix(resource.Kind, "cloudflare-") {
			continue
		}
		for _, setting := range []string{"origin_endpoint", "application_endpoint"} {
			id := selected.Settings[setting]
			if id == "" {
				continue
			}
			strict := resource.Kind == "cloudflare-hyperdrive" && setting == "origin_endpoint"
			refs[id] = refs[id] || strict
		}
	}
	if len(refs) == 0 {
		return pins
	}
	principal := security.ActorPrincipal(plan.OrgID, actor)
	if verifier, ok := s.authz.(interface {
		RequireCurrentMembership(context.Context, security.Principal) error
	}); ok {
		if verifier.RequireCurrentMembership(ctx, principal) != nil {
			return pins
		}
	}
	if s.authz.Require(ctx, principal, security.SecretManage, "") != nil {
		return pins
	}
	for id, strictOrigin := range refs {
		endpoint, err := s.endpoints.Endpoint(ctx, plan.OrgID, id)
		if err != nil || endpoint == nil || endpoint.ID != id || endpoint.OrgID != plan.OrgID || endpoint.Revision <= 0 || endpoint.Host == "" || endpoint.Engine == "" {
			continue
		}
		if strictOrigin && ((endpoint.Engine != catalogconnect.EnginePostgres && endpoint.Engine != catalogconnect.EngineMySQL) || endpoint.TLS != catalogconnect.TLSVerifyFull) {
			continue
		}
		pins[id] = EndpointPin{ID: id, Revision: endpoint.Revision, Engine: endpoint.Engine, Host: endpoint.Host, Database: endpoint.Database}
	}
	return pins
}
