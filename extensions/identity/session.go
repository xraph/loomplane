package identity

import (
	"context"
	"net/http"

	authmw "github.com/xraph/authsome/middleware"
	"github.com/xraph/authsome/rbac"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/platformadmin"
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
// ConsolePath is the exception, and it only goes to a session holding a
// platform role.
type PublicConfig struct {
	PublishableKey string   `json:"publishable_key"`
	AppID          string   `json:"app_id"`
	AuthBasePath   string   `json:"auth_base_path"`
	Features       Features `json:"features"`
	// SetupPending means the instance has no users yet, so the dashboard
	// shows setup instead of sign-in.
	SetupPending bool `json:"setup_pending"`
	// Waitlist means sign-up needs an invitation or an approved waitlist
	// entry.
	Waitlist bool `json:"waitlist"`
	// SetupTokenRequired means setup is pending and needs the setup token
	// from the API's log. It is never true in development.
	SetupTokenRequired bool `json:"setup_token_required"`
	// ConsolePath is where the instance console is mounted, for platform
	// admins only.
	ConsolePath string `json:"console_path,omitempty"`
}

// Features says which optional sections the server runs, so the dashboard
// shows only those.
type Features struct {
	AI bool `json:"ai"` // the server runs the AI section
}

func (e *Extension) routes(r forge.Router) error {
	if err := e.publicConfigRoute(r); err != nil {
		return err
	}
	if err := e.setupRoutes(r); err != nil {
		return err
	}
	v1 := r.Group("/api/v1", forge.WithGroupMiddleware(e.guard()), forge.WithGroupTags("Identity"))
	if err := v1.GET("/session", e.getSession,
		forge.WithName("session.get"),
		forge.WithSummary("The signed-in user, their active organization and their permissions in it"),
		forge.WithResponseSchema(http.StatusOK, "The session", Session{}),
	); err != nil {
		return err
	}
	if err := e.memberRoutes(v1); err != nil {
		return err
	}
	return e.keyRoutes(r)
}

// publicConfigRoute serves the settings the dashboard reads before sign-in.
// A session is optional; with one, a platform admin also learns where the
// console is.
func (e *Extension) publicConfigRoute(r forge.Router) error {
	return r.GET("/api/v1/public-config", e.publicConfig,
		forge.WithName("publicConfig.get"),
		forge.WithSummary("Settings the dashboard needs before sign-in"),
		forge.WithTags("Identity"),
		forge.WithMiddleware(e.sessionMiddleware()...),
		forge.WithResponseSchema(http.StatusOK, "Public settings", PublicConfig{}),
	)
}

// staticPublicConfig is the part of the public config that only the config
// file decides.
func (e *Extension) staticPublicConfig() PublicConfig {
	return PublicConfig{
		PublishableKey: e.cfg.Identity.PublishableKey,
		AppID:          e.cfg.Identity.AppID,
		AuthBasePath:   e.cfg.Identity.BasePath,
		Features:       Features{AI: e.cfg.AI.Enabled},
	}
}

func (e *Extension) publicConfig(c forge.Context) error {
	ctx := c.Request().Context()
	out := e.staticPublicConfig()
	out.SetupPending = e.setupPending(ctx)
	out.SetupTokenRequired = out.SetupPending && !e.cfg.IsDevelopment()
	out.Waitlist = e.waitlistOn(ctx)
	if e.holdsPlatformRole(ctx) {
		out.ConsolePath = e.cfg.Console.Path
	}
	// The answer changes with setup, the waitlist and who asks.
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, out)
}

// holdsPlatformRole reports whether the request's session belongs to a
// platform owner or admin. Without a session, or when the roles can't be
// read, it says no.
func (e *Extension) holdsPlatformRole(ctx context.Context) bool {
	userID, ok := authmw.UserIDFrom(ctx)
	if !ok {
		return false
	}
	roles, ok := e.appRoles(ctx, userID.String())
	return ok && platformadmin.Holds(roles, e.cfg.Identity.AppID)
}

// appRoles reads a user's roles in Loomplane's app, live from Warden rather
// than from anything stamped on the session. A failure is logged and
// reported as ok false.
func (e *Extension) appRoles(ctx context.Context, userID string) (roles []*rbac.Role, ok bool) {
	roles, err := e.platformRoles.ListUserRolesForApp(ctx, e.cfg.Identity.AppID, userID)
	if err != nil {
		e.Logger().Error("read platform roles", forge.F("user_id", userID), forge.F("error", err.Error()))
		return nil, false
	}
	return roles, true
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
