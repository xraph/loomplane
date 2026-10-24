package planning

import (
	"net/http"
	"strconv"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/recovery"
)

func (e *Extension) nativeRetirementRoutes(group forge.Router) error {
	if err := e.nativeRetentionPolicyRoutes(group); err != nil {
		return err
	}
	if err := group.GET("/native-recovery-sources/:id/retirements", e.listNativeRetirements, forge.WithName("nativeRecoveryRetirements.list"), forge.WithQuerySchema(recovery.NativePageInput{}), forge.WithResponseSchema(http.StatusOK, "Native deletion history for a captured source", recovery.NativeRetirementPage{})); err != nil {
		return err
	}

	if err := group.GET("/native-recovery-sources/:id/retention-preview", e.previewNativeRetention, forge.WithName("nativeRecoverySources.previewRetention"), forge.WithQuerySchema(recovery.RetentionInput{}), forge.WithResponseSchema(http.StatusOK, "Native full-backup retention preview", recovery.RetentionPreview{})); err != nil {
		return err
	}
	if err := group.POST("/native-recovery-sources/:id/points/:pointId/retirements", e.createNativeRetirement, forge.WithName("nativeRecoveryRetirements.create"), forge.WithRequestSchema(recovery.NativeRetirementInput{}), forge.WithResponseSchema(http.StatusAccepted, "Admitted native recovery-point deletion", recovery.NativeRetirement{})); err != nil {
		return err
	}
	if err := group.GET("/native-recovery-retirements/:id", e.getNativeRetirement, forge.WithName("nativeRecoveryRetirements.get"), forge.WithResponseSchema(http.StatusOK, "Native deletion and reconciliation status", recovery.NativeRetirement{})); err != nil {
		return err
	}
	if err := group.POST("/native-recovery-retirements/:id/retry", e.retryNativeRetirement, forge.WithName("nativeRecoveryRetirements.retry"), forge.WithResponseSchema(http.StatusAccepted, "Retried original native deletion", recovery.NativeRetirement{})); err != nil {
		return err
	}
	return group.GET("/native-recovery-retirements/:id/history", e.nativeRetirementHistory, forge.WithName("nativeRecoveryRetirements.history"), forge.WithQuerySchema(recovery.RetirementHistoryInput{}), forge.WithResponseSchema(http.StatusOK, "Committed native deletion transitions", recovery.RetirementHistory{}))
}

func (e *Extension) previewNativeRetention(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	days, err := strconv.Atoi(c.Query("older_than_days"))
	if err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "provide older_than_days from 1 to 3650")
	}
	keep, err := strconv.Atoi(c.Query("keep_last"))
	if err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "provide keep_last from 1 to 1000")
	}
	preview, err := e.nativeBackups.PreviewNativeRetention(c.Request().Context(), p, c.Param("id"), recovery.RetentionInput{OlderThanDays: days, KeepLast: keep})
	if err != nil {
		return nativeCopyError(err)
	}
	return c.JSON(http.StatusOK, preview)
}
func (e *Extension) createNativeRetirement(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in recovery.NativeRetirementInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "provide request_id, retention policy and explicit deletion acknowledgment")
	}
	r, err := e.nativeBackups.CreatePointRetirement(c.Request().Context(), p, c.Param("id"), c.Param("pointId"), in)
	if err != nil {
		return nativeCopyError(err)
	}
	return c.JSON(http.StatusAccepted, r)
}
func (e *Extension) getNativeRetirement(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	r, err := e.nativeBackups.NativeRetirement(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, r)
}
func (e *Extension) retryNativeRetirement(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	r, err := e.nativeBackups.RetryPointRetirement(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return nativeCopyError(err)
	}
	return c.JSON(http.StatusAccepted, r)
}
func (e *Extension) nativeRetirementHistory(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if _, err = e.nativeBackups.NativeRetirement(c.Request().Context(), p, c.Param("id")); err != nil {
		return fail(err)
	}
	in := recovery.RetirementHistoryInput{Before: c.Query("before")}
	if raw := c.Query("limit"); raw != "" {
		limit, parseErr := strconv.Atoi(raw)
		if parseErr != nil {
			return forge.NewHTTPError(http.StatusBadRequest, "provide limit from 1 to 100")
		}
		in.Limit = limit
	}
	page, err := e.nativeBackups.Retirements.NativeRetirementHistory(c.Request().Context(), p.OrgID, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, page)
}

func (e *Extension) listNativeRetirements(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if _, err = e.nativeBackups.Inventory(c.Request().Context(), p, c.Param("id"), recovery.NativePageInput{Limit: 1}); err != nil {
		return fail(err)
	}
	page, err := nativePage(c)
	if err != nil {
		return err
	}
	out, err := e.nativeBackups.Retirements.ListNativeRetirements(c.Request().Context(), p.OrgID, c.Param("id"), page)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, out)
}
