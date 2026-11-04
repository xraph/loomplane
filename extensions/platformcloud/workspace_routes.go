package platformcloud

import (
	"log/slog"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/platformcloud"
	"github.com/xraph/loomplane/internal/security"
)

// WorkspacePlatformCloud is a workspace's own view of Loomplane Cloud: whether
// it may use it, why it is suspended, its limits and use, the regions and
// where its services are. It never names a host, an endpoint or host
// evidence.
type WorkspacePlatformCloud = platformcloud.WorkspaceView

// mountWorkspace serves a workspace's own view. It needs the permission the
// organization's Clouds page needs.
func (r *routes) mountWorkspace(router forge.Router, guard identity.Guard) error {
	g := router.Group("/api/v1/platform-cloud", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Platform Cloud"))
	return g.GET("", r.workspace,
		forge.WithName("workspacePlatformCloud.get"),
		forge.WithSummary("Your workspace's Loomplane Cloud: whether it's enabled, why it's suspended, limits, use, regions and placements"),
		forge.WithResponseSchema(http.StatusOK, "Loomplane Cloud for this workspace", WorkspacePlatformCloud{}),
		forge.WithResponseSchema(http.StatusForbidden, "Needs the organization's Clouds permission", nil),
	)
}

func (r *routes) workspace(c forge.Context) error {
	ctx := c.Request().Context()
	p, ok := security.PrincipalFrom(ctx)
	if !ok {
		return forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	if err := r.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID); err != nil {
		return forge.NewHTTPError(security.StatusFor(err), err.Error())
	}
	v, err := r.svc.Workspace(ctx, p.OrgID)
	if err != nil {
		slog.ErrorContext(ctx, "platform cloud workspace view failed", "org", p.OrgID, "error", err)
		return forge.NewHTTPError(http.StatusInternalServerError, "Loomplane Cloud couldn't be read right now.")
	}
	return c.JSON(http.StatusOK, v)
}
