package notifications

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/notifications"
)

func (e *Extension) pushRoutes(r forge.Router, guard identity.Guard) error {
	g := r.Group("/api/v1/push", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Push"))
	return g.GET("/config", e.pushConfig, forge.WithName("push.config.get"), forge.WithSummary("How this server sends push notifications"),
		forge.WithResponseSchema(http.StatusOK, "How this server sends push notifications", notifications.PushConfig{}))
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
