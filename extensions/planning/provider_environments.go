package planning

import (
	"context"
	"net/http"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/publicplan"
	"github.com/xraph/loomplane/internal/security"
)

// ProviderEnvironment says whether one environment deploys to a provider's
// own environment or fell back to a project of its own. It names no
// provider project or environment.
type ProviderEnvironment struct {
	EnvironmentID string    `json:"environmentId"`
	Provider      string    `json:"provider"`
	Scope         string    `json:"scope"`
	Mode          string    `json:"mode"`
	Permanent     bool      `json:"permanent"`
	Reason        string    `json:"reason"`
	CreatedAt     time.Time `json:"createdAt"`
}

// ProviderEnvironmentList is a project's provider environment records.
type ProviderEnvironmentList struct {
	ProviderEnvironments []ProviderEnvironment `json:"providerEnvironments"`
}

// errNothingToRetry answers "try again" for an environment whose only
// fallbacks the provider can never lift.
const errNothingToRetry = "This environment can't use the provider's own environments, so there's nothing to try again."

type providerEnvironmentStore interface {
	ProviderEnvironments(ctx context.Context, orgID, projectID string) ([]*deployments.ProviderEnvironment, error)
	ResetFallbacks(ctx context.Context, orgID, environmentID string) (reset, permanent int, err error)
}

type projectEnvironments interface {
	GetEnvironment(ctx context.Context, orgID, projectID, id string) (*projects.Environment, error)
}

type requirer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

type planCreator interface {
	Create(ctx context.Context, p security.Principal, projectID string, in planning.CreatePlanInput) (*planning.Plan, error)
}

// providerEnvironmentDeps are what the provider environment routes need.
type providerEnvironmentDeps struct {
	Store        providerEnvironmentStore
	Environments projectEnvironments
	Authz        requirer
	Plans        planCreator
}

// providerEnvironmentRoutes registers the routes that list which
// environments fell back from a provider's own environments, and try one
// again. Trying again plans the move back, which a person approves.
func providerEnvironmentRoutes(r forge.Router, guard identity.Guard, d providerEnvironmentDeps) error {
	g := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Projects"))
	list := func(c forge.Context) error {
		p, err := principal(c)
		if err != nil {
			return err
		}
		ctx, projectID := c.Request().Context(), c.Param("id")
		if err = d.Authz.Require(ctx, p, security.EnvironmentRead, projectID); err != nil {
			return fail(err)
		}
		rows, err := d.Store.ProviderEnvironments(ctx, p.OrgID, projectID)
		if err != nil {
			return fail(err)
		}
		out := ProviderEnvironmentList{ProviderEnvironments: make([]ProviderEnvironment, 0, len(rows))}
		for _, pe := range rows {
			out.ProviderEnvironments = append(out.ProviderEnvironments, ProviderEnvironment{
				EnvironmentID: pe.EnvironmentID, Provider: pe.Provider, Scope: pe.Scope, Mode: pe.Mode,
				Permanent: pe.Permanent, Reason: pe.Reason, CreatedAt: pe.CreatedAt,
			})
		}
		return c.JSON(http.StatusOK, out)
	}
	if err := g.GET("/projects/:id/provider-environments", list,
		forge.WithName("providerEnvironments.list"),
		forge.WithSummary("Which of the project's environments deploy to a provider's own environments, and which fell back to a project of their own, with why"),
		forge.WithResponseSchema(http.StatusOK, "Provider environments", ProviderEnvironmentList{}),
	); err != nil {
		return err
	}

	retry := func(c forge.Context) error {
		p, err := principal(c)
		if err != nil {
			return err
		}
		ctx, projectID := c.Request().Context(), c.Param("id")
		if err = d.Authz.Require(ctx, p, security.DeploymentCreate, projectID); err != nil {
			return fail(err)
		}
		env, err := d.Environments.GetEnvironment(ctx, p.OrgID, projectID, c.Param("environmentId"))
		if err != nil {
			return fail(err)
		}
		reset, permanent, err := d.Store.ResetFallbacks(ctx, p.OrgID, env.ID)
		if err != nil {
			return fail(err)
		}
		switch {
		case reset == 0 && permanent > 0:
			return forge.NewHTTPError(http.StatusConflict, errNothingToRetry)
		case reset == 0:
			return forge.NewHTTPError(http.StatusNotFound, env.Name+" deploys to the provider's own environments already, so there's nothing to try again.")
		}
		plan, err := d.Plans.Create(ctx, p, projectID, planning.CreatePlanInput{Environment: env.Name})
		if err != nil {
			return fail(err)
		}
		return c.JSON(http.StatusOK, publicplan.Project(plan))
	}
	return g.POST("/projects/:id/environments/:environmentId/provider-environments/retry", retry,
		forge.WithName("providerEnvironments.retry"),
		forge.WithSummary("Try the provider's own environment again for an environment that fell back: clears the fallbacks the provider might lift and plans the move back, which a person approves"),
		forge.WithResponseSchema(http.StatusOK, "The plan", publicplan.Plan{}),
	)
}
