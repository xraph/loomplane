package cloudinfra

import (
	"net/http"

	"github.com/xraph/forge"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
)

func (e *Extension) getDomainEdge(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.GetEdge(c.Request().Context(), p, c.Param("projectId"), c.Param("routeId"))
	return result(c, http.StatusOK, v, err)
}
func (e *Extension) updateDomainEdge(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.EdgeInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.UpdateEdge(c.Request().Context(), p, c.Param("projectId"), c.Param("routeId"), in)
	return result(c, http.StatusAccepted, v, err)
}
func (e *Extension) purgeDomainEdge(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.EdgePurgeInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.PurgeEdge(c.Request().Context(), p, c.Param("projectId"), c.Param("routeId"), in)
	return result(c, http.StatusAccepted, v, err)
}
func (e *Extension) retryDomainEdge(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in RemoveRouteInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.RetryEdge(c.Request().Context(), p, c.Param("projectId"), c.Param("routeId"), in.Revision)
	return result(c, http.StatusAccepted, v, err)
}
