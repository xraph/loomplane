package notifications

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/notifications"
)

func (e *Extension) pushRoutes(r forge.Router, guard identity.Guard) error {
	g := r.Group("/api/v1/push", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Push"))
	if err := g.GET("/config", e.pushConfig, forge.WithName("push.config.get"), forge.WithSummary("How this server sends push notifications"),
		forge.WithResponseSchema(http.StatusOK, "How this server sends push notifications", notifications.PushConfig{})); err != nil {
		return err
	}
	if err := g.POST("/devices", e.registerDevice, forge.WithName("push.devices.create"), forge.WithSummary("Register this phone for push notifications"),
		forge.WithRequestSchema(notifications.RegisterDeviceInput{}),
		forge.WithResponseSchema(http.StatusCreated, "The registered phone", notifications.PushDevice{})); err != nil {
		return err
	}
	if err := g.PUT("/devices/:id", e.updateDevice, forge.WithName("push.devices.update"), forge.WithSummary("Change a phone's time zone, quiet hours or settings"),
		forge.WithRequestSchema(notifications.UpdateDeviceInput{}),
		forge.WithResponseSchema(http.StatusOK, "The updated phone", notifications.PushDevice{})); err != nil {
		return err
	}
	return g.DELETE("/devices/:id", e.deleteDevice, forge.WithName("push.devices.delete"), forge.WithSummary("Stop sending push notifications to a phone"),
		forge.WithNoContentResponse())
}

func (e *Extension) registerDevice(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in notifications.RegisterDeviceInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "invalid push device")
	}
	out, err := e.svc.RegisterDevice(c.Request().Context(), p, in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, out)
}

func (e *Extension) updateDevice(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in notifications.UpdateDeviceInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "invalid push device")
	}
	out, err := e.svc.UpdateDevice(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) deleteDevice(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err = e.svc.DeleteDevice(c.Request().Context(), p, c.Param("id")); err != nil {
		return fail(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (e *Extension) pushConfig(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	out, err := e.svc.PushConfig(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, out)
}
