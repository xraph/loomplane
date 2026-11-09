package cloudinfra

import (
	"net/http"

	"github.com/xraph/forge"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
)

// NodeAdoptionList is the nodes joined into a Cloud by their join tokens,
// and where each one's adoption stands.
type NodeAdoptionList struct {
	Adoptions []*infra.NodeAdoption `json:"adoptions"`
}

func (NodeAdoptionList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "adoptions"}
}

func (e *Extension) nodeAdoptions(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.ListNodeAdoptions(c.Request().Context(), p, c.Param("cloudId"))
	return result(c, http.StatusOK, NodeAdoptionList{Adoptions: v}, err)
}
