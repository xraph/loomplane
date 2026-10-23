package planning

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/recovery"
)

type NativeRecoverySources struct {
	Sources    []recovery.NativeSource `json:"sources"`
	NextBefore string                  `json:"next_before,omitempty"`
}

func (NativeRecoverySources) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "sources"}
}

func (e *Extension) nativeBackupRoutes(group forge.Router) error {
	if err := group.GET("/projects/:id/native-recovery-sources", e.listNativeSources, forge.WithName("nativeRecoverySources.list"), forge.WithQuerySchema(recovery.NativePageInput{}), forge.WithResponseSchema(http.StatusOK, "Captured native database histories", NativeRecoverySources{})); err != nil {
		return err
	}
	if err := group.GET("/native-recovery-sources/:id", e.getNativeInventory, forge.WithName("nativeRecoverySources.inventory"), forge.WithQuerySchema(recovery.NativePageInput{}), forge.WithResponseSchema(http.StatusOK, "Provider snapshot observations", recovery.NativeInventory{})); err != nil {
		return err
	}
	if err := group.POST("/native-recovery-sources/:id/refresh", e.refreshNativeInventory, forge.WithName("nativeRecoverySources.refresh"), forge.WithResponseSchema(http.StatusOK, "Refreshed provider snapshot observations", recovery.NativeInventory{})); err != nil {
		return err
	}
	return e.nativeCopyRoutes(group)
}
func nativePage(c forge.Context) (recovery.NativePageInput, error) {
	page := recovery.NativePageInput{Before: c.Query("before"), EnvironmentID: c.Query("environment_id"), Resource: c.Query("resource")}
	if raw := c.Query("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			return page, forge.NewHTTPError(http.StatusBadRequest, "provide limit as an integer from 1 to 100")
		}
		page.Limit = limit
	}
	return page, nil
}
func (e *Extension) listNativeSources(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	page, err := nativePage(c)
	if err != nil {
		return err
	}
	list, err := e.nativeBackups.List(c.Request().Context(), p, c.Param("id"), page)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, NativeRecoverySources{Sources: list.Sources, NextBefore: list.NextBefore})
}
func (e *Extension) getNativeInventory(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	page, err := nativePage(c)
	if err != nil {
		return err
	}
	inventory, err := e.nativeBackups.Inventory(c.Request().Context(), p, c.Param("id"), page)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, inventory)
}
func (e *Extension) refreshNativeInventory(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	inventory, err := e.nativeBackups.Refresh(c.Request().Context(), p, c.Param("id"))
	if errors.Is(err, recovery.ErrNativeUnavailable) {
		return forge.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, inventory)
}
