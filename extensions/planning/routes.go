package planning

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// PlanQuery is the query of GET /api/v1/projects/:id/plans.
type PlanQuery struct {
	// Environment is an environment's name. Without it, every
	// environment's plans.
	Environment string `query:"environment,omitempty"`
}

// BindingQuery is the query of GET /api/v1/projects/:id/bindings.
type BindingQuery struct {
	// Environment is the environment's name.
	Environment string `query:"environment"`
}

// PlanList is the body of GET /api/v1/projects/:id/plans.
type PlanList struct {
	Plans []planning.Plan `json:"plans"`
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
		{http.MethodGet, "/projects/:id/manifest/revisions", e.manifestHistory, []forge.RouteOption{
			forge.WithName("manifests.history"),
			forge.WithSummary("The project's latest manifest revisions, newest first"),
			forge.WithResponseSchema(http.StatusOK, "Revisions", RevisionList{}),
		}},
		{http.MethodGet, "/projects/:id/manifest/draft", e.draftManifest, []forge.RouteOption{
			forge.WithName("manifests.draft"),
			forge.WithSummary("A first manifest proposed from the project's latest analysis, to review before saving"),
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
			forge.WithSummary("Plan an environment from the current manifest and the latest analysis, replacing its open plan"),
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
		{http.MethodGet, "/projects/:id/bindings", e.listBindings, []forge.RouteOption{
			forge.WithName("bindings.list"),
			forge.WithSummary("What runs each of the project's services and resources in an environment"),
			forge.WithQuerySchema(BindingQuery{}),
			forge.WithResponseSchema(http.StatusOK, "Bindings", BindingList{}),
		}},
	}
	groups := []struct {
		tag    string
		routes []route
	}{{"Manifests", manifestRoutes}, {"Plans", planRoutes}}
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
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
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
	plans, err := e.plans.List(c.Request().Context(), p, c.Param("id"), c.Request().URL.Query().Get("environment"))
	if err != nil {
		return fail(err)
	}
	out := PlanList{Plans: make([]planning.Plan, 0, len(plans))}
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
