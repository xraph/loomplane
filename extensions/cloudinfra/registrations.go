package cloudinfra

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	infra "github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/orchestration"
)

// The service starts registrations only through this optional interface.
var _ infra.RegistrationWorkflow = (*orchestration.Deployments)(nil)

func (e *Extension) registrationRoutes(r forge.Router, guard identity.Guard) error {
	group := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Domain registration"))
	queries := []struct {
		path, name string
		handler    forge.Handler
		query      any
		response   any
	}{
		{"/domain-registration/extensions", "domainRegistration.extensions", e.registrationExtensions, infra.ExtensionsQuery{}, infra.RegistrationExtensionList{}},
		{"/domain-registration/search", "domainRegistration.search", e.searchDomains, infra.DomainSearchQuery{}, infra.DomainOfferList{}},
	}
	for _, q := range queries {
		if err := group.GET(q.path, q.handler, forge.WithName(q.name), forge.WithSummary(q.name), forge.WithQuerySchema(q.query), forge.WithResponseSchema(http.StatusOK, "Result", q.response)); err != nil {
			return err
		}
	}
	routes := []route{
		{http.MethodPost, "/domain-registration/check", "domainRegistration.check", e.checkDomains, infra.DomainCheckInput{}, infra.DomainOfferList{}, http.StatusOK},
		{http.MethodPost, "/domain-registrations/preview", "domainRegistrations.preview", e.previewRegistration, infra.RegistrationInput{}, infra.Registration{}, http.StatusOK},
		{http.MethodPost, "/domain-registrations/:registrationId/approve", "domainRegistrations.approve", e.approveRegistration, infra.RegistrationApproval{}, infra.Registration{}, http.StatusAccepted},
		{http.MethodGet, "/domain-registrations", "domainRegistrations.list", e.listRegistrations, nil, infra.RegistrationList{}, http.StatusOK},
		{http.MethodGet, "/domain-registrations/:registrationId", "domainRegistrations.get", e.getRegistration, nil, infra.Registration{}, http.StatusOK},
		{http.MethodPost, "/domain-registrations/:registrationId/auto-renew", "domainRegistrations.setAutoRenew", e.setRegistrationAutoRenew, infra.AutoRenewInput{}, infra.Registration{}, http.StatusOK},
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
	return nil
}

func (e *Extension) registrationExtensions(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.RegistrationExtensions(c.Request().Context(), p, c.Query("connection_id"))
	return result(c, http.StatusOK, v, err)
}

func (e *Extension) searchDomains(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.SearchDomains(c.Request().Context(), p, c.Query("connection_id"), c.Query("q"))
	return result(c, http.StatusOK, v, err)
}

func (e *Extension) checkDomains(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.DomainCheckInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.CheckDomains(c.Request().Context(), p, in)
	return result(c, http.StatusOK, v, err)
}

func (e *Extension) previewRegistration(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.RegistrationInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.PreviewRegistration(c.Request().Context(), p, in)
	return result(c, http.StatusOK, v, err)
}

func (e *Extension) approveRegistration(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.RegistrationApproval
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.ApproveRegistration(c.Request().Context(), p, c.Param("registrationId"), in)
	return result(c, http.StatusAccepted, v, err)
}

func (e *Extension) listRegistrations(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.ListRegistrations(c.Request().Context(), p)
	return result(c, http.StatusOK, infra.RegistrationList{Registrations: v}, err)
}

func (e *Extension) getRegistration(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	v, err := e.svc.GetRegistration(c.Request().Context(), p, c.Param("registrationId"))
	return result(c, http.StatusOK, v, err)
}

func (e *Extension) setRegistrationAutoRenew(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return result(c, 0, nil, err)
	}
	var in infra.AutoRenewInput
	if err = c.BindJSON(&in); err != nil {
		return result(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.SetRegistrationAutoRenew(c.Request().Context(), p, c.Param("registrationId"), in)
	return result(c, http.StatusOK, v, err)
}
