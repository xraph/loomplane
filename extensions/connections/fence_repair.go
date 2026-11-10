package connections

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/connections"
)

func (e *Extension) reviewFenceRepair(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	review, err := e.svc.ReviewFenceRepair(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, review)
}

func (e *Extension) repairFence(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in connections.FenceRepairInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"review": "..."}`)
	}
	repaired, err := e.svc.RepairFence(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, repaired)
}
