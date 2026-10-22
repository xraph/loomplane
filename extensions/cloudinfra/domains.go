package cloudinfra

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	infra "github.com/xraph/loomplane/internal/cloudinfra"
)

type ZoneList struct {
	Zones []*infra.Zone `json:"zones"`
}

func (ZoneList) ForgeEnvelope() forge.EnvelopeDef { return forge.EnvelopeDef{ItemsField: "zones"} }

type DomainRouteList struct {
	Routes []*infra.Route `json:"routes"`
}

func (DomainRouteList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "routes"}
}

type ProjectZoneList struct {
	Zones []infra.ProjectZone `json:"zones"`
}

type DomainOperationList struct {
	Operations []*infra.RouteOperation `json:"operations"`
}

type HandoffReleaseInput struct {
	DrainSeconds int `json:"drain_seconds"`
}

type RemoveRouteInput struct {
	Revision int64 `json:"revision"`
}

type DiscoverZonesQuery struct {
	ConnectionID string `query:"connection_id"`
}

func (e *Extension) domainRoutes(r forge.Router, guard identity.Guard) error {
	group := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Domains"))
	routes := []route{
		{http.MethodPost, "/projects/:projectId/domain-routes/:routeId/ttl", "domainRoutes.updateTTL", e.updateDomainRouteTTL, infra.RouteTTLInput{}, infra.Route{}, http.StatusOK},
		{http.MethodGet, "/projects/:projectId/domain-routes/:routeId/operations", "domainRoutes.history", e.domainRouteHistory, nil, DomainOperationList{}, http.StatusOK},
		{http.MethodPost, "/projects/:projectId/domain-routes/:routeId/prepare-handoff", "domainRoutes.prepareHandoff", e.prepareDomainHandoff, infra.HandoffInput{}, infra.RouteOperation{}, http.StatusAccepted},
		{http.MethodPost, "/projects/:projectId/route-operations/:operationId/cutover", "domainRoutes.cutover", e.cutoverDomainHandoff, nil, infra.RouteOperation{}, http.StatusAccepted},
		{http.MethodPost, "/projects/:projectId/route-operations/:operationId/rollback", "domainRoutes.rollback", e.rollbackDomainHandoff, nil, infra.RouteOperation{}, http.StatusAccepted},
		{http.MethodPost, "/projects/:projectId/route-operations/:operationId/release-handoff", "domainRoutes.releaseHandoff", e.releaseDomainHandoff, HandoffReleaseInput{}, infra.RouteOperation{}, http.StatusAccepted},
		{http.MethodPost, "/dns-zones/:zoneId/update", "dnsZones.update", e.updateDomainZone, infra.ZoneUpdate{}, infra.Zone{}, http.StatusOK},
		{http.MethodPost, "/dns-zones/:zoneId/remove", "dnsZones.remove", e.removeDomainZone, RemoveRouteInput{}, nil, http.StatusNoContent},
		{http.MethodGet, "/projects/:projectId/dns-zones", "dnsZones.project", e.projectDomainZones, nil, ProjectZoneList{}, http.StatusOK},
		{http.MethodGet, "/dns-zones", "dnsZones.list", e.domainZones, nil, ZoneList{}, http.StatusOK},
		{http.MethodPost, "/dns-zones", "dnsZones.create", e.createDomainZone, infra.ZoneInput{}, infra.Zone{}, http.StatusCreated},
		{http.MethodPost, "/dns-zones/:zoneId/verify", "dnsZones.verify", e.verifyDomainZone, nil, infra.Zone{}, http.StatusOK},
		{http.MethodGet, "/projects/:projectId/domain-routes", "domainRoutes.list", e.listDomainRoutes, nil, DomainRouteList{}, http.StatusOK},
		{http.MethodPost, "/projects/:projectId/domain-routes/preview", "domainRoutes.preview", e.previewDomainRoute, infra.RouteInput{}, infra.RouteOperation{}, http.StatusOK},
		{http.MethodGet, "/projects/:projectId/route-operations/:operationId", "domainRoutes.operation", e.domainRouteOperation, nil, infra.RouteOperation{}, http.StatusOK},
		{http.MethodPost, "/projects/:projectId/route-operations/:operationId/apply", "domainRoutes.apply", e.applyDomainRoute, infra.ApplyInput{}, infra.RouteOperation{}, http.StatusAccepted},
		{http.MethodPost, "/projects/:projectId/route-operations/:operationId/retry", "domainRoutes.retry", e.retryDomainRoute, nil, infra.RouteOperation{}, http.StatusAccepted},
		{http.MethodPost, "/projects/:projectId/domain-routes/:routeId/verify", "domainRoutes.verify", e.verifyDomainRoute, nil, infra.RouteOperation{}, http.StatusAccepted},
		{http.MethodPost, "/projects/:projectId/domain-routes/:routeId/remove", "domainRoutes.remove", e.removeDomainRoute, RemoveRouteInput{}, infra.RouteOperation{}, http.StatusAccepted},
	}
	for _, rt := range routes {
		opts := []forge.RouteOption{forge.WithName(rt.name), forge.WithSummary(rt.name), forge.WithResponseSchema(rt.status, "Result", rt.response)}
		if rt.request != nil {
			opts = append(opts, forge.WithRequestSchema(rt.request))
		}
		var err error
		if rt.method == http.MethodGet {
			err = group.GET(rt.path, rt.handler, opts...)
		} else {
			err = group.POST(rt.path, rt.handler, opts...)
		}
		if err != nil {
			return err
		}
	}
	if err := group.GET("/dns-zones/discover", e.discoverDomainZones, forge.WithName("dnsZones.discover"), forge.WithSummary("dnsZones.discover"), forge.WithQuerySchema(DiscoverZonesQuery{}), forge.WithResponseSchema(http.StatusOK, "Result", infra.ProviderZoneList{})); err != nil {
		return err
	}
	return e.overviewRoutes(group)
}

func (e *Extension) discoverDomainZones(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.DiscoverProviderZones(c.Request().Context(), p, c.Query("connection_id"))
	return result(c, http.StatusOK, v, err)
}

func (e *Extension) domainZones(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.ListZones(c.Request().Context(), p)
	return result(c, http.StatusOK, ZoneList{v}, err)
}

func (e *Extension) createDomainZone(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.ZoneInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.CreateZone(c.Request().Context(), p, in)
	return result(c, http.StatusCreated, v, err)
}

func (e *Extension) verifyDomainZone(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.VerifyZone(c.Request().Context(), p, c.Param("zoneId"))
	return result(c, http.StatusOK, v, err)
}

func (e *Extension) listDomainRoutes(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.ListRoutes(c.Request().Context(), p, c.Param("projectId"))
	return result(c, http.StatusOK, DomainRouteList{v}, err)
}

func (e *Extension) previewDomainRoute(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.RouteInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.PreviewRoute(c.Request().Context(), p, c.Param("projectId"), in)
	return result(c, http.StatusOK, v, err)
}

func (e *Extension) domainRouteOperation(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.GetRouteOperation(c.Request().Context(), p, c.Param("projectId"), c.Param("operationId"))
	return result(c, http.StatusOK, v, err)
}

func (e *Extension) applyDomainRoute(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.ApplyInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.ApplyRoute(c.Request().Context(), p, c.Param("projectId"), c.Param("operationId"), in)
	return result(c, http.StatusAccepted, v, err)
}

func (e *Extension) retryDomainRoute(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.RetryRoute(c.Request().Context(), p, c.Param("projectId"), c.Param("operationId"))
	return result(c, http.StatusAccepted, v, err)
}

func (e *Extension) verifyDomainRoute(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.VerifyRoute(c.Request().Context(), p, c.Param("projectId"), c.Param("routeId"))
	return result(c, http.StatusAccepted, v, err)
}

func (e *Extension) removeDomainRoute(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in RemoveRouteInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.RemoveRoute(c.Request().Context(), p, c.Param("projectId"), c.Param("routeId"), in.Revision)
	return result(c, http.StatusAccepted, v, err)
}

func (e *Extension) updateDomainZone(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.ZoneUpdate
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.UpdateZone(c.Request().Context(), p, c.Param("zoneId"), in)
	return result(c, http.StatusOK, v, err)
}
func (e *Extension) removeDomainZone(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in RemoveRouteInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	err = e.svc.RemoveZone(c.Request().Context(), p, c.Param("zoneId"), in.Revision)
	return result(c, http.StatusNoContent, nil, err)
}
func (e *Extension) projectDomainZones(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.ProjectZones(c.Request().Context(), p, c.Param("projectId"))
	return result(c, http.StatusOK, ProjectZoneList{v}, err)
}

func (e *Extension) prepareDomainHandoff(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.HandoffInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.PrepareRouteHandoff(c.Request().Context(), p, c.Param("projectId"), c.Param("routeId"), in)
	return result(c, http.StatusAccepted, v, err)
}
func (e *Extension) cutoverDomainHandoff(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.CutoverRoute(c.Request().Context(), p, c.Param("projectId"), c.Param("operationId"))
	return result(c, http.StatusAccepted, v, err)
}
func (e *Extension) rollbackDomainHandoff(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.RollbackRoute(c.Request().Context(), p, c.Param("projectId"), c.Param("operationId"))
	return result(c, http.StatusAccepted, v, err)
}
func (e *Extension) releaseDomainHandoff(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in HandoffReleaseInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.ReleaseRouteHandoff(c.Request().Context(), p, c.Param("projectId"), c.Param("operationId"), in.DrainSeconds)
	return result(c, http.StatusAccepted, v, err)
}

func (e *Extension) domainRouteHistory(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.RouteHistory(c.Request().Context(), p, c.Param("projectId"), c.Param("routeId"))
	return result(c, http.StatusOK, DomainOperationList{v}, err)
}

func (e *Extension) updateDomainRouteTTL(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.RouteTTLInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.UpdateRouteTTL(c.Request().Context(), p, c.Param("projectId"), c.Param("routeId"), in)
	return result(c, http.StatusOK, v, err)
}
