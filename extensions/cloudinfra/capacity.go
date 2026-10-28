package cloudinfra

import (
	"net/http"

	"github.com/xraph/forge"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
)

// CapacityPolicyView is a Cloud's capacity policy, and whether this
// Loomplane lets pools grow on their own yet. While AutomaticExpansion is
// false the policy is kept, but nothing is added or approved. The flag is
// read-only: a save never takes it. CloudID names the Cloud, so the
// dashboard's cached view refreshes when that Cloud's capacity changes.
type CapacityPolicyView struct {
	CloudID            string                `json:"cloud_id" forge:"id"`
	Policy             *infra.CapacityPolicy `json:"policy"`
	AutomaticExpansion bool                  `json:"automatic_expansion"`
}

// ExpansionList is a Cloud's expansions, newest first, without cancelled ones.
// Items are values, never null, so the generated client tags the list.
type ExpansionList struct {
	Expansions []infra.Expansion `json:"expansions"`
}

func (ExpansionList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "expansions"}
}

// ApproveExpansionInput approves the expansion at the revision you reviewed.
// A changed expansion answers 409, so nothing is approved twice. One that a
// capacity limit still blocks answers 422 with the block's sentence.
type ApproveExpansionInput struct {
	ExpectedRevision int64 `json:"expected_revision"`
}

func (e *Extension) policyView(cloudID string, v *infra.CapacityPolicy) CapacityPolicyView {
	return CapacityPolicyView{CloudID: cloudID, Policy: v, AutomaticExpansion: e.svc.AutomaticExpansion()}
}

func (e *Extension) capacityPolicy(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.GetCapacityPolicy(c.Request().Context(), p, c.Param("cloudId"))
	return result(c, http.StatusOK, e.policyView(c.Param("cloudId"), v), err)
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
	return result(c, http.StatusOK, e.policyView(c.Param("cloudId"), v), err)
}

// createScalingPool creates a pool with no servers and the policy that grows
// it, in one step. A stale policy revision answers 409 and creates nothing.
func (e *Extension) createScalingPool(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.ScalingPoolInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.CreateScalingPool(c.Request().Context(), p, c.Param("cloudId"), in)
	return result(c, http.StatusOK, v, err)
}

func (e *Extension) capacityUsage(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.GetCapacityUsage(c.Request().Context(), p, c.Param("cloudId"))
	return result(c, http.StatusOK, v, err)
}

func (e *Extension) refreshCapacity(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.RefreshCapacity(c.Request().Context(), p, c.Param("cloudId"))
	return result(c, http.StatusOK, v, err)
}

func (e *Extension) expansions(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.ListExpansions(c.Request().Context(), p, c.Param("cloudId"))
	list := ExpansionList{Expansions: make([]infra.Expansion, 0, len(v))}
	for _, item := range v {
		list.Expansions = append(list.Expansions, *item)
	}
	return result(c, http.StatusOK, list, err)
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
