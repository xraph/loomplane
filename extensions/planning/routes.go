package planning

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/activity"
	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/catalogproxy"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/page"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// PlanQuery is the query of GET /api/v1/projects/:id/plans.
type PlanQuery struct {
	// Environment is an environment's name. Without it, every
	// environment's plans.
	Environment string `query:"environment,omitempty"`
	PageQuery
}

// PageQuery pages a newest-first list.
type PageQuery struct {
	// Before is the next of the page before, to read older ones. Without
	// it, the newest.
	Before string `query:"before,omitempty"`
	// Limit is how many to return: 30 when it's left out, at most 100.
	Limit int `query:"limit,omitempty"`
}

// ActivityQuery is the query of GET /api/v1/activity.
type ActivityQuery struct {
	// Days is how many days back, today included: 30 without it, and at
	// most 90.
	Days int `query:"days,omitempty"`
}

// BindingQuery is the query of GET /api/v1/projects/:id/bindings.
type BindingQuery struct {
	// Environment is the environment's name.
	Environment string `query:"environment"`
}

// PlanList is the body of GET /api/v1/projects/:id/plans.
type PlanList struct {
	Plans []planning.Plan `json:"plans"`
	// Next reads the page after this one, as ?before=. It's empty on the
	// last page.
	Next string `json:"next,omitempty"`
}

// ForgeEnvelope tells the generated client the list wraps Plans.
func (PlanList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "plans"}
}

// RevisionList is the body of GET /api/v1/projects/:id/manifest/revisions.
type RevisionList struct {
	Revisions []manifests.Revision `json:"revisions"`
}

// ForgeEnvelope tells the generated client the list wraps Revisions.
func (RevisionList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "revisions"}
}

// BindingList is the body of GET /api/v1/projects/:id/bindings.
type BindingList struct {
	Bindings []planning.Binding `json:"bindings"`
}

// ForgeEnvelope tells the generated client the list wraps Bindings.
func (BindingList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "bindings"}
}

// DeployedList is the body of GET
// /api/v1/projects/:id/environments/:environment/deployed.
type DeployedList struct {
	Sources []planning.DeployedRepository `json:"sources"`
}

// ForgeEnvelope tells the generated client the list wraps Sources.
func (DeployedList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "sources"}
}

// EnvironmentConnections is the body of the environment connection
// routes: which connection the environment deploys to each cloud with,
// where someone chose.
type EnvironmentConnections struct {
	Environment string            `json:"environment"`
	Connections map[string]string `json:"connections"`
}

// DeploymentList is the body of GET /api/v1/projects/:id/deployments.
type DeploymentList struct {
	Deployments []deployments.Deployment `json:"deployments"`
	// Next reads the page after this one, as ?before=. It's empty on the
	// last page.
	Next string `json:"next,omitempty"`
}

// ForgeEnvelope tells the generated client the list wraps Deployments.
func (DeploymentList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "deployments"}
}

// ProviderList is the body of GET /api/v1/providers. It declares no
// envelope: a provider is keyed by its name, and pkg/provider stays free
// of the dashboard client's tags, so the catalog is cached as one value.
type ProviderList struct {
	Providers []provider.Provider `json:"providers"`
}

func (e *Extension) routes(r forge.Router, guard identity.Guard) error {
	type route struct {
		method, path string
		handler      forge.Handler
		opts         []forge.RouteOption
	}
	manifestRoutes := []route{
		{http.MethodGet, "/projects/:id/manifest", e.currentManifest, []forge.RouteOption{
			forge.WithName("manifests.current"),
			forge.WithSummary("The project's manifest: where its services run and what they need"),
			forge.WithResponseSchema(http.StatusOK, "The current revision", manifests.Revision{}),
		}},
		{http.MethodPut, "/projects/:id/manifest", e.saveManifest, []forge.RouteOption{
			forge.WithName("manifests.save"),
			forge.WithSummary("Save a new revision of the project's manifest, as YAML or as a parsed manifest"),
			forge.WithRequestSchema(manifests.SaveInput{}),
			forge.WithResponseSchema(http.StatusOK, "The current revision, unchanged when nothing changed", manifests.Revision{}),
			forge.WithResponseSchema(http.StatusCreated, "The new revision", manifests.Revision{}),
		}},
		{http.MethodPost, "/projects/:id/manifest/edits", e.editManifest, []forge.RouteOption{
			forge.WithName("manifests.edit"),
			forge.WithSummary("Add a resource to the manifest, or ignore a resource kind, keeping the YAML's comments"),
			forge.WithRequestSchema(manifests.EditInput{}),
			forge.WithResponseSchema(http.StatusOK, "The current revision, unchanged when nothing changed", manifests.Revision{}),
			forge.WithResponseSchema(http.StatusCreated, "The new revision", manifests.Revision{}),
		}},
		{http.MethodGet, "/projects/:id/manifest/revisions", e.manifestHistory, []forge.RouteOption{
			forge.WithName("manifests.history"),
			forge.WithSummary("The project's latest manifest revisions, newest first"),
			forge.WithResponseSchema(http.StatusOK, "Revisions", RevisionList{}),
		}},
		{http.MethodGet, "/projects/:id/manifest/draft", e.draftManifest, []forge.RouteOption{
			forge.WithName("manifests.draft"),
			forge.WithSummary("A first manifest drafted from the newest successful analysis of each repository, to review before saving"),
			forge.WithResponseSchema(http.StatusOK, "The draft", planning.ManifestDraft{}),
		}},
	}
	planRoutes := []route{
		{http.MethodGet, "/providers", e.listProviders, []forge.RouteOption{
			forge.WithName("providers.list"),
			forge.WithSummary("The deployment targets a manifest can name, and what each runs and provisions"),
			forge.WithResponseSchema(http.StatusOK, "Providers", ProviderList{}),
		}},
		{http.MethodPost, "/projects/:id/plans", e.createPlan, []forge.RouteOption{
			forge.WithName("plans.create"),
			forge.WithSummary("Plan an environment from the current manifest and one commit of each repository, replacing its open plan; a scope moves only some of them"),
			forge.WithRequestSchema(planning.CreatePlanInput{}),
			forge.WithResponseSchema(http.StatusCreated, "The plan", planning.Plan{}),
		}},
		{http.MethodGet, "/projects/:id/plans", e.listPlans, []forge.RouteOption{
			forge.WithName("plans.list"),
			forge.WithSummary("The project's latest plans, newest first"),
			forge.WithQuerySchema(PlanQuery{}),
			forge.WithResponseSchema(http.StatusOK, "Plans", PlanList{}),
		}},
		{http.MethodGet, "/plans/:id", e.getPlan, []forge.RouteOption{
			forge.WithName("plans.get"),
			forge.WithSummary("One plan, with its actions and findings"),
			forge.WithResponseSchema(http.StatusOK, "The plan", planning.Plan{}),
		}},
		{http.MethodPost, "/plans/:id/approve", e.approvePlan, []forge.RouteOption{
			forge.WithName("plans.approve"),
			forge.WithSummary("Approve a ready plan, naming the revision you reviewed"),
			forge.WithRequestSchema(planning.DecideInput{}),
			forge.WithResponseSchema(http.StatusOK, "The plan", planning.Plan{}),
		}},
		{http.MethodPost, "/plans/:id/reject", e.rejectPlan, []forge.RouteOption{
			forge.WithName("plans.reject"),
			forge.WithSummary("Reject a ready plan"),
			forge.WithRequestSchema(planning.DecideInput{}),
			forge.WithResponseSchema(http.StatusOK, "The plan", planning.Plan{}),
		}},
		{http.MethodGet, "/projects/:id/environments/:environment/deployed", e.listDeployed, []forge.RouteOption{
			forge.WithName("deployed.list"),
			forge.WithSummary("The commit of each of the project's repositories deployed in the environment; one with nothing deployed there isn't listed, and a plan can't hold it"),
			forge.WithResponseSchema(http.StatusOK, "What's deployed", DeployedList{}),
		}},
		{http.MethodGet, "/projects/:id/environments/:environment/connections", e.environmentConnections, []forge.RouteOption{
			forge.WithName("environmentConnections.get"),
			forge.WithSummary("Which cloud connection the environment deploys to each cloud with, where someone chose"),
			forge.WithResponseSchema(http.StatusOK, "The choices", EnvironmentConnections{}),
		}},
		{http.MethodPut, "/projects/:id/environments/:environment/connections/:cloud", e.chooseConnection, []forge.RouteOption{
			forge.WithName("environmentConnections.choose"),
			forge.WithSummary("Choose the connection the environment deploys to a cloud with, or clear the choice"),
			forge.WithRequestSchema(planning.ChooseConnectionInput{}),
			forge.WithResponseSchema(http.StatusOK, "The choices", EnvironmentConnections{}),
		}},
		{http.MethodGet, "/projects/:id/deployments", e.listDeployments, []forge.RouteOption{
			forge.WithName("deployments.list"),
			forge.WithSummary("The project's latest deployments, newest first, with each step's progress"),
			forge.WithQuerySchema(PageQuery{}),
			forge.WithResponseSchema(http.StatusOK, "Deployments", DeploymentList{}),
		}},
		{http.MethodGet, "/deployments/:id", e.getDeployment, []forge.RouteOption{
			forge.WithName("deployments.get"),
			forge.WithSummary("One deployment, with each step's progress"),
			forge.WithResponseSchema(http.StatusOK, "The deployment", deployments.Deployment{}),
		}},
		{http.MethodPost, "/deployments/:id/cleanup/preview", e.previewFailedCleanup, []forge.RouteOption{forge.WithName("deployments.cleanupPreview"), forge.WithResponseSchema(http.StatusOK, "The cleanup review", deployments.CleanupReview{})}},
		{http.MethodPost, "/deployments/:id/cleanup/apply", e.applyFailedCleanup, []forge.RouteOption{forge.WithName("deployments.cleanupApply"), forge.WithRequestSchema(FailedCleanupInput{}), forge.WithResponseSchema(http.StatusAccepted, "The accepted cleanup", deployments.FailedCleanup{})}},
		{http.MethodGet, "/deployments/:id/cleanup", e.getFailedCleanup, []forge.RouteOption{forge.WithName("deployments.cleanup"), forge.WithResponseSchema(http.StatusOK, "Cleanup progress", deployments.FailedCleanup{})}},
		{http.MethodPost, "/deployments/:id/cleanup/retry", e.retryFailedCleanup, []forge.RouteOption{forge.WithName("deployments.cleanupRetry"), forge.WithResponseSchema(http.StatusAccepted, "Cleanup retry", deployments.FailedCleanup{})}},
		{http.MethodGet, "/deployments/:id/canary", e.getCanary, []forge.RouteOption{
			forge.WithName("deployments.canary"),
			forge.WithSummary("The deployment's canary: where traffic is and every step's verdict"),
			forge.WithResponseSchema(http.StatusOK, "The canary and its verdicts", deployments.CanaryReport{}),
		}},
		{http.MethodPost, "/deployments/:id/retry", e.retryDeployment, []forge.RouteOption{
			forge.WithName("deployments.retry"),
			forge.WithSummary("Run a failed deployment's failed and skipped steps again, with the plan that was approved"),
			forge.WithResponseSchema(http.StatusOK, "The deployment, running again", deployments.Deployment{}),
		}},
		{http.MethodGet, "/plans/:id/deployment", e.planDeployment, []forge.RouteOption{
			forge.WithName("deployments.forPlan"),
			forge.WithSummary("The deployment an approved plan started"),
			forge.WithResponseSchema(http.StatusOK, "The deployment", deployments.Deployment{}),
		}},
		{http.MethodGet, "/projects/:id/bindings", e.listBindings, []forge.RouteOption{
			forge.WithName("bindings.list"),
			forge.WithSummary("What runs each of the project's services and resources in an environment"),
			forge.WithQuerySchema(BindingQuery{}),
			forge.WithResponseSchema(http.StatusOK, "Bindings", BindingList{}),
		}},
	}
	activityRoutes := []route{
		{http.MethodGet, "/activity", e.getActivity, []forge.RouteOption{
			forge.WithName("activity.get"),
			forge.WithSummary("What the organization's Loomplane has done lately: deployments, plans, analyses and drift"),
			forge.WithQuerySchema(ActivityQuery{}),
			forge.WithResponseSchema(http.StatusOK, "Activity", activity.ActivitySummary{}),
		}},
	}
	catalogRoutes := []route{
		{http.MethodGet, "/catalog/kinds", e.listCatalogKinds, []forge.RouteOption{
			forge.WithName("catalogKinds.list"),
			forge.WithSummary("The catalog kinds a manifest can name: the database engines and the composed services, and where each can be placed and run"),
			forge.WithResponseSchema(http.StatusOK, "Kinds", CatalogKindList{}),
		}},
		{http.MethodGet, "/catalog/endpoints", e.listCatalogEndpoints, []forge.RouteOption{
			forge.WithName("catalogEndpoints.list"),
			forge.WithSummary("The external databases and caches a catalog service can connect to, without their DSNs"),
			forge.WithResponseSchema(http.StatusOK, "Endpoints", EndpointList{}),
		}},
		{http.MethodPost, "/catalog/endpoints", e.createCatalogEndpoint, []forge.RouteOption{
			forge.WithName("catalogEndpoints.create"),
			forge.WithSummary("Register an external Postgres or Redis; its DSN is kept in Vault and never returned"),
			forge.WithRequestSchema(catalogconnect.CreateInput{}),
			forge.WithResponseSchema(http.StatusCreated, "The endpoint", catalogconnect.Endpoint{}),
		}},
		{http.MethodPost, "/catalog/endpoints/checks", e.checkCatalogEndpoint, []forge.RouteOption{
			forge.WithName("catalogEndpoints.check"),
			forge.WithSummary("Check a Postgres or Redis DSN from a target against what a kind's dependency needs, before storing it; the DSN isn't stored or returned"),
			forge.WithRequestSchema(catalogconnect.CheckInput{}),
			forge.WithResponseSchema(http.StatusOK, "What the check found", catalogconnect.CheckResult{}),
		}},
		{http.MethodDelete, "/catalog/endpoints/:id", e.deleteCatalogEndpoint, []forge.RouteOption{
			forge.WithName("catalogEndpoints.delete"),
			forge.WithSummary("Forget an external endpoint and its DSN; refused while a service connects to it. Nothing on the endpoint changes"),
		}},
		{http.MethodPost, "/projects/:projectId/environments/:environmentId/catalog/:resource/admin-sessions", e.openCatalogAdmin, []forge.RouteOption{
			forge.WithName("catalogAdmin.open"),
			forge.WithSummary("Open a catalog service's own admin through Loomplane's sandboxed proxy, for 30 minutes of idle time and at most 8 hours; needs permission to modify the project's infrastructure"),
			forge.WithResponseSchema(http.StatusCreated, "The session: open its URL on this origin", catalogproxy.Session{}),
		}},
	}
	groups := []struct {
		tag    string
		routes []route
	}{{"Manifests", manifestRoutes}, {"Plans", planRoutes}, {"Activity", activityRoutes}, {"Catalog", catalogRoutes}}
	for _, grp := range groups {
		g := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags(grp.tag))
		for _, rt := range grp.routes {
			var err error
			switch rt.method {
			case http.MethodGet:
				err = g.GET(rt.path, rt.handler, rt.opts...)
			case http.MethodPost:
				err = g.POST(rt.path, rt.handler, rt.opts...)
			case http.MethodPut:
				err = g.PUT(rt.path, rt.handler, rt.opts...)
			case http.MethodDelete:
				err = g.DELETE(rt.path, rt.handler, rt.opts...)
			}
			if err != nil {
				return err
			}
		}
	}
	return e.fabriqRoutes(r, guard)
}

func principal(c forge.Context) (security.Principal, error) {
	p, ok := security.PrincipalFrom(c.Request().Context())
	if !ok {
		return p, forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	return p, nil
}

func (e *Extension) currentManifest(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	rev, err := e.manifests.Current(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, rev)
}

func (e *Extension) saveManifest(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in manifests.SaveInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"yaml": "..."}`)
	}
	rev, created, err := e.manifests.Save(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	if created {
		return c.JSON(http.StatusCreated, rev)
	}
	return c.JSON(http.StatusOK, rev)
}

func (e *Extension) editManifest(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in manifests.EditInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"add_resource": {...}} or {"ignore": "kind"}`)
	}
	rev, created, err := e.manifests.Edit(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	if created {
		return c.JSON(http.StatusCreated, rev)
	}
	return c.JSON(http.StatusOK, rev)
}

func (e *Extension) manifestHistory(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	revs, err := e.manifests.History(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	out := RevisionList{Revisions: make([]manifests.Revision, 0, len(revs))}
	for _, r := range revs {
		out.Revisions = append(out.Revisions, *r)
	}
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) draftManifest(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	d, err := e.plans.DraftManifest(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, d)
}

func (e *Extension) listProviders(c forge.Context) error {
	if _, err := principal(c); err != nil {
		return err
	}
	list := e.plans.Providers()
	out := ProviderList{Providers: make([]provider.Provider, 0, len(list))}
	for _, p := range list {
		out.Providers = append(out.Providers, *p)
	}
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) createPlan(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in planning.CreatePlanInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"environment": "production"}`)
	}
	plan, err := e.plans.Create(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, plan)
}

func (e *Extension) listPlans(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	before, limit, err := pageOf(c)
	if err != nil {
		return err
	}
	plans, next, err := e.plans.List(c.Request().Context(), p, c.Param("id"), c.Request().URL.Query().Get("environment"), before, limit)
	if err != nil {
		return fail(err)
	}
	out := PlanList{Plans: make([]planning.Plan, 0, len(plans)), Next: next.String()}
	for _, pl := range plans {
		out.Plans = append(out.Plans, *pl)
	}
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) getPlan(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	plan, err := e.plans.Get(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, plan)
}

func (e *Extension) approvePlan(c forge.Context) error {
	return e.decide(c, true)
}

func (e *Extension) rejectPlan(c forge.Context) error {
	return e.decide(c, false)
}

func (e *Extension) decide(c forge.Context, approve bool) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in planning.DecideInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"revision": "...", "note": "..."}`)
	}
	ctx := c.Request().Context()
	var plan *planning.Plan
	if approve {
		plan, err = e.plans.Approve(ctx, p, c.Param("id"), in)
	} else {
		plan, err = e.plans.Reject(ctx, p, c.Param("id"), in)
	}
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, plan)
}

func (e *Extension) listBindings(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	env := c.Request().URL.Query().Get("environment")
	if env == "" {
		return forge.NewHTTPError(http.StatusBadRequest, "say which environment: ?environment=production")
	}
	bindings, err := e.plans.Bindings(c.Request().Context(), p, c.Param("id"), env)
	if err != nil {
		return fail(err)
	}
	out := BindingList{Bindings: make([]planning.Binding, 0, len(bindings))}
	for _, b := range bindings {
		out.Bindings = append(out.Bindings, *b)
	}
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) listDeployed(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	list, err := e.plans.Deployed(c.Request().Context(), p, c.Param("id"), c.Param("environment"))
	if err != nil {
		return fail(err)
	}
	out := DeployedList{Sources: make([]planning.DeployedRepository, 0, len(list))}
	for _, d := range list {
		out.Sources = append(out.Sources, *d)
	}
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) environmentConnections(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	env := c.Param("environment")
	chosen, err := e.plans.EnvironmentConnections(c.Request().Context(), p, c.Param("id"), env)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, EnvironmentConnections{Environment: env, Connections: chosen})
}

func (e *Extension) chooseConnection(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in planning.ChooseConnectionInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"connection_id": "..."}`)
	}
	env := c.Param("environment")
	chosen, err := e.plans.ChooseConnection(c.Request().Context(), p, c.Param("id"), env, c.Param("cloud"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, EnvironmentConnections{Environment: env, Connections: chosen})
}

func (e *Extension) getActivity(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	days := 30
	if raw := c.Request().URL.Query().Get("days"); raw != "" {
		if days, err = strconv.Atoi(raw); err != nil {
			return forge.NewHTTPError(http.StatusUnprocessableEntity, "days is a number of days")
		}
	}
	out, err := e.activity.Summary(c.Request().Context(), p, days)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) listDeployments(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	before, limit, err := pageOf(c)
	if err != nil {
		return err
	}
	list, next, err := e.deploys.List(c.Request().Context(), p, c.Param("id"), before, limit)
	if err != nil {
		return fail(err)
	}
	out := DeploymentList{Deployments: make([]deployments.Deployment, 0, len(list)), Next: next.String()}
	for _, d := range list {
		out.Deployments = append(out.Deployments, *d)
	}
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) getDeployment(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	d, err := e.deploys.Get(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, d)
}

func (e *Extension) getCanary(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	report, err := e.deploys.Canary(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, report)
}

func (e *Extension) planDeployment(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	d, err := e.deploys.ForPlan(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, d)
}

func (e *Extension) retryDeployment(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	d, err := e.deploys.Retry(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, d)
}

// pageOf reads a list's ?before= and ?limit=.
func pageOf(c forge.Context) (page.Cursor, int, error) {
	q := c.Request().URL.Query()
	before, err := page.Parse(q.Get("before"))
	if err != nil {
		return page.Cursor{}, 0, forge.NewHTTPError(http.StatusBadRequest, "before must be the next of an earlier page")
	}
	limit, err := page.Limit(q.Get("limit"))
	if err != nil {
		return page.Cursor{}, 0, forge.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("limit must be a whole number from 1 to %d", page.Max))
	}
	return before, limit, nil
}
