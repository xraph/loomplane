package identity

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/security"
)

// Session is who's signed in, the organization the session acts in, and
// what they may do there. The dashboard uses Permissions to decide which
// actions to offer; the API checks every action again regardless.
type Session struct {
	UserID      string   `json:"user_id"`
	OrgID       string   `json:"org_id"`
	Permissions []string `json:"permissions"`
}

// PublicConfig is what the dashboard needs before anyone signs in. Nothing
// in it is secret: the publishable key and app ID only name the authsome
// app, which some authsome calls (checking an organization slug) take.
type PublicConfig struct {
	PublishableKey string `json:"publishable_key"`
	AppID          string `json:"app_id"`
	AuthBasePath   string `json:"auth_base_path"`
}

func (e *Extension) routes(r forge.Router) error {
	public := PublicConfig{
		PublishableKey: e.cfg.Identity.PublishableKey,
		AppID:          e.cfg.Identity.AppID,
		AuthBasePath:   e.cfg.Identity.BasePath,
	}
	if err := r.GET("/api/v1/public-config", func(c forge.Context) error { return c.JSON(http.StatusOK, public) },
		forge.WithName("publicConfig.get"),
		forge.WithSummary("Settings the dashboard needs before sign-in"),
		forge.WithTags("Identity"),
		forge.WithResponseSchema(http.StatusOK, "Public settings", PublicConfig{}),
	); err != nil {
		return err
	}
	v1 := r.Group("/api/v1", forge.WithGroupMiddleware(e.middleware()...), forge.WithGroupTags("Identity"))
	if err := v1.GET("/session", e.getSession,
		forge.WithName("session.get"),
		forge.WithSummary("The signed-in user, their active organization and their permissions in it"),
		forge.WithResponseSchema(http.StatusOK, "The session", Session{}),
	); err != nil {
		return err
	}
	return e.memberRoutes(v1)
}

func (e *Extension) getSession(c forge.Context) error {
	ctx := c.Request().Context()
	p, ok := security.PrincipalFrom(ctx)
	if !ok {
		return forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	// Removing someone leaves their session alone, and the session still
	// names the organization. Answer the way a session with no organization
	// is answered, so the dashboard moves them on to one they belong to
	// rather than showing a workspace where every request is refused.
	_, member, err := e.membership(ctx, p.OrgID, p.UserID)
	if err != nil {
		return err
	}
	if !member {
		return forge.NewHTTPError(http.StatusForbidden, security.ErrNotMember.Error())
	}
	// A member with no role yet gets it here, through the same repair
	// Require does, so the first session read already shows their rights.
	if err := e.authz.Require(ctx, p, security.ProjectRead, ""); err != nil && security.StatusFor(err) != http.StatusForbidden {
		return err
	}
	perms := security.AllPermissions()
	out := Session{UserID: p.UserID, OrgID: p.OrgID, Permissions: make([]string, 0, len(perms))}
	for _, perm := range perms {
		allowed, err := e.authz.Allowed(ctx, p, perm)
		if err != nil {
			return err
		}
		if allowed {
			out.Permissions = append(out.Permissions, perm.Name())
		}
	}
	return c.JSON(http.StatusOK, out)
}
