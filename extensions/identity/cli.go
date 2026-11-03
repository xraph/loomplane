package identity

// loomctl signs in with the OAuth 2.0 device authorization grant (RFC 8628),
// which authsome's oauth2provider plugin serves. loomctl asks for a code,
// you open the dashboard's /device page and approve it there, signed in as
// yourself, and loomctl collects a token for your account. No key is copied
// anywhere.
//
// authsome parks a token issued to an OAuth client until a route admits its
// scope, so the guard admits loomctl's scope and nothing else. The token
// starts with no organization; loomctl lists yours and picks one through the
// routes here, the way the dashboard's switcher does.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	authid "github.com/xraph/authsome/id"
	authmw "github.com/xraph/authsome/middleware"
	"github.com/xraph/authsome/plugins/oauth2provider"
	"github.com/xraph/forge"
	"github.com/xraph/grove"

	"github.com/xraph/loomplane/internal/security"
)

const (
	// CLIClientID is the OAuth client loomctl signs in as. It's public: a
	// command-line tool can't keep a secret, so the device code and your
	// approval in the browser are what tie a token to you.
	CLIClientID = "loomctl"
	// CLIScope is the one scope loomctl's tokens hold. Loomplane's API
	// admits an OAuth token only when it holds this.
	CLIScope = "loomplane"
	// DevicePath is the dashboard page that approves a device code.
	DevicePath = "/device"

	deviceCodeGrant = "urn:ietf:params:oauth:grant-type:device_code"
)

// oauthProvider is authsome's OAuth provider, issuing at Loomplane's public
// address. Device codes are approved on the dashboard's page rather than
// authsome's own, so they read like the rest of Loomplane.
func (e *Extension) oauthProvider(db *grove.DB) *oauth2provider.Plugin {
	public := strings.TrimRight(e.cfg.PublicURL, "/")
	p := oauth2provider.New(oauth2provider.Config{
		Issuer:          public + e.cfg.Identity.BasePath,
		VerificationURI: public + DevicePath,
	})
	// Given explicitly: the plugin only finds Postgres through the engine's
	// grove handle, which Loomplane's engine doesn't have, and would fall
	// back to memory, one store per replica.
	e.oauthClients = oauth2provider.NewPostgresStore(db)
	p.SetOAuth2Store(e.oauthClients)
	return p
}

// seedCLIClient registers loomctl's client on the first start. It's looked
// up first, so restarts change nothing; prepare runs it under the startup
// lock.
func (e *Extension) seedCLIClient(ctx context.Context) error {
	if _, err := e.oauthClients.GetClient(ctx, CLIClientID); err == nil {
		return nil
	} else if !errors.Is(err, oauth2provider.ErrClientNotFound) {
		return fmt.Errorf("look up the loomctl OAuth client: %w", err)
	}
	appID, err := authid.ParseAppID(e.cfg.Identity.AppID)
	if err != nil {
		return fmt.Errorf("identity.app_id: %w", err)
	}
	now := time.Now().UTC()
	if err := e.oauthClients.CreateClient(ctx, &oauth2provider.OAuth2Client{
		ID: authid.NewOAuth2ClientID(), AppID: appID, Name: "loomctl", ClientID: CLIClientID,
		Scopes:     []string{CLIScope},
		GrantTypes: []string{deviceCodeGrant, "refresh_token"},
		Public:     true, TokenEndpointAuthMethod: "none",
		// Loomplane's own tool: the device page is the consent.
		FirstParty: true,
		CreatedAt:  now, UpdatedAt: now,
	}); err != nil {
		return fmt.Errorf("seed the loomctl OAuth client: %w", err)
	}
	return nil
}

// admitCLI lets a loomctl token through to Loomplane's API. Any other OAuth
// token stays parked, so the request goes on as if it had none.
func admitCLI() forge.Middleware {
	return authmw.RequireScope(CLIScope)
}

// CLIOrganization is an organization you can point loomctl at.
type CLIOrganization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// CLIOrganizationList is the organizations you belong to, and which one the
// token acts in, if any yet.
type CLIOrganizationList struct {
	Organizations []CLIOrganization `json:"organizations"`
	Active        string            `json:"active,omitempty"`
}

// UseOrganizationInput names the organization the token should act in.
type UseOrganizationInput struct {
	OrgID string `json:"org_id"`
}

// cliRoutes serve a session before it has an organization, which the guard
// would refuse.
func (e *Extension) cliRoutes(r forge.Router) error {
	mw := append(e.sessionMiddleware(), admitCLI())
	g := r.Group("/api/v1/cli", forge.WithGroupMiddleware(mw...), forge.WithGroupTags("Identity"))
	if err := g.GET("/organizations", e.cliOrganizations,
		forge.WithName("cli.organizations.list"),
		forge.WithSummary("The organizations you belong to, and the one this session acts in"),
		forge.WithResponseSchema(http.StatusOK, "Organizations", CLIOrganizationList{}),
	); err != nil {
		return err
	}
	return g.PUT("/organization", e.cliUseOrganization,
		forge.WithName("cli.organization.use"),
		forge.WithSummary("Make one of your organizations the one this session acts in"),
		forge.WithRequestSchema(UseOrganizationInput{}),
		forge.WithResponseSchema(http.StatusOK, "Organizations", CLIOrganizationList{}),
	)
}

// cliUser is the signed-in user, held to the same email rule as the guard.
func (e *Extension) cliUser(ctx context.Context) (authid.UserID, error) {
	uid, ok := authmw.UserIDFrom(ctx)
	if !ok || uid.IsNil() {
		return uid, forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	if e.cfg.Identity.RequireEmailVerification {
		if u, ok := authmw.UserFrom(ctx); !ok || u == nil || !u.EmailVerified {
			return uid, forge.NewHTTPError(http.StatusForbidden, security.ErrEmailNotVerified.Error())
		}
	}
	return uid, nil
}

func (e *Extension) cliOrganizations(c forge.Context) error {
	ctx := c.Request().Context()
	uid, err := e.cliUser(ctx)
	if err != nil {
		return err
	}
	out, err := e.cliOrganizationList(ctx, uid)
	if err != nil {
		return err
	}
	if o, ok := authmw.OrgIDFrom(ctx); ok && !o.IsNil() {
		out.Active = o.String()
	}
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) cliUseOrganization(c forge.Context) error {
	ctx := c.Request().Context()
	uid, err := e.cliUser(ctx)
	if err != nil {
		return err
	}
	var in UseOrganizationInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must name an org_id")
	}
	orgID, err := authid.ParseOrgID(strings.TrimSpace(in.OrgID))
	if err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "that isn't an organization ID")
	}
	sid, ok := authmw.SessionIDFrom(ctx)
	if !ok {
		return forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	out, err := e.cliOrganizationList(ctx, uid)
	if err != nil {
		return err
	}
	// Checked here as well as by authsome, so a stranger's organization is
	// a plain 403 rather than authsome's wrapped error.
	member := false
	for _, o := range out.Organizations {
		member = member || o.ID == orgID.String()
	}
	if !member {
		return forge.NewHTTPError(http.StatusForbidden, "you aren't a member of that organization")
	}
	if _, err := e.engine.SwitchActiveOrg(ctx, sid, orgID); err != nil {
		return fmt.Errorf("switch the session's organization: %w", err)
	}
	out.Active = orgID.String()
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) cliOrganizationList(ctx context.Context, uid authid.UserID) (CLIOrganizationList, error) {
	orgs, err := e.orgs.ListUserOrganizations(ctx, uid)
	if err != nil {
		return CLIOrganizationList{}, fmt.Errorf("list your organizations: %w", err)
	}
	out := CLIOrganizationList{Organizations: make([]CLIOrganization, 0, len(orgs))}
	for _, o := range orgs {
		out.Organizations = append(out.Organizations, CLIOrganization{ID: o.ID.String(), Name: o.Name, Slug: o.Slug})
	}
	return out, nil
}
