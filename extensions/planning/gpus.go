package planning

import (
	"net/http"
	"strconv"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/gpus"
)

// gpuTargetRoutes serves where GPUs can run, for the model registry.
func (e *Extension) gpuTargetRoutes(router forge.Router, guard identity.Guard, svc *gpus.Service) error {
	group := router.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("GPUs"))
	return group.GET("/gpu-targets", func(c forge.Context) error {
		p, err := principal(c)
		if err != nil {
			return err
		}
		q, err := gpuTargetQuery(c)
		if err != nil {
			return err
		}
		out, err := svc.Targets(c.Request().Context(), p, q)
		if err != nil {
			return fail(err)
		}
		return c.JSON(http.StatusOK, out)
	},
		forge.WithName("gpuTargets.list"),
		forge.WithSummary("Where GPUs can run and what they cost per hour"),
		forge.WithQuerySchema(gpus.GPUTargetQuery{}),
		forge.WithResponseSchema(http.StatusOK, "GPUTargetList", gpus.GPUTargetList{}),
	)
}

// gpuTargetQuery reads the query. A number that isn't one is a bad request.
func gpuTargetQuery(c forge.Context) (gpus.GPUTargetQuery, error) {
	values := c.Request().URL.Query()
	q := gpus.GPUTargetQuery{Kind: values.Get("kind")}
	for name, into := range map[string]*int{"count": &q.Count, "min_memory_gib": &q.MinMemoryGiB} {
		raw := values.Get(name)
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			return q, forge.NewHTTPError(http.StatusUnprocessableEntity, name+" must be a whole number")
		}
		*into = n
	}
	return q, nil
}
