package planning

import (
	"context"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/security"
)

// RollbackTargetQuery is the query of GET
// /api/v1/projects/:id/environments/:environment/rollback-target.
type RollbackTargetQuery struct {
	// DeploymentID is the deployment to roll back. It must be the
	// environment's newest, which is the default.
	DeploymentID string `query:"deployment_id,omitempty"`
}

// releaseService is what the release routes ask of planning: a plan,
// which may promote what another environment runs, and a rollback.
type releaseService interface {
	Create(ctx context.Context, p security.Principal, projectID string, in planning.CreatePlanInput) (*planning.Plan, error)
	RollbackTarget(ctx context.Context, p security.Principal, projectID, environment, deploymentID string) (*planning.RollbackTarget, error)
	CreateRollback(ctx context.Context, p security.Principal, projectID string, in planning.RollbackInput) (*planning.Plan, error)
}

// releaseRoutes registers the routes that plan a release, a promotion or
// a rollback. None of them deploys anything: a person approves the plan.
func releaseRoutes(r forge.Router, guard identity.Guard, svc releaseService) error {
	g := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Plans"))
	createPlan := func(c forge.Context) error {
		p, err := principal(c)
		if err != nil {
			return err
		}
		var in planning.CreatePlanInput
		if err = c.BindJSON(&in); err != nil {
			return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"environment": "production"}`)
		}
		plan, err := svc.Create(c.Request().Context(), p, c.Param("id"), in)
		if err != nil {
			return fail(err)
		}
		return c.JSON(http.StatusCreated, plan)
	}
	if err := g.POST("/projects/:id/plans", createPlan,
		forge.WithName("plans.create"),
		forge.WithSummary("Plan an environment from the current manifest and one commit of each repository, or what another environment runs, replacing its open plan; a scope moves only some of them"),
		forge.WithRequestSchema(planning.CreatePlanInput{}),
		forge.WithResponseSchema(http.StatusCreated, "The plan", planning.Plan{}),
	); err != nil {
		return err
	}

	rollbackTarget := func(c forge.Context) error {
		p, err := principal(c)
		if err != nil {
			return err
		}
		target, err := svc.RollbackTarget(c.Request().Context(), p, c.Param("id"), c.Param("environment"), c.Request().URL.Query().Get("deployment_id"))
		if err != nil {
			return fail(err)
		}
		return c.JSON(http.StatusOK, target)
	}
	if err := g.GET("/projects/:id/environments/:environment/rollback-target", rollbackTarget,
		forge.WithName("rollbacks.target"),
		forge.WithSummary("What rolling the environment back would undo and restore, and each repository's commits; nothing changes"),
		forge.WithQuerySchema(RollbackTargetQuery{}),
		forge.WithResponseSchema(http.StatusOK, "The rollback target", planning.RollbackTarget{}),
	); err != nil {
		return err
	}

	createRollback := func(c forge.Context) error {
		p, err := principal(c)
		if err != nil {
			return err
		}
		var in planning.RollbackInput
		if err = c.BindJSON(&in); err != nil {
			return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"environment": "production", "reason": "..."}`)
		}
		plan, err := svc.CreateRollback(c.Request().Context(), p, c.Param("id"), in)
		if err != nil {
			return fail(err)
		}
		return c.JSON(http.StatusCreated, plan)
	}
	return g.POST("/projects/:id/rollbacks", createRollback,
		forge.WithName("rollbacks.create"),
		forge.WithSummary("Plan the environment back to its previous successful release, with a reason; a person approves it like any plan. Data-bearing resources and services stay as they are"),
		forge.WithRequestSchema(planning.RollbackInput{}),
		forge.WithResponseSchema(http.StatusCreated, "The rollback plan", planning.Plan{}),
	)
}

// soakService is what the soak route asks of the release watch.
type soakService interface {
	SoakForPlan(ctx context.Context, p security.Principal, planID string) (*releasewatch.Soak, error)
}

// soakRoutes registers the route that says how long a promotion's source
// environment has run the commits the plan moves. It changes nothing.
func soakRoutes(r forge.Router, guard identity.Guard, svc soakService) error {
	g := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Plans"))
	soak := func(c forge.Context) error {
		p, err := principal(c)
		if err != nil {
			return err
		}
		s, err := svc.SoakForPlan(c.Request().Context(), p, c.Param("id"))
		if err != nil {
			return fail(err)
		}
		return c.JSON(http.StatusOK, s)
	}
	return g.GET("/plans/:id/soak", soak,
		forge.WithName("plans.soak"),
		forge.WithSummary("How long the promotion's source environment has run exactly the commits the plan moves, and whether it ran them healthily; unverified means missing evidence"),
		forge.WithResponseSchema(http.StatusOK, "The soak", releasewatch.Soak{}),
	)
}
