package connections

import (
	"net/http"

	"github.com/xraph/forge"
)

func (e *Extension) gpus(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	inv, err := e.svc.GPUInventory(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, inv)
}
