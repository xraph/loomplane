package planning

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/xraph/dispatch/cron"
	"github.com/xraph/dispatch/engine"
	"github.com/xraph/dispatch/job"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/fabriqloom"
	"github.com/xraph/loomplane/internal/orchestration"
)

const (
	fabriqRebuildJob       = "loomplane.fabriq.rebuild"
	fabriqRebuildReconcile = "loomplane.fabriq.rebuild.reconcile"
	// fabriqRebuildTimeout covers the rebuild job's hour and the time it
	// takes to start and to record how it ended.
	fabriqRebuildTimeout = 70 * time.Minute
)

// TenantList is the body of GET .../fabriq/:resource/tenants.
type TenantList struct {
	Tenants []fabriqloom.Tenant `json:"tenants"`
}

// ForgeEnvelope tells the generated client the list wraps Tenants.
func (TenantList) ForgeEnvelope() forge.EnvelopeDef { return forge.EnvelopeDef{ItemsField: "tenants"} }

// FabriqKeyList is the body of GET .../fabriq/:resource/keys.
type FabriqKeyList struct {
	Keys []fabriqloom.ConsumerKey `json:"keys"`
}

// ForgeEnvelope tells the generated client the list wraps Keys.
func (FabriqKeyList) ForgeEnvelope() forge.EnvelopeDef { return forge.EnvelopeDef{ItemsField: "keys"} }

// RebuildList is the body of GET .../fabriq/:resource/rebuilds.
type RebuildList struct {
	Rebuilds []fabriqloom.Rebuild `json:"rebuilds"`
}

// ForgeEnvelope tells the generated client the list wraps Rebuilds.
func (RebuildList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "rebuilds"}
}

// TenantInput is the body of POST .../fabriq/:resource/tenants.
type TenantInput struct {
	// TenantID is lowercase letters, digits and dashes, starting with a
	// letter or digit, at most 63 characters.
	TenantID string `json:"tenant_id"`
}

// RebuildInput is the body of POST .../fabriq/:resource/rebuilds.
type RebuildInput struct {
	// Projection is "graph" (FalkorDB) or "search" (Elasticsearch).
	Projection string `json:"projection"`
	// RequestID makes a retried request return what the first one made:
	// 8 to 128 letters, digits, underscores or hyphens.
	RequestID string `json:"request_id"`
}

// setupFabriq keeps the Fabriq panel's service and registers the rebuild
// job and its reconciler with Dispatch.
func (e *Extension) setupFabriq(svc *fabriqloom.Service) {
	svc.Enqueue = e.enqueueFabriqRebuild
	e.fabriq = svc
	engine.Register(e.engineOf(), job.NewDefinition(fabriqRebuildJob, func(ctx context.Context, in backupPayload) error {
		return e.fabriq.RunRebuild(ctx, in.OrgID, in.ID)
	}, job.WithMaxRetries(0), job.WithTimeout(fabriqRebuildTimeout), job.WithQueue(orchestration.Queue)))
	engine.Register(e.engineOf(), job.NewDefinition(fabriqRebuildReconcile, func(ctx context.Context, _ orchestration.DriftPayload) error {
		return e.fabriq.ReconcileRebuilds(ctx)
	}))
}

// enqueueFabriqRebuild hands a queued rebuild to Dispatch. Its job runs
// once: a rerun is a new request.
func (e *Extension) enqueueFabriqRebuild(ctx context.Context, org, id string) error {
	_, err := engine.Enqueue(ctx, e.engineOf(), fabriqRebuildJob, backupPayload{OrgID: org, ID: id},
		job.WithQueue(orchestration.Queue), job.WithMaxRetries(0), job.WithTimeout(fabriqRebuildTimeout))
	return err
}

// scheduleFabriqRebuilds runs the rebuild reconciler every minute.
func (e *Extension) scheduleFabriqRebuilds(ctx context.Context) error {
	return engine.RegisterCron(ctx, e.engineOf(), &cron.Definition[orchestration.DriftPayload]{
		Name: fabriqRebuildReconcile, JobName: fabriqRebuildReconcile, Schedule: "@every 1m", Queue: orchestration.Queue,
	})
}

func (e *Extension) fabriqRoutes(r forge.Router, guard identity.Guard) error {
	// The same shape as catalogAdmin.open's path.
	const base = "/projects/:projectId/environments/:environmentId/fabriq/:resource"
	g := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Fabriq"))
	if err := g.GET(base, e.fabriqStatus,
		forge.WithName("fabriq.status"),
		forge.WithSummary("A hosted Fabriq's stack: its release, entity types and migrations, and whether its console answers"),
		forge.WithResponseSchema(http.StatusOK, "The status, with reachable false and a reason when Fabriq doesn't answer", fabriqloom.Status{}),
	); err != nil {
		return err
	}
	if err := g.GET(base+"/tenants", e.listFabriqTenants,
		forge.WithName("fabriqTenants.list"),
		forge.WithSummary("A hosted Fabriq's tenants, by id"),
		forge.WithResponseSchema(http.StatusOK, "Tenants", TenantList{}),
	); err != nil {
		return err
	}
	if err := g.POST(base+"/tenants", e.createFabriqTenant,
		forge.WithName("fabriqTenants.create"),
		forge.WithSummary("Create a tenant in a hosted Fabriq, or find it"),
		forge.WithRequestSchema(TenantInput{}),
		forge.WithResponseSchema(http.StatusCreated, "The new tenant", fabriqloom.Tenant{}),
		forge.WithResponseSchema(http.StatusOK, "The tenant, which was there already", fabriqloom.Tenant{}),
	); err != nil {
		return err
	}
	if err := g.GET(base+"/keys", e.listFabriqKeys,
		forge.WithName("fabriqKeys.list"),
		forge.WithSummary("The Fabriq key of each service that needs it or had one; never a key itself, only its prefix"),
		forge.WithResponseSchema(http.StatusOK, "Keys", FabriqKeyList{}),
	); err != nil {
		return err
	}
	if err := g.POST(base+"/keys/:service/rotate", e.rotateFabriqKey,
		forge.WithName("fabriqKeys.rotate"),
		forge.WithSummary("Issue a service a new Fabriq key, which its next release takes and then revokes the old one"),
		forge.WithResponseSchema(http.StatusOK, "The service's key, rotating", fabriqloom.ConsumerKey{}),
	); err != nil {
		return err
	}
	if err := g.POST(base+"/keys/:service/revoke", e.revokeFabriqKey,
		forge.WithName("fabriqKeys.revoke"),
		forge.WithSummary("Revoke a service's Fabriq keys now; its next release gets a new one while it still needs Fabriq"),
		forge.WithResponseSchema(http.StatusOK, "The service's key, revoked", fabriqloom.ConsumerKey{}),
	); err != nil {
		return err
	}
	if err := g.GET(base+"/rebuilds", e.listFabriqRebuilds,
		forge.WithName("fabriqRebuilds.list"),
		forge.WithSummary("A hosted Fabriq's latest projection rebuilds, newest first"),
		forge.WithResponseSchema(http.StatusOK, "Rebuilds", RebuildList{}),
	); err != nil {
		return err
	}
	return g.POST(base+"/rebuilds", e.createFabriqRebuild,
		forge.WithName("fabriqRebuilds.create"),
		forge.WithSummary("Rebuild a FalkorDB or Elasticsearch projection from Postgres; while one is queued or running, return it"),
		forge.WithRequestSchema(RebuildInput{}),
		forge.WithResponseSchema(http.StatusAccepted, "The rebuild", fabriqloom.Rebuild{}),
	)
}

func fabriqRef(c forge.Context) deployments.ResourceRef {
	return deployments.ResourceRef{ProjectID: c.Param("projectId"), EnvironmentID: c.Param("environmentId"), Resource: c.Param("resource")}
}

// failFabriq maps the panel's errors to responses. A Fabriq that can't be
// reached is a 503 that says why: written here, because the router hides
// what a 5xx error says.
func failFabriq(c forge.Context, err error) error {
	var down *fabriqloom.UnavailableError
	switch {
	case errors.As(err, &down):
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": down.Error()})
	case errors.Is(err, fabriqloom.ErrInvalid):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, fabriqloom.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, fabriqloom.ErrRequestConflict):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	}
	return fail(err)
}

func (e *Extension) fabriqStatus(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	st, err := e.fabriq.Status(c.Request().Context(), p, fabriqRef(c))
	if err != nil {
		return failFabriq(c, err)
	}
	return c.JSON(http.StatusOK, st)
}

func (e *Extension) listFabriqTenants(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	tenants, err := e.fabriq.Tenants(c.Request().Context(), p, fabriqRef(c))
	if err != nil {
		return failFabriq(c, err)
	}
	return c.JSON(http.StatusOK, TenantList{Tenants: tenants})
}

func (e *Extension) createFabriqTenant(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in TenantInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"tenant_id": "umbrella"}`)
	}
	tenant, created, err := e.fabriq.CreateTenant(c.Request().Context(), p, fabriqRef(c), in.TenantID)
	if err != nil {
		return failFabriq(c, err)
	}
	if created {
		return c.JSON(http.StatusCreated, tenant)
	}
	return c.JSON(http.StatusOK, tenant)
}

func (e *Extension) listFabriqKeys(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	keys, err := e.fabriq.Consumers(c.Request().Context(), p, fabriqRef(c))
	if err != nil {
		return failFabriq(c, err)
	}
	return c.JSON(http.StatusOK, FabriqKeyList{Keys: keys})
}

func (e *Extension) rotateFabriqKey(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	key, err := e.fabriq.Rotate(c.Request().Context(), p, fabriqRef(c), c.Param("service"))
	if err != nil {
		return failFabriq(c, err)
	}
	return c.JSON(http.StatusOK, key)
}

func (e *Extension) revokeFabriqKey(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	key, err := e.fabriq.Revoke(c.Request().Context(), p, fabriqRef(c), c.Param("service"))
	if err != nil {
		return failFabriq(c, err)
	}
	return c.JSON(http.StatusOK, key)
}

func (e *Extension) listFabriqRebuilds(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	list, err := e.fabriq.Rebuilds(c.Request().Context(), p, fabriqRef(c))
	if err != nil {
		return failFabriq(c, err)
	}
	return c.JSON(http.StatusOK, RebuildList{Rebuilds: list})
}

func (e *Extension) createFabriqRebuild(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in RebuildInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"projection": "graph", "request_id": "..."}`)
	}
	r, err := e.fabriq.RequestRebuild(c.Request().Context(), p, fabriqRef(c), in.Projection, in.RequestID)
	if err != nil {
		return failFabriq(c, err)
	}
	return c.JSON(http.StatusAccepted, r)
}
