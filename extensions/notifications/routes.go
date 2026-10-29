package notifications

import (
	"errors"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/security"
)

func (e *Extension) routes(r forge.Router, guard identity.Guard) error {
	g := r.Group("/api/v1/notifications", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Notifications"))
	routes := []struct {
		method, path, name, summary string
		handler                     forge.Handler
		input, output               any
	}{
		{"GET", "", "notifications.list", "Recent deployment notifications", e.list, nil, notifications.NotificationList{}},
		{"GET", "/preferences", "notifications.preferences.get", "Your deployment email preference", e.preferences, nil, notifications.Preferences{}},
		{"PUT", "/preferences", "notifications.preferences.update", "Change your deployment email preference", e.setPreferences, notifications.PreferenceInput{}, notifications.Preferences{}},
		{"PUT", "/:id/read", "notifications.read", "Mark your notification read", e.read, nil, notifications.OK{}},
		{"POST", "/retry", "notifications.retry", "Retry exhausted notification deliveries for the organization", e.retry, nil, notifications.OK{}},
		{"GET", "/webhooks", "notifications.webhooks.list", "Organization deployment webhooks", e.webhooks, nil, notifications.WebhookEndpointList{}},
		{"POST", "/webhooks", "notifications.webhooks.create", "Create a signed deployment webhook", e.createWebhook, notifications.WebhookEndpointInput{}, notifications.WebhookEndpointSecret{}},
		{"POST", "/webhooks/:id/rotate", "notifications.webhooks.rotate", "Rotate the signing secret", e.rotateWebhook, nil, notifications.WebhookEndpointSecret{}},
		{"DELETE", "/webhooks/:id", "notifications.webhooks.delete", "Delete a webhook endpoint", e.deleteWebhook, nil, notifications.OK{}},
		{"GET", "/webhooks/:id/deliveries", "notifications.webhooks.deliveries", "Recent webhook delivery outcomes", e.deliveries, nil, notifications.DeliveryList{}},
	}
	for _, route := range routes {
		opts := []forge.RouteOption{forge.WithName(route.name), forge.WithSummary(route.summary), forge.WithResponseSchema(http.StatusOK, route.summary, route.output)}
		if route.input != nil {
			opts = append(opts, forge.WithRequestSchema(route.input))
		}
		var err error
		switch route.method {
		case "GET":
			err = g.GET(route.path, route.handler, opts...)
		case "POST":
			err = g.POST(route.path, route.handler, opts...)
		case "PUT":
			err = g.PUT(route.path, route.handler, opts...)
		case "DELETE":
			err = g.DELETE(route.path, route.handler, opts...)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
func principal(c forge.Context) (security.Principal, error) {
	p, ok := security.PrincipalFrom(c.Request().Context())
	if !ok {
		return p, forge.NewHTTPError(http.StatusUnauthorized, "sign in first")
	}
	return p, nil
}
func fail(err error) error {
	switch {
	case errors.Is(err, notifications.ErrPushDisabled):
		return forge.NewHTTPError(http.StatusNotFound, "push notifications are turned off on this server")
	case errors.Is(err, notifications.ErrInvalid):
		return forge.NewHTTPError(http.StatusBadRequest, err.Error())
	case errors.Is(err, notifications.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, "notification resource not found")
	case security.StatusFor(err) != http.StatusInternalServerError:
		return forge.NewHTTPError(security.StatusFor(err), err.Error())
	default:
		return forge.NewHTTPError(http.StatusInternalServerError, "notification request failed")
	}
}
func (e *Extension) list(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	out, err := e.svc.List(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, out)
}
func (e *Extension) preferences(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	out, err := e.svc.Preferences(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, out)
}
func (e *Extension) setPreferences(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in notifications.PreferenceInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "invalid email preference")
	}
	out, err := e.svc.SetPreferences(c.Request().Context(), p, in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, out)
}
func (e *Extension) read(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err = e.svc.MarkRead(c.Request().Context(), p, c.Param("id")); err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, notifications.OK{OK: true})
}
func (e *Extension) retry(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err = e.svc.RetryExhausted(c.Request().Context(), p); err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, notifications.OK{OK: true})
}
func (e *Extension) webhooks(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	out, err := e.svc.ListEndpoints(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, out)
}
func (e *Extension) createWebhook(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in notifications.WebhookEndpointInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "invalid webhook endpoint")
	}
	out, err := e.svc.CreateEndpoint(c.Request().Context(), p, in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, out)
}
func (e *Extension) rotateWebhook(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	out, err := e.svc.RotateEndpoint(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, out)
}
func (e *Extension) deleteWebhook(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err = e.svc.DeleteEndpoint(c.Request().Context(), p, c.Param("id")); err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, notifications.OK{OK: true})
}
func (e *Extension) deliveries(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	out, err := e.svc.Deliveries(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, out)
}
