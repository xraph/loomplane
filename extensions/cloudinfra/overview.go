package cloudinfra

import (
	"net/http"

	"github.com/xraph/forge"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
)

type ProjectDomainList struct {
	Domains []infra.ProjectDomain `json:"domains"`
}

func (e *Extension) overviewRoutes(group forge.Router) error {
	routes := []route{
		{http.MethodGet, "/domains", "domains.overview", e.domainOverview, nil, infra.DomainOverview{}, http.StatusOK},
		{http.MethodGet, "/projects/:projectId/domains", "domains.project", e.projectDomains, nil, ProjectDomainList{}, http.StatusOK},
		{http.MethodGet, "/projects/:projectId/domain-routes/:routeId/status", "domainRoutes.status", e.domainRouteStatus, nil, infra.RouteStatus{}, http.StatusOK},
	}
	for _, rt := range routes {
		if err := group.GET(rt.path, rt.handler, forge.WithName(rt.name), forge.WithSummary(rt.name), forge.WithResponseSchema(rt.status, "Result", rt.response)); err != nil {
			return err
		}
	}
	return nil
}

func (e *Extension) domainOverview(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.DomainOverview(c.Request().Context(), p)
	return result(c, http.StatusOK, v, err)
}

func (e *Extension) projectDomains(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.ProjectDomains(c.Request().Context(), p, c.Param("projectId"))
	return result(c, http.StatusOK, ProjectDomainList{v}, err)
}

func (e *Extension) domainRouteStatus(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.RouteStatus(c.Request().Context(), p, c.Param("projectId"), c.Param("routeId"))
	return result(c, http.StatusOK, v, err)
}
