// Package agentscope binds every call the AI agent makes to one workspace.
//
// A run's context carries the workspace four ways, one for each library the
// agent uses: cortex's scope and principal, nexus's tenant, shield's tenant
// and app, and weave's tenant. Binder sets them all at once from one Scope,
// so no library can see one organization while another sees a different
// one. The cortex adapters take the shield and weave tenant from the scope's
// first level, which is the organization's ID, and nexus takes the tenant
// Bind puts on the context, the workspace's own nexus tenant.
package agentscope

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/xraph/cortex"
	"github.com/xraph/forge"
	"github.com/xraph/nexus/pipeline"
	"github.com/xraph/shield"
	"github.com/xraph/weave"

	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/security"
)

// App is the shield app every workspace's safety profiles live under.
const App = "loomplane"

// The cortex scope levels, in the order a scope carries them. The order
// decides the column each level is stored in, so it never changes.
const (
	LevelOrg         = "org"
	LevelProject     = "project"
	LevelEnvironment = "environment"
)

// ErrInvalid is a scope that names no organization, an environment without
// its project, a value cortex can't store, or a cortex scope that isn't one
// of Loomplane's.
var ErrInvalid = errors.New("agentscope: invalid scope")

// Scope is where an agent run acts: always an organization, maybe one of
// its projects, maybe one environment of that project.
type Scope struct {
	OrgID         string `json:"org_id"`
	ProjectID     string `json:"project_id,omitempty"`
	EnvironmentID string `json:"environment_id,omitempty"`
}

// Validate checks that s names an organization, that an environment comes
// with its project, and that cortex can store every value.
func (s Scope) Validate() error {
	if s.OrgID == "" {
		return fmt.Errorf("%w: it names no organization", ErrInvalid)
	}
	if s.EnvironmentID != "" && s.ProjectID == "" {
		return fmt.Errorf("%w: an environment needs its project", ErrInvalid)
	}
	if err := cortex.ValidateScope(s.Cortex()); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}

// Cortex is s as cortex's scope: org, then project and environment when s
// has them.
func (s Scope) Cortex() cortex.Scope {
	levels := []cortex.Level{{Key: LevelOrg, Value: s.OrgID}}
	if s.ProjectID != "" {
		levels = append(levels, cortex.Level{Key: LevelProject, Value: s.ProjectID})
		if s.EnvironmentID != "" {
			levels = append(levels, cortex.Level{Key: LevelEnvironment, Value: s.EnvironmentID})
		}
	}
	return cortex.Scope{Levels: levels}
}

// FromCortex reads a cortex scope Cortex made back into a Scope. Any other
// shape is ErrInvalid.
func FromCortex(cs cortex.Scope) (Scope, error) {
	if err := cortex.ValidateScope(cs); err != nil {
		return Scope{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	keys := []string{LevelOrg, LevelProject, LevelEnvironment}
	values := make([]string, len(keys))
	for i, l := range cs.Levels {
		if l.Key != keys[i] {
			return Scope{}, fmt.Errorf("%w: level %d is %q, not %q", ErrInvalid, i+1, l.Key, keys[i])
		}
		values[i] = l.Value
	}
	s := Scope{OrgID: values[0], ProjectID: values[1], EnvironmentID: values[2]}
	return s, s.Validate()
}

// Tenants names a workspace's nexus tenant. *agentsettings.Service is the
// real one; it answers agentsettings.ErrDisabled while the agent is off.
type Tenants interface {
	NexusTenant(ctx context.Context, orgID string) (string, error)
}

// Binder puts a Scope on a context for every library at once.
type Binder struct {
	Tenants Tenants
}

type scopeKey struct{}

// Bind returns ctx carrying s and p for every library the agent uses:
// cortex's scope and principal, nexus's tenant (the workspace's own),
// shield's tenant (the organization) and app (App), and weave's tenant
// (the organization). The principal must belong to s's organization: a
// person of another organization, the agent of another organization, or
// the agent acting for a person of another one is security.ErrForbidden.
//
// On any error Bind returns ctx as it was, never a context bound in part.
func (b *Binder) Bind(ctx context.Context, p security.Principal, s Scope) (context.Context, error) {
	if err := s.Validate(); err != nil {
		return ctx, err
	}
	if err := sameOrganization(p, s.OrgID); err != nil {
		return ctx, err
	}
	if b == nil || b.Tenants == nil {
		return ctx, errors.New("agentscope: no tenant lookup is configured")
	}
	tenant, err := b.Tenants.NexusTenant(ctx, s.OrgID)
	if err != nil {
		return ctx, fmt.Errorf("look up the workspace's model tenant: %w", err)
	}
	if tenant == "" {
		return ctx, errors.New("agentscope: the workspace has no model tenant")
	}
	bound := cortex.WithScope(ctx, s.Cortex())
	bound = cortex.WithPrincipal(bound, p)
	bound = pipeline.WithTenantID(bound, tenant)
	bound = shield.WithApp(shield.WithTenant(bound, s.OrgID), App)
	bound = weave.WithTenant(bound, s.OrgID)
	return context.WithValue(bound, scopeKey{}, s), nil
}

// sameOrganization refuses a principal that doesn't belong to orgID.
func sameOrganization(p security.Principal, orgID string) error {
	forbidden := fmt.Errorf("%w: the agent can only act in your own organization", security.ErrForbidden)
	if p.OrgID != orgID {
		return forbidden
	}
	if p.IsAgent() && p.UserID != security.AgentSubject(orgID) {
		return forbidden
	}
	if person, ok := p.Person(); ok && person.OrgID != orgID {
		return forbidden
	}
	return nil
}

// From returns the Scope Bind put on ctx.
func From(ctx context.Context) (Scope, bool) {
	s, ok := ctx.Value(scopeKey{}).(Scope)
	return s, ok
}

// Middleware binds each request of the agent's routes to its caller's
// organization, after the identity guard has put the caller on the
// request. No caller is a 401; the agent being off is a 409 with
// agentsettings' message; another failure is a 500 that names no detail.
func (b *Binder) Middleware() forge.Middleware {
	return func(next forge.Handler) forge.Handler {
		return func(c forge.Context) error {
			ctx := c.Request().Context()
			p, ok := security.PrincipalFrom(ctx)
			if !ok {
				return forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
			}
			bound, err := b.Bind(ctx, p, Scope{OrgID: p.OrgID})
			switch {
			case err == nil:
			case errors.Is(err, security.ErrForbidden):
				return forge.NewHTTPError(http.StatusForbidden, security.ErrForbidden.Error())
			case errors.Is(err, agentsettings.ErrDisabled):
				return forge.NewHTTPError(http.StatusConflict, agentsettings.ErrDisabled.Error())
			case errors.Is(err, ErrInvalid):
				return forge.NewHTTPError(http.StatusUnauthorized, security.ErrNoOrganization.Error())
			default:
				return forge.NewHTTPError(http.StatusInternalServerError, "the agent couldn't reach your workspace's settings; try again")
			}
			c.WithContext(bound)
			return next(c)
		}
	}
}
