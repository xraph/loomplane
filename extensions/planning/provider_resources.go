package planning

import (
	"context"
	"errors"
	"net/http"

	"github.com/xraph/forge"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/security"
)

type providerResourceService interface {
	List(context.Context, security.Principal) (*providerresources.ProviderResourceList, error)
	Get(context.Context, security.Principal, string) (*providerresources.ProviderResource, error)
	Sync(context.Context, security.Principal, string) (*providerresources.ProviderResourceSync, error)
	Connect(context.Context, security.Principal, string, providerresources.ConnectInput) (*providerresources.ProviderResource, error)
}

func (e *Extension) providerResourceRoutes(router forge.Router, guard identity.Guard) error {
	return registerProviderResourceRoutes(router, guard, e.providerResources)
}

func registerProviderResourceRoutes(router forge.Router, guard identity.Guard, svc providerResourceService) error {
	group := router.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Provider resources"))
	if err := group.GET("/provider-resources", func(c forge.Context) error {
		return providerResourceCall(c, func(ctx context.Context, p security.Principal) (any, error) { return svc.List(ctx, p) })
	}, forge.WithName("providerResources.list"), forge.WithSummary("List your provider resource inventory and sync attempts"), forge.WithResponseSchema(http.StatusOK, "Provider resources", providerresources.ProviderResourceList{})); err != nil {
		return err
	}
	if err := group.GET("/provider-resources/:resourceId", func(c forge.Context) error {
		return providerResourceCall(c, func(ctx context.Context, p security.Principal) (any, error) {
			return svc.Get(ctx, p, c.Param("resourceId"))
		})
	}, forge.WithName("providerResources.get"), forge.WithSummary("Read a provider resource without its credentials"), forge.WithResponseSchema(http.StatusOK, "Provider resource", providerresources.ProviderResource{})); err != nil {
		return err
	}
	if err := group.POST("/connections/:connectionId/resource-syncs", func(c forge.Context) error {
		return providerResourceCall(c, func(ctx context.Context, p security.Principal) (any, error) {
			return svc.Sync(ctx, p, c.Param("connectionId"))
		})
	}, forge.WithName("providerResources.sync"), forge.WithSummary("Refresh a connected account's resource inventory"), forge.WithResponseSchema(http.StatusOK, "Completed resource sync attempt", providerresources.ProviderResourceSync{})); err != nil {
		return err
	}
	return group.PUT("/provider-resources/:resourceId/connection", func(c forge.Context) error {
		p, err := principal(c)
		if err != nil {
			return err
		}
		var in providerresources.ConnectInput
		if err = c.BindJSON(&in); err != nil {
			return forge.NewHTTPError(http.StatusBadRequest, "the body must be valid JSON")
		}
		out, err := svc.Connect(c.Request().Context(), p, c.Param("resourceId"), in)
		if err != nil {
			return providerResourceFail(err)
		}
		return c.JSON(http.StatusOK, out)
	}, forge.WithName("providerResources.connect"), forge.WithSummary("Store an application DSN for a verified provider resource"), forge.WithRequestSchema(providerresources.ProviderResourceConnectInput{}), forge.WithResponseSchema(http.StatusOK, "Provider resource connection", providerresources.ProviderResource{}))
}
func providerResourceCall(c forge.Context, call func(context.Context, security.Principal) (any, error)) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	out, err := call(c.Request().Context(), p)
	if err != nil {
		return providerResourceFail(err)
	}
	return c.JSON(http.StatusOK, out)
}

// Provider errors can contain native response bodies, so this boundary only
// returns known safe messages. The sync history retains the safe retry reason.
func providerResourceFail(err error) error {
	for _, entry := range []struct {
		err    error
		status int
	}{
		{providerresources.ErrNotFound, http.StatusNotFound},
		{providerresources.ErrInvalid, http.StatusUnprocessableEntity},
		{providerresources.ErrChanged, http.StatusConflict},
		{providerresources.ErrUnverified, http.StatusConflict},
		{providerresources.ErrDiscovery, http.StatusServiceUnavailable},
	} {
		if errors.Is(err, entry.err) {
			return forge.NewHTTPError(entry.status, entry.err.Error())
		}
	}
	if status := security.StatusFor(err); status != http.StatusInternalServerError {
		return forge.NewHTTPError(status, http.StatusText(status))
	}
	return forge.NewHTTPError(http.StatusInternalServerError, "provider resources are unavailable; retry the request")
}

// Only previously requested accounts participate in scheduled refreshes.
func providerResourceOrganizations(db *pgdriver.PgDB) func(context.Context) ([]string, error) {
	return func(ctx context.Context) ([]string, error) {
		rows, err := db.Query(ctx, `SELECT DISTINCT org_id FROM loomplane_provider_resource_syncs ORDER BY org_id`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []string{}
		for rows.Next() {
			var org string
			if scanErr := rows.Scan(&org); scanErr != nil {
				return nil, scanErr
			}
			out = append(out, org)
		}
		return out, rows.Err()
	}
}
