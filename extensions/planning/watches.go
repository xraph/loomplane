package planning

import (
	"context"
	"net/http"

	"github.com/xraph/dispatch/cron"
	"github.com/xraph/dispatch/engine"
	"github.com/xraph/dispatch/job"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/security"
)

// releaseWatchJob judges post-deploy watches and follows their rollbacks,
// once a minute on Dispatch's cron leader.
const releaseWatchJob = "loomplane.release-watch"

// registerReleaseWatch registers the job that ticks watches.
func (e *Extension) registerReleaseWatch(watches *releasewatch.Service) {
	engine.Register(e.engineOf(), job.NewDefinition(releaseWatchJob, func(ctx context.Context, _ orchestration.DriftPayload) error {
		return watches.Tick(ctx)
	}))
}

// scheduleReleaseWatch runs the watch job every minute.
func (e *Extension) scheduleReleaseWatch(ctx context.Context) error {
	return engine.RegisterCron(ctx, e.engineOf(), &cron.Definition[orchestration.DriftPayload]{
		Name: releaseWatchJob, JobName: releaseWatchJob, Schedule: "@every 1m", Queue: orchestration.Queue,
	})
}

// watchService is what the watch routes read.
type watchService interface {
	ForDeployment(ctx context.Context, p security.Principal, deploymentID string) (*releasewatch.Watch, error)
	Armable(ctx context.Context, p security.Principal, projectID, environment string) (*releasewatch.Coverage, error)
	Evidence(ctx context.Context, p security.Principal, projectID, environment string) (*releasewatch.ReleaseEvidence, error)
}

// watchRoutes registers the routes that read post-deploy watches. None of
// them changes anything.
func watchRoutes(r forge.Router, guard identity.Guard, svc watchService) error {
	g := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Deployments"))
	forDeployment := func(c forge.Context) error {
		p, err := principal(c)
		if err != nil {
			return err
		}
		w, err := svc.ForDeployment(c.Request().Context(), p, c.Param("id"))
		if err != nil {
			return fail(err)
		}
		return c.JSON(http.StatusOK, w)
	}
	if err := g.GET("/deployments/:id/watch", forDeployment,
		forge.WithName("releaseWatches.forDeployment"),
		forge.WithSummary("The deployment's post-deploy watch: the services it judges, their verdicts and any automatic rollback; 404 when it has none"),
		forge.WithResponseSchema(http.StatusOK, "The release watch", releasewatch.Watch{}),
	); err != nil {
		return err
	}
	coverage := func(c forge.Context) error {
		p, err := principal(c)
		if err != nil {
			return err
		}
		cov, err := svc.Armable(c.Request().Context(), p, c.Param("id"), c.Param("environment"))
		if err != nil {
			return fail(err)
		}
		return c.JSON(http.StatusOK, cov)
	}
	if err := g.GET("/projects/:id/environments/:environment/watch-coverage", coverage,
		forge.WithName("releaseWatches.coverage"),
		forge.WithSummary("How long a release in the environment would be watched, which services a canary would ramp, and whether each other service can be watched"),
		forge.WithResponseSchema(http.StatusOK, "Watch coverage", releasewatch.Coverage{}),
	); err != nil {
		return err
	}
	evidence := func(c forge.Context) error {
		p, err := principal(c)
		if err != nil {
			return err
		}
		ev, err := svc.Evidence(c.Request().Context(), p, c.Param("id"), c.Param("environment"))
		if err != nil {
			return fail(err)
		}
		return c.JSON(http.StatusOK, ev)
	}
	return g.GET("/projects/:id/environments/:environment/release-evidence", evidence,
		forge.WithName("releaseWatches.evidence"),
		forge.WithSummary("What the post-deploy watch says about the environment's current release; unverified means missing evidence"),
		forge.WithResponseSchema(http.StatusOK, "Release evidence", releasewatch.ReleaseEvidence{}),
	)
}
