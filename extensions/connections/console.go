package connections

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/xraph/forge"
	"github.com/xraph/forge/extensions/dashboard/contract"
	"github.com/xraph/forge/extensions/dashboard/contract/dispatcher"
	"github.com/xraph/forge/extensions/dashboard/contract/loader"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/repositories"
)

// ConsoleContributor is the contract contributor's name, and the console
// plugin's.
const ConsoleContributor = "integrations"

//go:embed console_manifest.yaml
var consoleManifest []byte

// Where an integration's app comes from, on the wire.
const (
	SourceConfig   = connections.AppFromConfig
	SourcePlatform = connections.AppFromPlatform
	SourceNone     = connections.AppFromNone
)

// Kinds of integration.
const (
	KindCloud      = "cloud"
	KindRepository = "repository"
)

// consoleService is what the console's handlers need of the connection
// service.
type consoleService interface {
	SignInApps(ctx context.Context) ([]connections.SignInApp, error)
	SetSignInApp(ctx context.Context, userID, cloud string, in connections.AppInput) (*connections.SignInApp, error)
	DeleteSignInApp(ctx context.Context, userID, cloud string) error
	CheckSignInApp(ctx context.Context, cloud string, in connections.AppCheckInput) (*connections.AppCheck, error)
	SignInHealth(ctx context.Context) (map[string]connections.SignInCounts, error)
	SignedIn(ctx context.Context, f connections.SignedInFilter) (connections.SignedInPage, error)
}

// userEmails resolves user IDs to emails, for "updated by".
type userEmails interface {
	UserEmails(ctx context.Context, ids []string) (map[string]string, error)
}

// repositoryApps keeps the repository hosts' apps saved for the platform
// (repositories.PlatformApps).
type repositoryApps interface {
	Fresh(ctx context.Context) (map[string]repositories.PlatformApp, error)
	Save(ctx context.Context, key string, a repositories.PlatformApp) error
	Remove(ctx context.Context, key string) error
}

// repositoryAuditor records a change to a repository host's app.
type repositoryAuditor interface {
	RecordRepositoryApp(ctx context.Context, action, userID, key, clientID string)
}

// console serves the integrations contributor.
type console struct {
	cfg    config.Config
	svc    consoleService
	users  userEmails
	logger forge.Logger
	// callbackURL is where every cloud sends people back after signing in.
	callbackURL string
	// repos keeps the repository hosts' apps; nil leaves them read-only.
	repos repositoryApps
	audit repositoryAuditor
	// http checks repository apps with their hosts; nil is the default.
	http *http.Client
	now  func() time.Time
	// readFile reads configuration's GitHub App key file.
	readFile func(string) ([]byte, error)
}

// RegisterContractContributor implements dashboard.ContractContributorAware:
// the instance console's Integrations pages.
func (e *Extension) RegisterContractContributor(d *dispatcher.Dispatcher, reg contract.Registry, wreg contract.WardenRegistry) error {
	if e.svc == nil {
		return errors.New("integrations console: the connection service isn't built yet")
	}
	c := &console{
		cfg: e.cfg, svc: e.svc, users: e.store, logger: e.Logger(), callbackURL: e.svc.CallbackURL,
		audit: e.audit, now: time.Now, readFile: os.ReadFile,
	}
	// The discovery extension keeps the repository hosts' apps, and the
	// console saves through its cache so a save is live there at once.
	// Without it they're shown read-only.
	if e.container != nil {
		if apps, err := forge.InjectType[*repositories.PlatformApps](e.container); err == nil {
			c.repos = apps
		} else {
			e.Logger().Warn("the console can't change repository apps", forge.F("error", err.Error()))
		}
	}
	return registerConsole(d, reg, wreg, c)
}

func registerConsole(d *dispatcher.Dispatcher, reg contract.Registry, wreg contract.WardenRegistry, c *console) error {
	m, err := loader.Load(bytes.NewReader(consoleManifest), "connections/console_manifest.yaml")
	if err != nil {
		return fmt.Errorf("integrations console: load manifest: %w", err)
	}
	if err := loader.Validate(m, wreg); err != nil {
		return fmt.Errorf("integrations console: validate manifest: %w", err)
	}
	if err := reg.Register(m); err != nil {
		return fmt.Errorf("integrations console: register manifest: %w", err)
	}
	n := ConsoleContributor
	for _, r := range []error{
		dispatcher.RegisterQuery(d, n, "oauth.integrations", 1, c.integrations),
		dispatcher.RegisterQuery(d, n, "oauth.integration", 1, c.integration),
		dispatcher.RegisterQuery(d, n, "connections.list", 1, c.connectionsList),
		dispatcher.RegisterCommand(d, n, "oauth.save", 1, c.save),
		dispatcher.RegisterCommand(d, n, "oauth.remove", 1, c.remove),
		dispatcher.RegisterCommand(d, n, "oauth.check", 1, c.check),
	} {
		if r != nil {
			return fmt.Errorf("integrations console: register intent: %w", r)
		}
	}
	return nil
}

// ---- wire types ----

// ConsoleIntegration is one OAuth integration as the console shows it: never
// a secret, only the client ID.
type ConsoleIntegration struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Editable says the console can set its app: when configuration
	// doesn't. A self-managed GitLab from configuration never is.
	Editable bool   `json:"editable"`
	Source   string `json:"source"`
	ClientID string `json:"clientId,omitempty"`
	Slug     string `json:"slug,omitempty"`
	// AppID is the GitHub App's ID.
	AppID int64 `json:"appId,omitempty"`
	// SavedSecrets names the secret fields that hold a value, so a form can
	// say "leave blank to keep it". Never the values.
	SavedSecrets []string   `json:"savedSecrets,omitempty"`
	UpdatedAt    *time.Time `json:"updatedAt,omitempty"`
	UpdatedBy    string     `json:"updatedBy,omitempty"`
	// Problem is why the cloud last refused the app, until it's saved again
	// or a sign-in with it works.
	Problem     string `json:"problem,omitempty"`
	CallbackURL string `json:"callbackUrl"`
	// OutlivesApp says its connections keep working when the app changes or
	// goes (Vercel's).
	OutlivesApp bool `json:"outlivesApp,omitempty"`
	// Connections counts its signed-in connections by health. Nil for a
	// repository host, or when health can't be read.
	Connections *connections.SignInCounts `json:"connections,omitempty"`
	// Organizations is how many organizations have a working sign-in with
	// the current app.
	Organizations int             `json:"organizations"`
	Provider      ConsoleProvider `json:"provider"`
}

// IntegrationsResponse is oauth.integrations.
type IntegrationsResponse struct {
	// AppURL is the product app, where people sign in to a connection again.
	AppURL          string                   `json:"appUrl"`
	HealthAvailable bool                     `json:"healthAvailable"`
	Integrations    []ConsoleIntegration     `json:"integrations"`
	Totals          connections.SignInCounts `json:"totals"`
}

// IntegrationDetail is oauth.integration.
type IntegrationDetail struct {
	ConsoleIntegration
	AppURL          string `json:"appUrl"`
	HealthAvailable bool   `json:"healthAvailable"`
	// EnvVars set it in the deployment instead; ConfigKey is its block in
	// the config file. Either wins over the console.
	EnvVars   []string `json:"envVars"`
	ConfigKey string   `json:"configKey"`
}

type keyInput struct {
	Key string `json:"key"`
}

// SaveInput is oauth.save. A blank secret keeps the saved one while what it
// belongs to is unchanged: the client secret with its client ID, the
// GitHub App's private key with its App ID. AppID, PrivateKey and
// WebhookSecret are the GitHub App's.
type SaveInput struct {
	Key                string `json:"key"`
	ClientID           string `json:"clientId"`
	ClientSecret       string `json:"clientSecret"`
	Slug               string `json:"slug"`
	AppID              string `json:"appId"`
	PrivateKey         string `json:"privateKey"`
	WebhookSecret      string `json:"webhookSecret"`
	ClearWebhookSecret bool   `json:"clearWebhookSecret"`
}

// CheckInput is oauth.check. With no client ID it checks the app in use
// now. AppID and PrivateKey are the GitHub App's, checked with GitHub too.
type CheckInput struct {
	Key          string `json:"key"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	AppID        string `json:"appId"`
	PrivateKey   string `json:"privateKey"`
}

type ackResponse struct {
	Key string `json:"key"`
}

// ---- handlers ----

func (c *console) integrations(ctx context.Context, _ struct{}, p contract.Principal) (IntegrationsResponse, error) {
	if err := signedIn(p); err != nil {
		return IntegrationsResponse{}, err
	}
	list, healthOK, err := c.list(ctx)
	if err != nil {
		return IntegrationsResponse{}, err
	}
	out := IntegrationsResponse{AppURL: c.appURL(), HealthAvailable: healthOK, Integrations: list}
	for _, in := range list {
		if in.Connections != nil {
			out.Totals.Merge(*in.Connections)
		}
	}
	return out, nil
}

func (c *console) integration(ctx context.Context, in keyInput, p contract.Principal) (IntegrationDetail, error) {
	if err := signedIn(p); err != nil {
		return IntegrationDetail{}, err
	}
	list, healthOK, err := c.list(ctx)
	if err != nil {
		return IntegrationDetail{}, err
	}
	key := strings.TrimSpace(in.Key)
	for _, it := range list {
		if it.Key == key {
			env, cfgKey := c.deploymentSettings(it)
			return IntegrationDetail{
				ConsoleIntegration: it, AppURL: c.appURL(), HealthAvailable: healthOK,
				EnvVars: env, ConfigKey: cfgKey,
			}, nil
		}
	}
	return IntegrationDetail{}, notFound(key)
}

func (c *console) connectionsList(ctx context.Context, in connections.SignedInFilter, p contract.Principal) (connections.SignedInPage, error) {
	if err := signedIn(p); err != nil {
		return connections.SignedInPage{}, err
	}
	out, err := c.svc.SignedIn(ctx, in)
	if err != nil {
		return connections.SignedInPage{}, c.fail("list signed-in connections", err)
	}
	return out, nil
}

func (c *console) save(ctx context.Context, in SaveInput, p contract.Principal) (ackResponse, error) {
	if err := signedIn(p); err != nil {
		return ackResponse{}, err
	}
	if c.isRepository(in.Key) {
		return c.saveRepository(ctx, in, p)
	}
	cloud, err := c.editableCloud(in.Key)
	if err != nil {
		return ackResponse{}, err
	}
	_, err = c.svc.SetSignInApp(ctx, p.User.Subject, cloud, connections.AppInput{
		ClientID: in.ClientID, ClientSecret: in.ClientSecret, Slug: in.Slug,
	})
	if err != nil {
		return ackResponse{}, c.fail("save the sign-in app", err)
	}
	return ackResponse{Key: cloud}, nil
}

func (c *console) remove(ctx context.Context, in keyInput, p contract.Principal) (ackResponse, error) {
	if err := signedIn(p); err != nil {
		return ackResponse{}, err
	}
	if c.isRepository(in.Key) {
		return c.removeRepository(ctx, strings.TrimSpace(in.Key), p)
	}
	cloud, err := c.editableCloud(in.Key)
	if err != nil {
		return ackResponse{}, err
	}
	if err := c.svc.DeleteSignInApp(ctx, p.User.Subject, cloud); err != nil {
		return ackResponse{}, c.fail("remove the sign-in app", err)
	}
	return ackResponse{Key: cloud}, nil
}

func (c *console) check(ctx context.Context, in CheckInput, p contract.Principal) (connections.AppCheck, error) {
	if err := signedIn(p); err != nil {
		return connections.AppCheck{}, err
	}
	cloud := strings.TrimSpace(in.Key)
	if _, ok := cloudProviders[cloud]; !ok {
		if c.isRepository(cloud) {
			return c.checkRepository(ctx, in)
		}
		return connections.AppCheck{}, notFound(cloud)
	}
	res, err := c.svc.CheckSignInApp(ctx, cloud, connections.AppCheckInput{ClientID: in.ClientID, ClientSecret: in.ClientSecret})
	if err != nil {
		return connections.AppCheck{}, c.fail("check the sign-in app", err)
	}
	return *res, nil
}

// ---- building the list ----

// list is every integration: the clouds first, then the repository hosts.
// healthOK is false when connection health couldn't be read.
func (c *console) list(ctx context.Context) (out []ConsoleIntegration, healthOK bool, err error) {
	apps, err := c.svc.SignInApps(ctx)
	if err != nil {
		return nil, false, c.fail("read the sign-in apps", err)
	}
	health, herr := c.svc.SignInHealth(ctx)
	healthOK = herr == nil
	if herr != nil && !errors.Is(herr, connections.ErrNoHealth) {
		c.logger.Warn("integrations console: read connection health", forge.F("error", herr.Error()))
	}
	repos, err := c.repositories(ctx)
	if err != nil {
		return nil, false, err
	}
	ids := make([]string, 0, len(apps)+len(repos))
	for _, a := range apps {
		ids = append(ids, a.UpdatedBy)
	}
	for _, r := range repos {
		ids = append(ids, r.UpdatedBy)
	}
	emails := c.emails(ctx, ids)
	out = []ConsoleIntegration{}
	for _, a := range apps {
		prov, ok := cloudProviders[a.Cloud]
		if !ok {
			prov = ConsoleProvider{Name: connections.CloudTitle(a.Cloud)}
		}
		it := ConsoleIntegration{
			Key: a.Cloud, Name: connections.CloudTitle(a.Cloud), Kind: KindCloud,
			Editable: a.Source != SourceConfig, Source: a.Source, ClientID: a.ClientID, Slug: a.Slug,
			UpdatedAt: a.UpdatedAt, UpdatedBy: firstNonEmpty(emails[a.UpdatedBy], a.UpdatedBy), Problem: a.Problem,
			CallbackURL: c.callbackURL, OutlivesApp: a.Cloud == connections.Vercel,
			Organizations: a.Organizations, Provider: prov,
		}
		if a.Source == SourcePlatform {
			// A cloud's app is saved with its secret, always.
			it.SavedSecrets = []string{secretClient}
		}
		if healthOK {
			counts := health[a.Cloud]
			it.Connections = &counts
		}
		out = append(out, it)
	}
	for i := range repos {
		repos[i].UpdatedBy = firstNonEmpty(emails[repos[i].UpdatedBy], repos[i].UpdatedBy)
	}
	return append(out, repos...), healthOK, nil
}

// deploymentSettings are the environment variables and config block that
// set an integration in the deployment.
func (c *console) deploymentSettings(it ConsoleIntegration) (env []string, configKey string) {
	switch {
	case it.Kind == KindCloud:
		prefix := "LOOMPLANE_" + strings.ToUpper(it.Key) + "_OAUTH_"
		env = []string{prefix + "CLIENT_ID", prefix + "CLIENT_SECRET"}
		if it.Key == connections.Vercel {
			env = append(env, prefix+"SLUG")
		}
		return env, "clouds.oauth." + it.Key
	case it.Key == keyGitHub:
		return []string{
			"LOOMPLANE_GITHUB_APP_ID", "LOOMPLANE_GITHUB_APP_SLUG", "LOOMPLANE_GITHUB_PRIVATE_KEY",
			"LOOMPLANE_GITHUB_CLIENT_ID", "LOOMPLANE_GITHUB_CLIENT_SECRET", "LOOMPLANE_GITHUB_WEBHOOK_SECRET",
		}, "github"
	case it.Key == keyGitLab:
		return []string{"LOOMPLANE_GITLAB_CLIENT_ID", "LOOMPLANE_GITLAB_CLIENT_SECRET"}, "gitlab"
	case it.Key == keyBitbucket:
		return []string{"LOOMPLANE_BITBUCKET_CLIENT_ID", "LOOMPLANE_BITBUCKET_CLIENT_SECRET"}, "bitbucket"
	case strings.HasPrefix(it.Key, gitLabHostPrefix):
		// A list in the config file; the environment can't hold it.
		return []string{}, "gitlab.hosts"
	}
	return []string{}, ""
}

// editableCloud is key's cloud, when the console can set its app.
func (c *console) editableCloud(key string) (string, error) {
	key = strings.TrimSpace(key)
	if _, ok := cloudProviders[key]; ok {
		return key, nil
	}
	return "", notFound(key)
}

// emails maps the users who last changed an app, by ID, to their emails. A
// lookup that fails leaves the IDs.
func (c *console) emails(ctx context.Context, all []string) map[string]string {
	var ids []string
	for _, id := range all {
		if id != "" {
			ids = append(ids, id)
		}
	}
	if c.users == nil || len(ids) == 0 {
		return map[string]string{}
	}
	got, err := c.users.UserEmails(ctx, ids)
	if err != nil {
		c.logger.Warn("integrations console: look up who changed the sign-in apps", forge.F("error", err.Error()))
		return map[string]string{}
	}
	return got
}

func (c *console) appURL() string { return strings.TrimRight(c.cfg.PublicURL, "/") }

// ---- errors ----

func signedIn(p contract.Principal) error {
	if p.User == nil || p.User.Subject == "" {
		return &contract.Error{Code: contract.CodeUnauthenticated, Message: "Sign in to manage integrations."}
	}
	return nil
}

func notFound(key string) error {
	return &contract.Error{Code: contract.CodeNotFound, Message: fmt.Sprintf("No integration %q signs in through OAuth here.", key)}
}

func badField(field, msg string) error {
	return &contract.Error{Code: contract.CodeBadRequest, Message: msg, Details: map[string]any{"field": field}}
}

// fail maps a service error to the console's. A failure the operator can't
// fix from the page goes to the server log, never on the wire.
func (c *console) fail(what string, err error) error {
	switch {
	case errors.Is(err, connections.ErrAppLocked):
		return &contract.Error{Code: contract.CodeConflict,
			Message: "This app is set in Loomplane's configuration, which wins over the console. Change it there, or remove it there to manage it here."}
	case errors.Is(err, connections.ErrInvalid):
		return &contract.Error{Code: contract.CodeBadRequest, Message: sentence(strings.TrimPrefix(err.Error(), connections.ErrInvalid.Error()+": "))}
	case errors.Is(err, connections.ErrNotFound):
		return &contract.Error{Code: contract.CodeNotFound, Message: "There's no app saved here for it."}
	case errors.Is(err, connections.ErrNoHealth):
		return &contract.Error{Code: contract.CodeUnavailable, Message: "Connection health can't be read on this server."}
	}
	c.logger.Error("integrations console: "+what, forge.F("error", err.Error()))
	return &contract.Error{Code: contract.CodeInternal, Message: "Couldn't " + what + ". The server log has the cause."}
}

// sentence capitalizes s and ends it with a full stop.
func sentence(s string) string {
	if s == "" {
		return s
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
