package cloudinfra

import (
	"errors"
	"net/http"
	"strings"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	infra "github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type ServerList struct {
	Servers []*infra.Server `json:"servers"`
}

func (ServerList) ForgeEnvelope() forge.EnvelopeDef { return forge.EnvelopeDef{ItemsField: "servers"} }

type OperationList struct {
	Operations []*infra.Operation `json:"operations"`
}

func (OperationList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "operations"}
}

type route struct {
	method, path, name string
	handler            forge.Handler
	request, response  any
	status             int
}

func (e *Extension) routes(r forge.Router, guard identity.Guard) error {
	g := r.Group("/api/v1/clouds/:cloudId", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Cloud infrastructure"))
	routes := []route{
		{http.MethodPost, "/servers/:serverId/check", "cloudServers.check", e.checkServer, nil, infra.Server{}, http.StatusOK},
		{http.MethodGet, "/servers", "cloudServers.list", e.list, nil, ServerList{}, http.StatusOK},
		{http.MethodGet, "/server-options/:connectionId", "cloudServers.options", e.options, nil, provider.ServerOptions{}, http.StatusOK},
		{http.MethodPost, "/servers/preview", "cloudServers.preview", e.preview, infra.ServerInput{}, infra.Operation{}, http.StatusOK},
		{http.MethodGet, "/infrastructure-operations", "infrastructureOperations.list", e.operations, nil, OperationList{}, http.StatusOK},
		{http.MethodGet, "/infrastructure-operations/:operationId", "infrastructureOperations.get", e.operation, nil, infra.Operation{}, http.StatusOK},
		{http.MethodPost, "/infrastructure-operations/:operationId/apply", "infrastructureOperations.apply", e.apply, infra.ApplyInput{}, infra.Operation{}, http.StatusAccepted},
		{http.MethodPost, "/infrastructure-operations/:operationId/cancel", "infrastructureOperations.cancel", e.cancelOperation, nil, infra.Operation{}, http.StatusOK},
		{http.MethodPost, "/infrastructure-operations/:operationId/retry", "infrastructureOperations.retry", e.retry, nil, infra.Operation{}, http.StatusAccepted},
	}
	for _, rt := range routes {
		opts := []forge.RouteOption{forge.WithName(rt.name), forge.WithSummary(rt.name), forge.WithResponseSchema(rt.status, "Result", rt.response)}
		if rt.request != nil {
			opts = append(opts, forge.WithRequestSchema(rt.request))
		}
		var err error
		if rt.method == http.MethodGet {
			err = g.GET(rt.path, rt.handler, opts...)
		} else {
			err = g.POST(rt.path, rt.handler, opts...)
		}
		if err != nil {
			return err
		}
	}
	return r.POST("/api/v1/infrastructure/enroll/:orgId/:operationId", e.enroll, forge.WithName("infrastructureEnrollment.complete"), forge.WithSummary("Verify one-time server enrollment"), forge.WithRequestSchema(infra.Enrollment{}))
}
func caller(c forge.Context) (security.Principal, error) {
	p, ok := security.PrincipalFrom(c.Request().Context())
	if !ok {
		return p, security.ErrUnauthenticated
	}
	return p, nil
}
func result(c forge.Context, status int, value any, err error) error {
	if err == nil {
		return c.JSON(status, value)
	}
	code := security.StatusFor(err)
	switch {
	case errors.Is(err, infra.ErrInvalid), errors.Is(err, provider.ErrInfrastructureInvalid):
		code = http.StatusUnprocessableEntity
	case errors.Is(err, infra.ErrNotFound), errors.Is(err, cloudprofiles.ErrNotFound), errors.Is(err, connections.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, infra.ErrConflict), errors.Is(err, infra.ErrBusy), errors.Is(err, provider.ErrInfrastructureDrift):
		code = http.StatusConflict
	case errors.Is(err, provider.ErrInfrastructureScope):
		code = http.StatusForbidden
	case errors.Is(err, provider.ErrInfrastructureUnsupported):
		code = http.StatusUnprocessableEntity
	}
	if code == http.StatusInternalServerError {
		return forge.NewHTTPError(code, "Infrastructure operation failed")
	}
	return forge.NewHTTPError(code, err.Error())
}
func (e *Extension) list(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.ListServers(c.Request().Context(), p, c.Param("cloudId"))
	return result(c, http.StatusOK, ServerList{Servers: v}, err)
}
func (e *Extension) operations(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.ListOperations(c.Request().Context(), p, c.Param("cloudId"))
	return result(c, http.StatusOK, OperationList{Operations: v}, err)
}
func (e *Extension) preview(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.ServerInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.PreviewServer(c.Request().Context(), p, c.Param("cloudId"), in)
	return result(c, http.StatusOK, v, err)
}
func (e *Extension) operation(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.GetOperation(c.Request().Context(), p, c.Param("cloudId"), c.Param("operationId"))
	return result(c, http.StatusOK, v, err)
}
func (e *Extension) apply(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.ApplyInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.ApplyServerOperation(c.Request().Context(), p, c.Param("cloudId"), c.Param("operationId"), in)
	return result(c, http.StatusAccepted, v, err)
}
func (e *Extension) retry(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.RetryOperation(c.Request().Context(), p, c.Param("cloudId"), c.Param("operationId"))
	return result(c, http.StatusAccepted, v, err)
}
func (e *Extension) enroll(c forge.Context) error {
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 16<<10)
	var in infra.Enrollment
	if err := c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "Invalid enrollment")
	}
	token := strings.TrimPrefix(c.Request().Header.Get("Authorization"), "Bearer ")
	if err := e.svc.Enroll(c.Request().Context(), c.Param("orgId"), c.Param("operationId"), token, in); err != nil {
		return forge.NewHTTPError(http.StatusConflict, "Enrollment not accepted")
	}
	return c.NoContent(http.StatusNoContent)
}

func (e *Extension) options(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.ServerOptions(c.Request().Context(), p, c.Param("cloudId"), c.Param("connectionId"))
	return result(c, http.StatusOK, v, err)
}

func (e *Extension) cancelOperation(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.CancelOperation(c.Request().Context(), p, c.Param("cloudId"), c.Param("operationId"))
	return result(c, http.StatusOK, v, err)
}

func (e *Extension) checkServer(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.CheckServer(c.Request().Context(), p, c.Param("cloudId"), c.Param("serverId"))
	return result(c, http.StatusOK, v, err)
}
