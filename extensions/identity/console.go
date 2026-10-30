package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	authcontract "github.com/xraph/authsome/extension/contract"
	authmw "github.com/xraph/authsome/middleware"
	"github.com/xraph/authsome/plugin"
	"github.com/xraph/authsome/rbac"
	chroniclecontract "github.com/xraph/chronicle/extension/contract"
	"github.com/xraph/forge"
	"github.com/xraph/forge/extensions/dashboard"
	dashauth "github.com/xraph/forge/extensions/dashboard/auth"
	"github.com/xraph/forge/extensions/dashboard/contract"
	"github.com/xraph/forge/extensions/dashboard/contract/dispatcher"
	"github.com/xraph/forge/extensions/dashboard/contract/transport"
	keysmithcontract "github.com/xraph/keysmith/extension/contract"
	wardencontract "github.com/xraph/warden/extension/contract"

	"github.com/xraph/loomplane/internal/platformadmin"
)

// ConsoleBasePath is where the Forge dashboard's API, the instance console,
// is mounted. It's under /api/, so the web extension's catch-all never
// answers it with the SPA.
const ConsoleBasePath = "/api/_console"

// consoleMaxBody caps a console request body the gate reads, the same cap
// the dashboard puts on a contract envelope.
const consoleMaxBody = 1 << 20

// consoleNotFound is the API's answer to a path nobody serves, word for word
// what the web extension's catch-all sends (extensions/web). The console gate
// answers with it, so a refused request can't be told from an unknown one.
const consoleNotFound = `{"error":"not found"}`

// consoleUserKey carries the console user the gate admitted, so the
// dashboard sees the same identity the gate checked.
type consoleUserKey struct{}

// consoleChecker is the console's dashauth.AuthChecker: authsome's own
// dashboard checker (authsome extension/dashboard_auth.go) reading
// Loomplane's session cookie, with the user's roles in Loomplane's app.
type consoleChecker struct{ e *Extension }

var _ dashauth.AuthChecker = consoleChecker{}

// CheckAuth returns the signed-in platform admin, or nil for anyone else.
// The gate already turns everyone else away; this doesn't rely on it.
func (c consoleChecker) CheckAuth(ctx context.Context, r *http.Request) (*dashauth.UserInfo, error) {
	if u, ok := ctx.Value(consoleUserKey{}).(*dashauth.UserInfo); ok {
		return u, nil
	}
	u, admin := c.e.consoleUser(ctx, r)
	if !admin {
		return nil, nil
	}
	return u, nil
}

// consoleUser resolves the request's session the way authsome's dashboard
// checker does, DPoP binding included, and reads the user's roles in
// Loomplane's app live from Warden. admin says whether they hold a platform
// role. Anything that fails resolves to nobody.
func (e *Extension) consoleUser(ctx context.Context, r *http.Request) (u *dashauth.UserInfo, admin bool) {
	scheme, token := authmw.ExtractCredentialFromContext(ctx, r, e.cfg.Identity.SessionCookie)
	if token == "" {
		return nil, false
	}
	sess, err := e.engine.ResolveSessionByToken(ctx, token)
	if err != nil {
		return nil, false
	}
	if authmw.EnforceDPoPForRequest(
		ctx, e.engine.DPoPBindingConfig(), r,
		sess.DPoPJKT, scheme, token,
		sess.AppID.String(), sess.ID.String(),
		e.engine.Logger(),
	) != nil {
		return nil, false
	}
	usr, err := e.engine.ResolveUser(ctx, sess.UserID.String())
	if err != nil {
		return nil, false
	}
	roles, ok := e.appRoles(ctx, usr.ID.String())
	if !ok {
		return nil, false
	}
	u = &dashauth.UserInfo{
		Subject:     usr.ID.String(),
		DisplayName: usr.Name(),
		Email:       usr.Email,
		AvatarURL:   usr.Image,
	}
	for _, role := range roles {
		if role.AppID == e.cfg.Identity.AppID {
			u.Roles = append(u.Roles, role.Slug)
		}
	}
	return u, platformadmin.Holds(roles, e.cfg.Identity.AppID)
}

// RegisterDashboardAuth implements dashboard.DashboardAuthAware: the console
// signs people in with Loomplane's session. The dashboard calls it while it
// starts, before it registers its routes.
func (e *Extension) RegisterDashboardAuth(d *dashboard.Extension) {
	// The gate follows the dashboard wherever its config put it.
	if base := d.BasePath(); base != ConsoleBasePath {
		e.Logger().Warn("the console API isn't at its usual path", forge.F("base_path", base), forge.F("expected", ConsoleBasePath))
	}
	e.consoleBase.Store(d.BasePath())
	d.SetAuthChecker(consoleChecker{e})
	d.SetTenantResolver(dashauth.ScopeTenantResolver{})
	d.EnableAuth()
	d.SetRequiredRoles([]string{rbac.PlatformOwnerSlug, rbac.PlatformAdminSlug})
}

// registryRef holds the dashboard's contract registry for the gate.
type registryRef struct{ contract.Registry }

// consoleRegistry is the dashboard's contract registry, or nil before the
// dashboard has started.
func (e *Extension) consoleRegistry() contract.Registry {
	if r := e.consoleReg.Load(); r != nil {
		return r.Registry
	}
	return nil
}

// RegisterContractContributor implements dashboard.ContractContributorAware,
// as authsome's own extension does: authsome's contributor, then every
// authsome plugin that contributes one (the waitlist among them), then the
// engines that live here: Warden, Chronicle and Keysmith, each wired as its
// own Forge extension wires it. Each registers all of its intents; the
// console gate decides which can be called, and keeps reg to tell reads from
// writes.
func (e *Extension) RegisterContractContributor(disp *dispatcher.Dispatcher, reg contract.Registry, wreg contract.WardenRegistry) error {
	e.consoleReg.Store(&registryRef{reg})
	if err := authcontract.Register(disp, reg, wreg, authcontract.Deps{Engine: e.engine, Brand: "Loomplane"}); err != nil {
		return fmt.Errorf("authsome contract: %w", err)
	}
	// Plugins register in the order they were added, which is the order
	// manifests that extend another need.
	var errs []error
	for _, p := range e.engine.Plugins().Plugins() {
		cc, ok := p.(plugin.ContractContributor)
		if !ok {
			continue
		}
		if err := cc.RegisterContract(disp, reg, wreg, e.engine); err != nil {
			errs = append(errs, fmt.Errorf("authsome plugin %s contract: %w", p.Name(), err))
		}
	}
	errs = append(errs, e.registerLibraryContracts(disp, reg, wreg))
	return errors.Join(errs...)
}

// registerLibraryContracts registers the contributors of the engines the
// identity extension builds. Platform roles live in Warden under Loomplane's
// app, so the app is the tenant Warden and Keysmith answer for and the app
// Chronicle reads; Chronicle reads it app-wide, every organization's events.
// Organizations' own keys and roles live in their own tenants, which the
// console has no way to pick yet.
func (e *Extension) registerLibraryContracts(disp *dispatcher.Dispatcher, reg contract.Registry, wreg contract.WardenRegistry) error {
	appID := e.cfg.Identity.AppID
	var errs []error
	if err := wardencontract.Register(disp, reg, wreg, wardencontract.Deps{Engine: e.warden, DefaultTenantID: appID}); err != nil {
		errs = append(errs, fmt.Errorf("warden contract: %w", err))
	}
	if err := chroniclecontract.Register(disp, reg, wreg, chroniclecontract.Deps{
		Store: e.auditStore, Chronicle: e.audit, DefaultAppID: appID, Logger: e.Logger(),
	}); err != nil {
		errs = append(errs, fmt.Errorf("chronicle contract: %w", err))
	}
	if err := keysmithcontract.Register(disp, reg, wreg, keysmithcontract.Deps{
		Engine: e.smith, DefaultTenantID: appID, DefaultAppID: appID, Logger: e.Logger(),
	}); err != nil {
		errs = append(errs, fmt.Errorf("keysmith contract: %w", err))
	}
	return errors.Join(errs...)
}

// consoleGate stands in front of everything under the console's base path.
// Someone who isn't signed in, or doesn't hold a platform role, gets the
// API's not-found answer for every path, so the console doesn't announce
// itself. A platform admin reaches only the dashboard routes the console
// uses, and through them only the intents consoleAllowed lets through.
//
// It's global middleware, so it runs before routing and before every route
// the dashboard registers, whenever it registers them.
func (e *Extension) consoleGate() forge.Middleware {
	return func(next forge.Handler) forge.Handler {
		return func(c forge.Context) error {
			r := c.Request()
			rest, under := consolePath(e.consoleBasePath(), r.URL.Path)
			if !under {
				return next(c)
			}
			ctx := r.Context()
			u, admin := e.consoleUser(ctx, r)
			if !admin || !consoleRequestAllowed(e.consoleRegistry(), r, rest) {
				return writeConsoleNotFound(c.Response())
			}
			c.WithContext(context.WithValue(ctx, consoleUserKey{}, u))
			return next(c)
		}
	}
}

// unlistConsole keeps the console's routes out of the API's OpenAPI and
// AsyncAPI documents, which anyone can read. The dashboard registers them
// with no option for that, so they're marked once every extension has
// started. Routes hands back each route's own metadata map, so marking it
// marks the route; the console integration test checks the document.
func (e *Extension) unlistConsole(r forge.Router) {
	for _, rt := range r.Routes() {
		if _, under := consolePath(e.consoleBasePath(), rt.Path); !under || rt.Metadata == nil {
			continue
		}
		rt.Metadata["openapi.exclude"] = true
		rt.Metadata["asyncapi.exclude"] = true
		rt.Metadata["orpc.exclude"] = true
	}
}

func (e *Extension) consoleBasePath() string {
	if base, ok := e.consoleBase.Load().(string); ok && base != "" {
		return base
	}
	return ConsoleBasePath
}

// consolePath reports whether path is base or below it, and the segments
// below it. Segments are compared the way Forge's router matches a route:
// repeated and trailing slashes are dropped, and case matters.
func consolePath(base, path string) (rest []string, under bool) {
	prefix := segments(base)
	segs := segments(path)
	if len(segs) < len(prefix) {
		return nil, false
	}
	for i, s := range prefix {
		if segs[i] != s {
			return nil, false
		}
	}
	return segs[len(prefix):], true
}

func segments(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
}

// consoleRequestAllowed decides a platform admin's request by the dashboard
// route it's for. Routes the console doesn't use, including any a later
// Forge release adds, are refused. reg is the dashboard's contract registry.
func consoleRequestAllowed(reg contract.Registry, r *http.Request, rest []string) bool {
	route := r.Method + " " + strings.Join(rest, "/")
	switch route {
	case "POST api/dashboard/v1":
		var req contract.Request
		return decodeConsoleBody(r, &req) && consoleAllowed(reg, req)
	case "POST api/dashboard/v1/stream/control":
		var msg transport.ControlMessage
		if !decodeConsoleBody(r, &msg) {
			return false
		}
		switch msg.Op {
		case "subscribe":
			return consoleAllowed(reg, contract.Request{Kind: contract.KindSubscribe, Contributor: msg.Contributor, Intent: msg.Intent})
		case "unsubscribe":
			return true
		}
		return false
	case "GET api/dashboard/v1/principal",
		"GET api/dashboard/v1/capabilities",
		"GET api/dashboard/v1/csrf",
		"GET api/dashboard/v1/stream",
		"GET export/json",
		"GET export/csv",
		"GET export/prometheus":
		return true
	}
	return false
}

// decodeConsoleBody reads the request body, decodes it into v and puts the
// same bytes back for the handler. It decodes with the same decoder and type
// the dashboard's handler uses, so both read the same request.
func decodeConsoleBody(r *http.Request, v any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, consoleMaxBody+1))
	if err != nil || len(body) > consoleMaxBody {
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return json.NewDecoder(bytes.NewReader(body)).Decode(v) == nil
}

func writeConsoleNotFound(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(consoleNotFound)) //nolint:errcheck // the client went away; nothing to do
	return nil
}
