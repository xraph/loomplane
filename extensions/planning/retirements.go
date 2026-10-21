package planning

import (
	"net/http"
	"strconv"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/recovery"
)

func (e *Extension) retirementRoutes(group forge.Router) error {
	if err := group.POST("/service-backups/:id/retire", e.retireBackup, forge.WithName("serviceRetirements.create"), forge.WithInvalidates("Backup[]", "Backup:{id}"), forge.WithRequestSchema(recovery.RetirementInput{}), forge.WithResponseSchema(http.StatusAccepted, "Queued backup retirement", recovery.Retirement{})); err != nil {
		return err
	}
	if err := group.GET("/projects/:id/service-backup-retirements", e.listRetirements, forge.WithName("serviceRetirements.list"), forge.WithQuerySchema(recovery.RetirementListInput{}), forge.WithResponseSchema(http.StatusOK, "Backup retirement records", recovery.RetirementPage{})); err != nil {
		return err
	}
	if err := group.GET("/service-backup-retirements/:id", e.getRetirement, forge.WithName("serviceRetirements.get"), forge.WithResponseSchema(http.StatusOK, "Backup retirement", recovery.Retirement{})); err != nil {
		return err
	}
	if err := group.GET("/service-backup-retirements/:id/history", e.retirementHistory, forge.WithName("serviceRetirements.history"), forge.WithQuerySchema(recovery.RetirementHistoryInput{}), forge.WithResponseSchema(http.StatusOK, "Committed retirement history", recovery.RetirementHistory{})); err != nil {
		return err
	}
	return group.POST("/service-backup-retirements/:id/retry", e.retryRetirement, forge.WithName("serviceRetirements.retry"), forge.WithInvalidates("Backup[]"), forge.WithRequestSchema(recovery.RetirementRetryInput{}), forge.WithResponseSchema(http.StatusAccepted, "Retried backup retirement", recovery.Retirement{}))
}

func (e *Extension) retireBackup(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in recovery.RetirementInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "provide request_id and a retention policy")
	}
	r, err := e.backups.Retire(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusAccepted, r)
}

func (e *Extension) retryRetirement(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in recovery.RetirementRetryInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "provide a retry request_id")
	}
	r, err := e.backups.RetryRetirement(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusAccepted, r)
}

func (e *Extension) getRetirement(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	r, err := e.backups.GetRetirement(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, r)
}

func retirementPageQuery(c forge.Context) (before string, limit int, err error) {
	before = c.Query("before")
	if raw := c.Query("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			return "", 0, forge.NewHTTPError(http.StatusBadRequest, "limit must be an integer")
		}
		if limit < 1 || limit > 100 {
			return "", 0, forge.NewHTTPError(http.StatusUnprocessableEntity, "limit must be between 1 and 100")
		}
	}
	return before, limit, nil
}

func (e *Extension) listRetirements(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	before, limit, err := retirementPageQuery(c)
	if err != nil {
		return err
	}
	page, err := e.backups.ListRetirements(c.Request().Context(), p, c.Param("id"), recovery.RetirementListInput{Before: before, Limit: limit, BackupID: c.Query("backup_id")})
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, page)
}

func (e *Extension) retirementHistory(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	before, limit, err := retirementPageQuery(c)
	if err != nil {
		return err
	}
	page, err := e.backups.RetirementHistory(c.Request().Context(), p, c.Param("id"), recovery.RetirementHistoryInput{Before: before, Limit: limit})
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, page)
}
