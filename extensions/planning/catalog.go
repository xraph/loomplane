package planning

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/catalogconnect"
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
