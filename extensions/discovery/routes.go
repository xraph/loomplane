package discovery

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/security"
)

// AnalysisList is the body of GET /api/v1/projects/:id/analyses.
type AnalysisList struct {
	Analyses []discovery.Analysis `json:"analyses"`
}

// ForgeEnvelope tells the generated client the list wraps Analyses.
func (AnalysisList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "analyses"}
}

// AnalysisGraph is the application graph a succeeded analysis found, keyed
// by the analysis's ID. A separate entity from Analysis, so a list of
// analyses never carries, or overwrites, a whole graph.
type AnalysisGraph struct {
	ID    string      `json:"id" forge:"id"`
	Graph graph.Graph `json:"graph"`
}

func (e *Extension) routes(r forge.Router, guard identity.Guard) error {
	g := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Analyses"))
	if err := g.POST("/projects/:id/analyses", e.start,
		forge.WithName("analyses.create"),
		forge.WithSummary("Analyze the project's repository: find its services, packages and the resources they need"),
		forge.WithRequestSchema(discovery.StartAnalysisInput{}),
		forge.WithResponseSchema(http.StatusCreated, "The queued analyses, one per source", AnalysisList{}),
	); err != nil {
		return err
	}
	if err := g.GET("/projects/:id/analyses", e.list,
		forge.WithName("analyses.list"),
		forge.WithSummary("The project's latest analyses, newest first"),
		forge.WithResponseSchema(http.StatusOK, "Analyses", AnalysisList{}),
	); err != nil {
		return err
	}
	if err := g.GET("/analyses/:id", e.get,
		forge.WithName("analyses.get"),
		forge.WithSummary("One analysis"),
		forge.WithResponseSchema(http.StatusOK, "The analysis", discovery.Analysis{}),
	); err != nil {
		return err
	}
	return g.GET("/analyses/:id/graph", e.graph,
		forge.WithName("analyses.graph"),
		forge.WithSummary("The application graph a succeeded analysis found, with the evidence for each finding"),
		forge.WithResponseSchema(http.StatusOK, "The graph", AnalysisGraph{}),
	)
}

func principal(c forge.Context) (security.Principal, error) {
	p, ok := security.PrincipalFrom(c.Request().Context())
	if !ok {
		return p, forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	return p, nil
}

func (e *Extension) start(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in discovery.StartAnalysisInput
	// An empty body is fine: it means the default branch.
	if c.Request().ContentLength != 0 {
		if err = c.BindJSON(&in); err != nil {
			return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON: {\"ref\": \"main\"}")
		}
	}
	queued, err := e.svc.Start(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	out := AnalysisList{Analyses: make([]discovery.Analysis, 0, len(queued))}
	for _, a := range queued {
		out.Analyses = append(out.Analyses, *a)
	}
	return c.JSON(http.StatusCreated, out)
}

func (e *Extension) list(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	list, err := e.svc.List(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	out := AnalysisList{Analyses: make([]discovery.Analysis, 0, len(list))}
	for _, a := range list {
		out.Analyses = append(out.Analyses, *a)
	}
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) get(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	a, err := e.svc.Get(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, a)
}

func (e *Extension) graph(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	g, err := e.svc.Graph(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, AnalysisGraph{ID: c.Param("id"), Graph: *g})
}
