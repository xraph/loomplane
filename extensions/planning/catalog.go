package planning

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/recipes"
)

// CatalogKindList is the body of GET /api/v1/catalog/kinds. It declares no
// envelope: the list is short and unpaged, so the dashboard caches it as
// one value, like ProviderList.
type CatalogKindList struct {
	Kinds []recipes.KindInfo `json:"kinds"`
}

// catalogKindList describes every engine and kind in the registry: the
// five built-in engines first, then the registered ones.
func catalogKindList(r *recipes.Registry) CatalogKindList {
	return CatalogKindList{Kinds: r.Kinds()}
}

func (e *Extension) listCatalogKinds(c forge.Context) error {
	if _, err := principal(c); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, catalogKindList(recipes.Default()))
}

// EndpointList is the body of GET /api/v1/catalog/endpoints.
type EndpointList struct {
	Endpoints []*catalogconnect.Endpoint `json:"endpoints"`
}

// ForgeEnvelope tells the generated client the list wraps Endpoints.
func (EndpointList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "endpoints"}
}

func (e *Extension) listCatalogEndpoints(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	list, err := e.endpoints.List(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, EndpointList{Endpoints: list})
}

func (e *Extension) createCatalogEndpoint(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in catalogconnect.CreateInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"name": "...", "engine": "postgres", "dsn": "postgres://..."}`)
	}
	endpoint, err := e.endpoints.Create(c.Request().Context(), p, in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, endpoint)
}

func (e *Extension) deleteCatalogEndpoint(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err = e.endpoints.Delete(c.Request().Context(), p, c.Param("id")); err != nil {
		return fail(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (e *Extension) checkCatalogEndpoint(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in catalogconnect.CheckInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"engine": "postgres", "dsn": "postgres://...", "target": "...", "kind": "...", "dependency": "..."}`)
	}
	ctx := c.Request().Context()
	if err = e.endpoints.AuthorizeCheck(ctx, p); err != nil {
		return fail(err)
	}
	kinds := e.catalogKinds
	if kinds == nil {
		kinds = recipes.Default()
	}
	kind, ok := kinds.Kind(in.Kind)
	if !ok {
		return fail(fmt.Errorf("%w: Loomplane has no catalog kind %q", catalogconnect.ErrInvalid, in.Kind))
	}
	i := slices.IndexFunc(kind.Dependencies, func(d recipes.Dependency) bool { return d.Name == in.Dependency })
	if i < 0 {
		return fail(fmt.Errorf("%w: %s has no %q dependency", catalogconnect.ErrInvalid, kind.Title, in.Dependency))
	}
	dep := kind.Dependencies[i]
	if dep.External == nil {
		return fail(fmt.Errorf("%w: %s can't connect %s to an external endpoint", catalogconnect.ErrInvalid, kind.Title, dep.Title))
	}
	req := *dep.External
	if in.Target == "" {
		return fail(fmt.Errorf("%w: name the target connection the endpoint is checked from", catalogconnect.ErrInvalid))
	}
	if in.Engine != "" && in.Engine != req.Engine {
		return c.JSON(http.StatusOK, catalogconnect.CheckResult{Problems: []string{
			fmt.Sprintf("this is a %s endpoint, and %s's %s dependency needs %s", catalogconnect.EngineTitle(in.Engine), kind.Title, dep.Name, catalogconnect.EngineTitle(req.Engine)),
		}})
	}
	dial, closer, err := e.endpointDialer.TargetDial(ctx, p.OrgID, in.Target)
	if errors.Is(err, connections.ErrNotFound) {
		return fail(err)
	}
	if err != nil {
		return c.JSON(http.StatusOK, catalogconnect.CheckResult{Problems: []string{"the target connection failed: " + err.Error()}})
	}
	if closer != nil {
		defer func() { _ = closer.Close() }() // a check only reads
	}
	report, err := e.endpointChecks.CheckDSN(catalogconnect.WithConsumer(ctx, catalogconnect.ConsumerOf(kind, dep.Name)), in.DSN, req, dial)
	if err != nil {
		return fail(err)
	}
	result := report.Result()
	// A cluster has nothing inside it to dial from, so its check runs from
	// Loomplane, as a plan's does.
	if cloud, cerr := e.endpointDialer.TargetCloud(ctx, p.OrgID, in.Target); cerr == nil && cloud == connections.Kubernetes {
		result.Notes = append(result.Notes, "Checked from Loomplane, not from inside the cluster.")
	}
	return c.JSON(http.StatusOK, result)
}
