package cloudinfra

import (
	"net/http"

	"github.com/xraph/forge"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
)

type PoolList struct {
	Pools []*infra.Pool `json:"pools"`
}

func (PoolList) ForgeEnvelope() forge.EnvelopeDef { return forge.EnvelopeDef{ItemsField: "pools"} }
func (e *Extension) pools(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.ListPools(c.Request().Context(), p, c.Param("cloudId"))
	return result(c, http.StatusOK, PoolList{Pools: v}, err)
}
func (e *Extension) savePool(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.PoolInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.SavePool(c.Request().Context(), p, c.Param("cloudId"), c.Param("poolId"), in)
	return result(c, http.StatusOK, v, err)
}
func (e *Extension) previewDrain(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.PreviewDrain(c.Request().Context(), p, c.Param("cloudId"), c.Param("serverId"))
	return result(c, http.StatusOK, v, err)
}
func (e *Extension) applyDrain(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.ApplyInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.ApplyDrain(c.Request().Context(), p, c.Param("cloudId"), c.Param("serverId"), in)
	return result(c, http.StatusOK, v, err)
}
func (e *Extension) previewMove(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.MoveInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.PreviewMove(c.Request().Context(), p, c.Param("cloudId"), in)
	return result(c, http.StatusOK, v, err)
}
func (e *Extension) acceptMove(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.MoveInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.AcceptMove(c.Request().Context(), p, c.Param("cloudId"), in)
	return result(c, http.StatusOK, v, err)
}
