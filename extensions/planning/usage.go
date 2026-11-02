package planning

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/usage"
	"github.com/xraph/loomplane/pkg/provider"
)

// UsageQuery is the query of GET /api/v1/usage/traffic.
type UsageQuery struct {
	// Range is how far back: 24h, 7d, 30d or 90d. A week without it.
	Range string `query:"range,omitempty"`
}

// meters are the executors whose clouds report requests and latency.
func meters(executors map[string]deployments.Executor) map[string]usage.Meter {
	out := map[string]usage.Meter{}
	for name, exec := range executors {
		if m, ok := exec.(usage.Meter); ok {
			out[name] = m
		}
	}
	return out
}

// revisionMeters are the executors whose clouds report one revision's
// traffic, which a canary is judged on.
func revisionMeters(executors map[string]deployments.Executor) map[string]deployments.RevisionMeter {
	out := map[string]deployments.RevisionMeter{}
	for name, exec := range executors {
		if m, ok := exec.(deployments.RevisionMeter); ok {
			out[name] = m
		}
	}
	return out
}

// titled names a provider as the catalog does, or by its name.
func titled(catalog *provider.Registry) func(string) string {
	return func(name string) string {
		if p, err := catalog.Get(name); err == nil && p.Title != "" {
			return p.Title
		}
		return name
	}
}

func (e *Extension) usageRoutes(router forge.Router, guard identity.Guard, svc *usage.Service) error {
	group := router.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Usage"))
	if err := group.GET("/usage/spend", func(c forge.Context) error {
		p, err := principal(c)
		if err != nil {
			return err
		}
		out, err := svc.Spend(c.Request().Context(), p)
		if err != nil {
			return fail(err)
		}
		return c.JSON(http.StatusOK, out)
	},
		forge.WithName("usage.spend"),
		forge.WithSummary("What each connected account has cost this month, read from the cloud's own billing, and why any account can't say"),
		forge.WithResponseSchema(http.StatusOK, "Spend", usage.SpendReport{}),
	); err != nil {
		return err
	}
	return group.GET("/usage/traffic", func(c forge.Context) error {
		p, err := principal(c)
		if err != nil {
			return err
		}
		out, err := svc.Traffic(c.Request().Context(), p, c.Request().URL.Query().Get("range"))
		if err != nil {
			return fail(err)
		}
		return c.JSON(http.StatusOK, out)
	},
		forge.WithName("usage.traffic"),
		forge.WithSummary("Requests and latency of every service you can see, read from each cloud's own monitoring, beside the stretch before"),
		forge.WithQuerySchema(UsageQuery{}),
		forge.WithResponseSchema(http.StatusOK, "Traffic", usage.TrafficReport{}),
	)
}
