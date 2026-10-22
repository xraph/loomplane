package planning

import (
	"net/http"

	"github.com/xraph/forge"

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
