package discovery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/clouds/oauth"
	"github.com/xraph/loomplane/internal/config"
	cloudconn "github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

// GitLabRepositoryList is the body of GET /api/v1/gitlab/repositories.
type GitLabRepositoryList struct {
	Repositories []repositories.GitLabRepository `json:"repositories"`
}

func (e *Extension) gitlabRoutes(r forge.Router, guard identity.Guard) error {
	g := r.Group("/api/v1/gitlab", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("GitLab"))
	if err := g.GET("", e.gitlabStatus,
		forge.WithName("gitlab.status"),
		forge.WithSummary("The organization's GitLab hosts and connected accounts"),
		forge.WithResponseSchema(http.StatusOK, "The hosts and connections", repositories.GitLabStatus{}),
	); err != nil {
		return err
	}
	if err := g.POST("/hosts", e.gitlabAddHost,
		forge.WithName("gitlab.addHost"),
		forge.WithSummary("Register a GitLab of the organization's own, with the OAuth application made on it"),
		forge.WithRequestSchema(repositories.AddGitLabHostInput{}),
		forge.WithResponseSchema(http.StatusCreated, "The registered host", repositories.GitLabHostView{}),
	); err != nil {
		return err
	}
	if err := g.DELETE("/hosts/:id", e.gitlabRemoveHost,
		forge.WithName("gitlab.removeHost"),
		forge.WithSummary("Remove a host the organization registered. Its accounts have to be disconnected first."),
		forge.WithNoContentResponse(),
		forge.WithEntity(forge.EntityDef{Type: "GitLabHostView", IDField: "id"}),
	); err != nil {
		return err
	}
	if err := g.POST("/sign-ins", e.gitlabStartSignIn,
		forge.WithName("gitlab.startSignIn"),
		forge.WithSummary("Start signing in to a GitLab host, and get the address to send the browser to"),
		forge.WithRequestSchema(repositories.StartGitLabSignInInput{}),
		forge.WithResponseSchema(http.StatusCreated, "Where to sign in", repositories.GitLabSignInStart{}),
	); err != nil {
		return err
	}
	if err := g.POST("/connections", e.gitlabConnect,
		forge.WithName("gitlab.connect"),
		forge.WithSummary("Connect a GitLab account, from what GitLab's redirect carried back"),
		forge.WithRequestSchema(repositories.ConnectGitLabInput{}),
		forge.WithResponseSchema(http.StatusCreated, "The connected account", repositories.GitLabConnectionView{}),
	); err != nil {
		return err
	}
	if err := g.DELETE("/connections/:id", e.gitlabDisconnect,
		forge.WithName("gitlab.disconnect"),
		forge.WithSummary("Disconnect an account, and revoke its sign-in at GitLab"),
		forge.WithNoContentResponse(),
		forge.WithEntity(forge.EntityDef{Type: "GitLabConnectionView", IDField: "id"}),
	); err != nil {
		return err
	}
	if err := g.POST("/connections/:id/webhook", e.gitlabWebhookSecret,
		forge.WithName("gitlab.webhookSecret"),
		forge.WithSummary("Create or replace a connection's webhook secret, shown once"),
		forge.WithRequestSchema(repositories.SetGitLabWebhookInput{}),
		forge.WithResponseSchema(http.StatusOK, "The secret in the two forms GitLab takes", repositories.GitLabWebhookSecret{}),
	); err != nil {
		return err
	}
	return g.GET("/repositories", e.gitlabRepositories,
		forge.WithName("gitlab.repositories"),
		forge.WithSummary("The projects the organization's connected accounts can read"),
		forge.WithResponseSchema(http.StatusOK, "Projects", GitLabRepositoryList{}),
	)
}

func (e *Extension) gitlabStatus(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	st, err := e.gitlab.Status(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, st)
}

func (e *Extension) gitlabAddHost(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in repositories.AddGitLabHostInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON with web_url, client_id and client_secret")
	}
	h, err := e.gitlab.AddHost(c.Request().Context(), p, in)
	if errors.Is(err, repositories.ErrInvalidURL) {
		return forge.NewHTTPError(http.StatusBadRequest, "web_url must be the address of a GitLab, such as https://git.example.com")
	}
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, h)
}

func (e *Extension) gitlabRemoveHost(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err := e.gitlab.RemoveHost(c.Request().Context(), p, c.Param("id")); err != nil {
		return fail(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (e *Extension) gitlabStartSignIn(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in repositories.StartGitLabSignInInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON with host_id")
	}
	start, err := e.gitlab.StartSignIn(c.Request().Context(), p, in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, start)
}

func (e *Extension) gitlabConnect(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in repositories.ConnectGitLabInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON with code and state")
	}
	conn, err := e.gitlab.Connect(c.Request().Context(), p, in)
	switch {
	case errors.Is(err, oauth.ErrRefused):
		return forge.NewHTTPError(http.StatusBadRequest, "GitLab didn't accept that sign-in code; connect again")
	case errors.Is(err, oauth.ErrAppRejected):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, "GitLab refused the sign-in application; check its application ID and secret")
	case err != nil:
		return fail(err)
	}
	return c.JSON(http.StatusCreated, conn)
}

func (e *Extension) gitlabDisconnect(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err := e.gitlab.Disconnect(c.Request().Context(), p, c.Param("id")); err != nil {
		return fail(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (e *Extension) gitlabWebhookSecret(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	// The body is optional: without one, Loomplane makes the secret.
	var in repositories.SetGitLabWebhookInput
	if c.Request().ContentLength != 0 {
		if err = c.BindJSON(&in); err != nil && !errors.Is(err, io.EOF) {
			return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON, such as {\"signing_token\": \"whsec_...\"}, or empty")
		}
	}
	created, err := e.gitlab.SetWebhookSecret(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, created)
}

func (e *Extension) gitlabRepositories(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	repos, err := e.gitlab.Repositories(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, GitLabRepositoryList{Repositories: repos})
}

// gitlabConnections builds the organizations' GitLab connections and the
// registry of hosts they read through. gitlab.com is a host of the
// operator's whenever its OAuth application is set, and so is each of
// gitlab.hosts.
func gitlabConnections(cfg config.Config, db *grove.DB, pg *pgdriver.PgDB, authz repositories.Authorizer, audit repositories.GitLabAuditor) (*repositories.GitLabConnections, *repositories.GitLabRegistry, error) {
	// GitLab's credentials get a key of their own, derived from secret_key.
	key, err := security.DeriveKey(cfg.SecretKey, repositories.GitLabVaultKeyLabel)
	if err != nil {
		return nil, nil, err
	}
	enc, err := crypto.NewEncryptor(key)
	if err != nil {
		return nil, nil, fmt.Errorf("vault encryptor: %w", err)
	}
	instance, secrets, err := gitlabInstanceHosts(cfg.GitLab)
	if err != nil {
		return nil, nil, err
	}
	registry := &repositories.GitLabRegistry{
		Instance: instance, Store: repositories.NewGitLabPgStore(pg), AllowOrganizationHosts: cfg.GitLab.AllowOrganizationHosts,
	}
	webhookBase := cfg.GitLab.WebhookURL
	if webhookBase == "" {
		webhookBase = cfg.PublicURL
	}
	conns := repositories.NewGitLabConnections(repositories.GitLabConnectionsConfig{
		Store:           registry.Store,
		Secrets:         cloudconn.NewVaultSecretsFor(secret.NewService(vaultpg.New(db), enc, secret.WithAppID(repositories.GitLabVaultAppID)), repositories.GitLabVaultAppID),
		Authz:           authz,
		Audit:           audit,
		Registry:        registry,
		InstanceSecrets: secrets,
		CallbackURL:     strings.TrimRight(cfg.PublicURL, "/") + "/integrations/gitlab/callback",
		WebhookBaseURL:  strings.TrimRight(webhookBase, "/"),
	})
	return conns, registry, nil
}

// gitlabInstanceHosts are the operator's GitLab hosts, and their OAuth
// client secrets by host. gitlab.com is one whenever its OAuth application
// is set. It is reached at gitlab.api_url, as the static gitlab.com reader
// is, but keeps the host name gitlab.com, so its sources stay
// gitlab.com/... wherever api_url points.
func gitlabInstanceHosts(cfg config.GitLabConfig) ([]repositories.GitLabHost, map[string]string, error) {
	var (
		instance []repositories.GitLabHost
		secrets  = map[string]string{}
	)
	if cfg.ClientID != "" {
		webURL := strings.TrimRight(cfg.APIURL, "/")
		if webURL == "" {
			webURL = repositories.DefaultGitLabURL
		}
		instance = append(instance, repositories.GitLabHost{
			WebURL: webURL, Host: repositories.HostGitLab, ClientID: cfg.ClientID, Instance: true,
		})
		secrets[repositories.HostGitLab] = cfg.ClientSecret
	}
	for _, h := range cfg.Hosts {
		webURL, host, err := repositories.NormalizeGitLabURL(h.WebURL, true)
		if err != nil {
			return nil, nil, fmt.Errorf("gitlab.hosts %q: %w", h.WebURL, err)
		}
		if _, taken := secrets[host]; taken {
			continue
		}
		instance = append(instance, repositories.GitLabHost{WebURL: webURL, Host: host, ClientID: h.ClientID, Instance: true})
		secrets[host] = h.ClientSecret
	}
	return instance, secrets, nil
}

// SourceReaders reads the hosts a source can be on: GitHub, Bitbucket,
// gitlab.com and whichever GitLab the registry knows.
func SourceReaders(cfg config.Config, registry *repositories.GitLabRegistry) repositories.Readers {
	github := repositories.NewGitHub(cfg.GitHub.APIURL)
	// Commit and release links point at GitHub's pages, not its API.
	github.WebURL = cfg.GitHub.WebURL
	return repositories.Readers{
		Static: map[string]repositories.Reader{
			"github.com":               github,
			repositories.HostBitbucket: repositories.NewBitbucket(cfg.Bitbucket.APIURL, cfg.Bitbucket.WebURL),
			repositories.HostGitLab:    repositories.NewGitLab(cfg.GitLab.APIURL, nil),
		},
		Lookup: registry.Reader,
	}
}

// SourceTokens mints the tokens private sources are read with, from the
// organization's GitHub installations, Bitbucket workspaces and GitLab
// accounts.
func SourceTokens(github *repositories.Connections, bitbucket *repositories.BitbucketConnections, gitlab *repositories.GitLabConnections) repositories.Tokens {
	return repositories.Tokens{
		Static: map[string]repositories.TokenSource{
			"github.com":               github,
			repositories.HostBitbucket: bitbucket,
			repositories.HostGitLab:    gitlab,
		},
		Lookup: gitlab.Lookup,
	}
}

// gitlabAuditor records GitLab hosts and connections in Chronicle, in the
// organization's stream. The details it keeps are the ones the service
// names, which never include a token or a secret.
type gitlabAuditor struct {
	chronicle *chronicle.Chronicle
	appID     string
	log       forge.Logger
}

var _ repositories.GitLabAuditor = gitlabAuditor{}

// gitlabAuditKeys are the details an event may carry.
var gitlabAuditKeys = []string{"host", "username", "host_id", "connection_id", "revoke_error", "list_error"}

func (a gitlabAuditor) Record(ctx context.Context, action string, p security.Principal, details map[string]any) {
	kind, id := "gitlab_connection", fmt.Sprint(details["connection_id"])
	if strings.HasPrefix(action, "gitlab.host.") {
		kind, id = "gitlab_host", fmt.Sprint(details["host_id"])
	}
	// A listing that left a connection out, or a replaced sign-in GitLab
	// didn't revoke, is a warning about that connection. Everything else
	// records a change that happened.
	b, outcome := a.chronicle.Info(ctx, action, kind, id), "success"
	if action == repositories.AuditGitLabListFailed || action == repositories.AuditGitLabRevokeFailed {
		b, outcome = a.chronicle.Warning(ctx, action, kind, id), "failure"
	}
	b = b.Category("integrations").AppID(a.appID).TenantID(p.OrgID).UserID(p.UserID).Outcome(outcome)
	for _, k := range gitlabAuditKeys {
		if v, ok := details[k]; ok {
			b = b.Meta(k, v)
		}
	}
	if err := b.Record(); err != nil {
		a.log.Error("record audit event", forge.F("action", action), forge.F("error", err.Error()))
	}
}
