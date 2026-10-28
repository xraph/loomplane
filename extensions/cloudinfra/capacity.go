package cloudinfra

import (
	"net/http"

	"github.com/xraph/forge"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
)

// CapacityPolicyView is a Cloud's capacity policy, and whether this
// Loomplane lets pools grow on their own yet. While AutomaticExpansion is
// false the policy is kept, but nothing is added or approved.
type CapacityPolicyView struct {
	Policy             *infra.CapacityPolicy `json:"policy"`
	AutomaticExpansion bool                  `json:"automatic_expansion"`
}

type ExpansionList struct {
	Expansions []*infra.Expansion `json:"expansions"`
}

func (ExpansionList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "expansions"}
}

// ApproveExpansionInput approves the expansion at the revision you reviewed.
type ApproveExpansionInput struct {
	ExpectedRevision int64 `json:"expected_revision"`
}

func (e *Extension) capacityPolicy(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.GetCapacityPolicy(c.Request().Context(), p, c.Param("cloudId"))
	return result(c, http.StatusOK, CapacityPolicyView{Policy: v, AutomaticExpansion: e.svc.AutomaticExpansion()}, err)
}
func (e *Extension) saveCapacityPolicy(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.CapacityPolicyInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.SaveCapacityPolicy(c.Request().Context(), p, c.Param("cloudId"), in)
	return result(c, http.StatusOK, CapacityPolicyView{Policy: v, AutomaticExpansion: e.svc.AutomaticExpansion()}, err)
}
func (e *Extension) expansions(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.ListExpansions(c.Request().Context(), p, c.Param("cloudId"))
	return result(c, http.StatusOK, ExpansionList{Expansions: v}, err)
}
func (e *Extension) approveExpansion(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in ApproveExpansionInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.ApproveExpansion(c.Request().Context(), p, c.Param("cloudId"), c.Param("expansionId"), in.ExpectedRevision)
	return result(c, http.StatusOK, v, err)
}
