// Package config loads Loomplane's settings.
//
// Settings come from config.yaml in the config directory, then
// config.local.yaml beside it (git ignores it), with LOOMPLANE_ environment
// variables wired into the YAML as "${LOOMPLANE_SOMETHING:-}". Loading goes
// through confy, the same loader Forge uses, but Loomplane reads the result
// into a typed Config itself and hands each extension its part. Forge gets
// an environment prefix nobody sets, so the two never read the same
// variables (see internal/platform).
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/confy"
)

const (
	configName      = "config.yaml"
	localConfigName = "config.local.yaml"
	envPrefix       = "LOOMPLANE"

	// secretKeyBytes is the length of secret_key once decoded.
	secretKeyBytes = 32

	// devSecretKey is used when nothing sets secret_key. Production refuses it.
	devSecretKey = "bG9vbXBsYW5lLWRldmVsb3BtZW50LXNlY3JldC1rZXk=" //nolint:gosec // a public development default, refused in production
)

// Environments Loomplane knows.
const (
	EnvDevelopment = "development"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// Config is everything Loomplane reads at start.
type Config struct {
	// Environment is development, staging or production.
	Environment string
	// HTTPAddr is where the API listens, as host:port or :port.
	HTTPAddr string
	// PublicURL is the address browsers and the CLI reach Loomplane at.
	PublicURL string
	// SecretKey is 32 random bytes, base64. Keys authsome needs are derived
	// from it, so it never leaves the process as is.
	SecretKey string

	Database      DatabaseConfig
	Identity      IdentityConfig
	Console       ConsoleConfig
	Notifications NotificationsConfig
	Dashboard     DashboardConfig
	Orchestration OrchestrationConfig
	GitHub        GitHubConfig
	Analysis      AnalysisConfig
	Planning      PlanningConfig
	Clouds        CloudsConfig
	Storage       StorageConfig
	Drift         DriftConfig
	Worker        WorkerConfig
	Federation    FederationConfig
	AI            AIConfig
	Providers     ProvidersConfig
	DBGateway     DBGatewayConfig
}

// DBGatewayConfig is the control plane's side of the database gateway.
type DBGatewayConfig struct {
	// SigningKeyFile is the private JSON file holding the Ed25519 key that
	// signs gateway snapshots, as a canonical absolute path. Empty leaves
	// the gateway endpoints answering 503.
	SigningKeyFile string
}

// FederationConfig keeps management and the agent listener independently configured.
type FederationConfig struct {
	Enabled  bool
	Runtime  FederationRuntimeConfig
	Identity FederationIdentityConfig
	Launch   FederationLaunchConfig
}

// ProvidersConfig adds deployment targets Loomplane doesn't ship with.
type ProvidersConfig struct {
	// Plugins are providers that run as their own services and speak the
	// protocol in pkg/provider/sdk.
	Plugins []PluginConfig
	// Paused are targets, by name, whose plans can be made and reviewed
	// but not carried out: during an incident on a cloud, say.
	Paused []string
}

// PluginConfig is one provider plugin.
type PluginConfig struct {
	// URL is where it listens, such as http://fly-provider:9000.
	URL string
	// Token is the token it was started with. Loomplane sends it with
	// every request, and the provider refuses anything without it.
	Token string
}

// WorkerConfig is loomplane-worker's own process settings.
type WorkerConfig struct {
	// HTTPAddr is where the worker serves its health checks. It serves
	// nothing else.
	HTTPAddr string
}

// GitHubConfig is how Loomplane reaches GitHub.
type GitHubConfig struct {
	// APIURL is GitHub's REST API: https://api.github.com, or your GitHub
	// Enterprise Server's /api/v3.
	APIURL string
	// WebURL is GitHub itself, where people install the App and sign in:
	// https://github.com, or your GitHub Enterprise Server.
	WebURL string

	// The GitHub App that reads private repositories. Leave all of these
	// empty to read public repositories only; set all of them otherwise.
	// docs/github-app.md says how to create the App.
	AppID   int64
	AppSlug string
	// PrivateKey is the App's PEM private key itself, or PrivateKeyFile
	// the path to it. Set it from the environment, never in a file you
	// commit.
	PrivateKey     string
	PrivateKeyFile string
	// ClientID and ClientSecret are the App's OAuth credentials. Loomplane
	// uses them once per connection, to check that whoever installed the
	// App can see the installation.
	ClientID     string
	ClientSecret string
	// WebhookSecret is the secret GitHub signs webhook deliveries with.
	// Set it, and the same secret on the App's webhook, to re-analyze a
	// project when its default branch moves. Empty turns webhooks off.
	WebhookSecret string
}

// AppConfigured reports whether a GitHub App is set up.
func (g GitHubConfig) AppConfigured() bool {
	return g.AppID != 0
}

// appSettingsSet counts the App settings that are set.
func (g GitHubConfig) appSettingsSet() int {
	n := 0
	for _, set := range []bool{
		g.AppID != 0, g.AppSlug != "", g.PrivateKey != "" || g.PrivateKeyFile != "", g.ClientID != "", g.ClientSecret != "",
	} {
		if set {
			n++
		}
	}
	return n
}

// AnalysisConfig tunes repository analysis, which loomplane-worker runs.
type AnalysisConfig struct {
	// WorkDir is where checkouts are extracted while they're analyzed.
	// Empty means the system's temporary directory.
	WorkDir string
	// Concurrency is how many analyses one worker runs at once.
	Concurrency int
}

// DatabaseConfig is the Postgres connection.
type DatabaseConfig struct {
	// URL is a postgres:// DSN.
	URL string
	// PoolSize caps open connections. 0 keeps the driver's default.
	PoolSize int
}

// IdentityConfig configures authsome, which Loomplane runs in-process.
type IdentityConfig struct {
	// BasePath is where authsome's API is mounted.
	BasePath string
	// AppID is the authsome app that holds Loomplane's users. It's created
	// on first start.
	AppID string
	// PublishableKey is the authsome app's public key.
	PublishableKey string
	// RequireEmailVerification makes sign-in wait for a verified email.
	RequireEmailVerification bool
	// SessionCookie names the HttpOnly cookie the dashboard's session is
	// kept in. Cookies are shared by every port on a host, so two authsome
	// apps on one host need different names, or each replaces the other's.
	SessionCookie string
	// Waitlist turns the sign-up waitlist on or off. Nil leaves it to the
	// instance's stored setting.
	Waitlist *bool
}

// ConsoleConfig configures the hidden Forge console.
type ConsoleConfig struct {
	// Path is where the console is mounted. It's one segment that starts
	// with __, so it can't collide with the dashboard's routes, /api/,
	// /authsome/, /_/, /.well-known/ or /catalog-admin/.
	Path string
}

// DashboardConfig is the web dashboard Loomplane serves.
type DashboardConfig struct {
	// Dir holds the built dashboard (apps/dashboard/dist). When it doesn't
	// exist the API still runs, it just serves no dashboard. In development
	// the Vite dev server proxies the API instead, so the browser only ever
	// talks to one origin and Loomplane needs no CORS.
	Dir string
}

// OrchestrationConfig tunes Dispatch, Loomplane's job engine.
type OrchestrationConfig struct {
	// Concurrency is how many jobs one process runs at once.
	Concurrency int
	// PollInterval is how often idle workers look for new jobs.
	PollInterval time.Duration
}

// PlanningConfig tunes deployment planning.
type PlanningConfig struct {
	// ApprovalWindow is how long a plan waits for someone to approve or
	// reject it. After that it expires, and you plan again.
	ApprovalWindow time.Duration
	// EndpointDeniedCIDRs are ranges an endpoint check from Loomplane's
	// own network never connects to, at any port, besides its own host
	// and the services it depends on. A bare address is one host.
	EndpointDeniedCIDRs []netip.Prefix
}

// DriftConfig schedules the drift check.
type DriftConfig struct {
	// Interval is how often everything deployed is compared with what its
	// provider has live. Zero turns the check off.
	Interval time.Duration
}

// StorageConfig is where artifacts go, such as the source of each
// deployment.
type StorageConfig struct {
	// ArtifactBucket overrides the existing artifact bucket name.
	ArtifactBucket string
	// BackupURL selects an independent backup backend; empty uses URL.
	BackupURL string
	// BackupBucket separates recovery objects from source artifacts.
	BackupBucket string
	// URL names the storage: file:///absolute/path for a directory. Empty
	// means .loomplane/storage under the working directory, for
	// development.
	URL string
}

// CloudsConfig configures enrollment callbacks and provider API test overrides.
// Leave provider API overrides empty for live deployments.
type CloudsConfig struct {
	// EnrollmentURL is the reachable HTTPS callback base for managed servers.
	// Empty uses PublicURL; local addresses cannot provision remote servers.
	EnrollmentURL string
	// GoogleEndpoint replaces every Google Cloud API host.
	GoogleEndpoint string
	// VercelEndpoint replaces Vercel's API.
	VercelEndpoint string
	// AWSEndpoint replaces every AWS endpoint.
	AWSEndpoint string
	// RenderEndpoint replaces Render's API.
	RenderEndpoint string
	// DigitalOceanEndpoint replaces DigitalOcean's API.
	DigitalOceanEndpoint string
	// AzureEndpoint replaces Microsoft Entra ID's sign-in and Azure
	// Resource Manager.
	AzureEndpoint string
	// CloudflareEndpoint replaces Cloudflare's API.
	CloudflareEndpoint string
	// CloudflareRegistrarSandbox sends registrar calls to Cloudflare's
	// sandbox, which never registers or charges for a real domain.
	CloudflareRegistrarSandbox bool
	// ACMEDirectory replaces the ACME directory certificates are ordered
	// from. Empty means Let's Encrypt's production directory.
	ACMEDirectory string
	// KubernetesInCluster lets organizations connect the Kubernetes cluster
	// Loomplane runs in, as Loomplane's own service account. Every
	// organization on the instance shares that account, so it's off unless
	// the operator turns it on, and needs the pod's token mounted.
	KubernetesInCluster bool
	// OAuth holds the sign-in app Loomplane registered with each cloud,
	// keyed by connection cloud such as "digitalocean". A cloud appears
	// only when both its client ID and secret are set.
	OAuth map[string]OAuthApp
}

// OAuthApp is Loomplane's OAuth 2 app at one cloud.
type OAuthApp struct {
	ClientID     string
	ClientSecret string
	// Slug names the app in the cloud's own address, which Vercel's
	// install page needs. Only Vercel sets it.
	Slug string
}

// oauthClouds are the clouds that can connect by signing in.
var oauthClouds = []string{"digitalocean", "vercel", "gcp", "azure"}

// Bounds on the approval window: long enough to reach a person, short
// enough that an approved plan still describes the world.
const (
	minApprovalWindow = time.Minute
	maxApprovalWindow = 90 * 24 * time.Hour
	approvalWindow    = 7 * 24 * time.Hour
)

// consolePath is a console mount path (see ConsoleConfig.Path for why it
// starts with __).
var consolePath = regexp.MustCompile(`^/__[a-z0-9][a-z0-9-]{2,62}$`)

// cookieName is a cookie name as RFC 6265 allows it: a token.
var cookieName = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")

// Defaults are the settings used when nothing overrides them.
func Defaults() Config {
	return Config{
		Environment: EnvDevelopment,
		HTTPAddr:    ":8780",
		PublicURL:   "http://localhost:8780",
		SecretKey:   devSecretKey,
		Database:    DatabaseConfig{PoolSize: 16},
		Identity: IdentityConfig{
			BasePath:       "/authsome",
			AppID:          "aapp_01jm0000000000000000000000",
			PublishableKey: "pk_test_loomplane_dev",
			SessionCookie:  "loomplane_session",
		},
		Console:       ConsoleConfig{Path: "/__console"},
		Notifications: NotificationsConfig{SMTPPort: 1025},
		Dashboard:     DashboardConfig{Dir: "apps/dashboard/dist"},
		Orchestration: OrchestrationConfig{
			Concurrency:  4,
			PollInterval: time.Second,
		},
		GitHub:   GitHubConfig{APIURL: "https://api.github.com", WebURL: "https://github.com"},
		Analysis: AnalysisConfig{Concurrency: 2},
		Planning: PlanningConfig{ApprovalWindow: approvalWindow},
		Drift:    DriftConfig{Interval: 30 * time.Minute},
		Worker:   WorkerConfig{HTTPAddr: ":8781"},
		AI:       AIConfig{Models: AIModelsConfig{HuggingFaceURL: DefaultHuggingFaceURL}},
	}
}

// Discovery is how Loomplane finds its config files in dir.
func Discovery(dir string) confy.AutoDiscoveryConfig {
	return confy.AutoDiscoveryConfig{
		AppName:          "loomplane",
		SearchPaths:      []string{dir},
		ConfigNames:      []string{configName},
		LocalConfigNames: []string{localConfigName},
		MaxDepth:         1,
		RequireBase:      true,
		EnableEnvSource:  true,
		EnvPrefix:        envPrefix,
		EnvSeparator:     "_",
		EnvOverridesFile: true,
	}
}

// Load reads config.yaml (and config.local.yaml) from dir and validates the
// result. config.yaml is required because it's where the variables are wired.
func Load(dir string) (Config, error) {
	src, found, err := confy.DiscoverAndLoadConfigs(Discovery(dir))
	if err != nil {
		return Config{}, fmt.Errorf("load %s from %s: %w", configName, dir, err)
	}
	want := filepath.Clean(filepath.Join(dir, configName))
	if !slices.ContainsFunc(found.BaseConfigPaths, func(got string) bool { return filepath.Clean(got) == want }) {
		return Config{}, fmt.Errorf("%s is missing from %s", configName, dir)
	}
	return FromSource(src)
}

// Getter is the one method Load needs from confy. Tests pass a map.
type Getter interface {
	Get(key string) any
}

// FromSource lays the settings in src over the defaults and validates them.
func FromSource(src Getter) (Config, error) {
	c := Defaults()
	r := &reader{src: src}

	r.str(&c.Environment, "environment")
	r.str(&c.HTTPAddr, "http_addr")
	r.str(&c.PublicURL, "public_url")
	r.str(&c.Clouds.EnrollmentURL, "clouds.enrollment_url")
	r.str(&c.SecretKey, "secret_key")

	r.str(&c.Database.URL, "database.url")
	if c.Database.URL == "" {
		// Platforms that provision the database, such as Heroku, Render,
		// DigitalOcean App Platform and Miget, hand the app DATABASE_URL.
		c.Database.URL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	r.integer(&c.Database.PoolSize, "database.pool_size")

	r.str(&c.Identity.BasePath, "identity.base_path")
	r.str(&c.Identity.AppID, "identity.app_id")
	r.str(&c.Identity.PublishableKey, "identity.publishable_key")
	r.boolean(&c.Identity.RequireEmailVerification, "identity.require_email_verification")
	r.str(&c.Identity.SessionCookie, "identity.session_cookie")
	r.optionalBool(&c.Identity.Waitlist, "identity.waitlist")
	r.str(&c.Console.Path, "console.path")
	r.str(&c.Notifications.SMTPHost, "notifications.smtp_host")
	r.integer(&c.Notifications.SMTPPort, "notifications.smtp_port")
	r.str(&c.Notifications.SMTPUsername, "notifications.smtp_username")
	r.str(&c.Notifications.SMTPPassword, "notifications.smtp_password")
	r.str(&c.Notifications.From, "notifications.from")
	r.boolean(&c.Notifications.SMTPTLS, "notifications.smtp_tls")

	r.str(&c.Dashboard.Dir, "dashboard.dir")

	r.integer(&c.Orchestration.Concurrency, "orchestration.concurrency")
	r.duration(&c.Orchestration.PollInterval, "orchestration.poll_interval")

	r.str(&c.GitHub.APIURL, "github.api_url")
	r.str(&c.GitHub.WebURL, "github.web_url")
	r.int64(&c.GitHub.AppID, "github.app_id")
	r.str(&c.GitHub.AppSlug, "github.app_slug")
	r.str(&c.GitHub.PrivateKey, "github.private_key")
	r.str(&c.GitHub.PrivateKeyFile, "github.private_key_file")
	r.str(&c.GitHub.ClientID, "github.client_id")
	r.str(&c.GitHub.ClientSecret, "github.client_secret")
	r.str(&c.GitHub.WebhookSecret, "github.webhook_secret")
	r.str(&c.Analysis.WorkDir, "analysis.work_dir")
	r.integer(&c.Analysis.Concurrency, "analysis.concurrency")
	r.str(&c.Worker.HTTPAddr, "worker.http_addr")
	r.duration(&c.Planning.ApprovalWindow, "planning.approval_window")
	r.prefixes(&c.Planning.EndpointDeniedCIDRs, "planning.endpoint_denied_cidrs")
	r.str(&c.Storage.URL, "storage.url")
	r.str(&c.Storage.ArtifactBucket, "storage.artifact_bucket")
	r.str(&c.Storage.BackupURL, "storage.backup_url")
	r.str(&c.Storage.BackupBucket, "storage.backup_bucket")
	r.duration(&c.Drift.Interval, "drift.interval")
	r.str(&c.Clouds.GoogleEndpoint, "clouds.google_endpoint")
	r.str(&c.Clouds.VercelEndpoint, "clouds.vercel_endpoint")
	r.str(&c.Clouds.RenderEndpoint, "clouds.render_endpoint")
	r.str(&c.Clouds.DigitalOceanEndpoint, "clouds.digitalocean_endpoint")
	r.str(&c.Clouds.AzureEndpoint, "clouds.azure_endpoint")
	r.str(&c.Clouds.AWSEndpoint, "clouds.aws_endpoint")
	r.str(&c.Clouds.CloudflareEndpoint, "clouds.cloudflare_endpoint")
	r.boolean(&c.Clouds.CloudflareRegistrarSandbox, "clouds.cloudflare_registrar_sandbox")
	r.str(&c.Clouds.ACMEDirectory, "clouds.acme_directory")
	r.boolean(&c.Clouds.KubernetesInCluster, "clouds.kubernetes_in_cluster")
	c.Clouds.OAuth = r.oauthApps(c.Clouds.OAuth, "clouds.oauth")
	r.plugins(&c.Providers.Plugins, "providers.plugins")
	r.names(&c.Providers.Paused, "providers.paused")
	r.boolean(&c.Federation.Enabled, "federation.enabled")
	r.boolean(&c.AI.Enabled, "ai.enabled")
	r.str(&c.AI.Models.HuggingFaceURL, "ai.models.huggingface_url")
	r.boolean(&c.Federation.Runtime.Enabled, "federation.runtime.enabled")
	r.str(&c.Federation.Runtime.ListenAddress, "federation.runtime.listen_address")
	r.str(&c.Federation.Runtime.OrganizationID, "federation.runtime.organization_id")
	r.str(&c.Federation.Runtime.TrustDomain, "federation.runtime.trust_domain")
	r.str(&c.Federation.Runtime.WorkloadAPI, "federation.runtime.workload_api")
	r.str(&c.Federation.Runtime.SigningKeysFile, "federation.runtime.signing_keys_file")
	r.boolean(&c.Federation.Launch.Enabled, "federation.launch.enabled")
	r.str(&c.Federation.Launch.ControlEndpoint, "federation.launch.control_endpoint")
	r.str(&c.Federation.Launch.AgentImage, "federation.launch.agent_image")
	r.int64(&c.Federation.Launch.TrustGeneration, "federation.launch.trust_generation")
	r.boolean(&c.Federation.Identity.Enabled, "federation.identity.enabled")
	r.str(&c.Federation.Identity.OrganizationID, "federation.identity.organization_id")
	r.str(&c.Federation.Identity.TrustDomain, "federation.identity.trust_domain")
	r.str(&c.Federation.Identity.AdminSocket, "federation.identity.admin_socket")
	r.str(&c.DBGateway.SigningKeyFile, "dbgateway.signing_key_file")

	if err := errors.Join(r.errs...); err != nil {
		return Config{}, err
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate reports settings Loomplane can't run with.
func (c *Config) Validate() error {
	var errs []error
	errs = append(errs, c.Notifications.Validate(c.Environment), c.Federation.Validate(), c.AI.Models.Validate())
	switch c.Environment {
	case EnvDevelopment, EnvStaging, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("environment must be development, staging or production, got %q", c.Environment))
	}
	if key, err := base64.StdEncoding.DecodeString(c.SecretKey); err != nil || len(key) != secretKeyBytes {
		errs = append(errs, fmt.Errorf("secret_key must be %d bytes, base64 encoded (openssl rand -base64 32)", secretKeyBytes))
	}
	if c.Database.URL == "" {
		errs = append(errs, errors.New("database.url is required (LOOMPLANE_DATABASE_URL, or DATABASE_URL)"))
	}
	if c.Database.PoolSize < 0 {
		errs = append(errs, errors.New("database.pool_size can't be negative"))
	}
	if !strings.HasPrefix(c.Identity.BasePath, "/") || c.Identity.BasePath == "/" {
		errs = append(errs, fmt.Errorf("identity.base_path must be a path below /, got %q", c.Identity.BasePath))
	}
	if !strings.HasPrefix(c.Identity.AppID, "aapp_") {
		errs = append(errs, fmt.Errorf("identity.app_id must be an authsome app ID (aapp_...), got %q", c.Identity.AppID))
	}
	if !cookieName.MatchString(c.Identity.SessionCookie) {
		errs = append(errs, fmt.Errorf("identity.session_cookie must be a cookie name (letters, digits and !#$%%&'*+-.^_`|~), got %q", c.Identity.SessionCookie))
	}
	if !consolePath.MatchString(c.Console.Path) {
		errs = append(errs, fmt.Errorf("console.path must be one segment starting with __ (like /__console), got %q", c.Console.Path))
	}
	if u, err := url.Parse(c.PublicURL); err != nil || u.Scheme == "" || u.Host == "" {
		errs = append(errs, fmt.Errorf("public_url must be an absolute URL, got %q", c.PublicURL))
	}
	if c.Orchestration.Concurrency < 1 {
		errs = append(errs, errors.New("orchestration.concurrency must be at least 1"))
	}
	if c.Orchestration.PollInterval <= 0 {
		errs = append(errs, errors.New("orchestration.poll_interval must be positive"))
	}
	for i, p := range c.Providers.Plugins {
		if u, err := url.Parse(p.URL); err != nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, fmt.Errorf("providers.plugins[%d].url must be an absolute URL, got %q", i, p.URL))
		}
		if p.Token == "" {
			errs = append(errs, fmt.Errorf("providers.plugins[%d].token is required: the provider refuses requests without it", i))
		}
	}
	if c.Drift.Interval != 0 && c.Drift.Interval < time.Second {
		errs = append(errs, fmt.Errorf("drift.interval must be 0, to turn it off, or at least 1s, got %s", c.Drift.Interval))
	}
	if c.Planning.ApprovalWindow < minApprovalWindow || c.Planning.ApprovalWindow > maxApprovalWindow {
		errs = append(errs, fmt.Errorf("planning.approval_window must be between %s and %s, got %s",
			minApprovalWindow, maxApprovalWindow, c.Planning.ApprovalWindow))
	}
	if u, err := url.Parse(c.GitHub.APIURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		errs = append(errs, fmt.Errorf("github.api_url must be an absolute URL, got %q", c.GitHub.APIURL))
	}
	if u, err := url.Parse(c.GitHub.WebURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		errs = append(errs, fmt.Errorf("github.web_url must be an absolute URL, got %q", c.GitHub.WebURL))
	}
	if n := c.GitHub.appSettingsSet(); n != 0 && n != 5 {
		errs = append(errs, errors.New("a GitHub App needs all of github.app_id, app_slug, private_key (or private_key_file), client_id and client_secret"))
	}
	if c.Analysis.Concurrency < 1 {
		errs = append(errs, errors.New("analysis.concurrency must be at least 1"))
	}
	if f := c.DBGateway.SigningKeyFile; f != "" && (!filepath.IsAbs(f) || filepath.Clean(f) != f) {
		errs = append(errs, errors.New("dbgateway.signing_key_file must be a canonical absolute path"))
	}
	if c.Environment == EnvProduction {
		errs = append(errs, c.productionChecks()...)
	}
	return errors.Join(errs...)
}

// productionChecks refuse development defaults that would be unsafe in
// production.
func (c *Config) productionChecks() []error {
	var errs []error
	if c.SecretKey == devSecretKey {
		errs = append(errs, errors.New("production refuses the development secret_key; set LOOMPLANE_SECRET_KEY"))
	}
	if strings.HasPrefix(c.PublicURL, "http://") {
		errs = append(errs, errors.New("production needs an https public_url"))
	}
	return errs
}

// IsDevelopment reports whether Loomplane runs in development.
func (c *Config) IsDevelopment() bool { return c.Environment == EnvDevelopment }

// reader takes settings out of a Getter. An empty value is no value, so a
// variable that isn't set leaves the default alone. A value that doesn't
// parse is collected, so every bad setting is reported at once.
type reader struct {
	src  Getter
	errs []error
}

func (r *reader) raw(key string) (any, bool) {
	v := r.src.Get(key)
	if v == nil {
		return nil, false
	}
	if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
		return nil, false
	}
	return v, true
}

func (r *reader) fail(key, want string, v any) {
	r.errs = append(r.errs, fmt.Errorf("%s must be %s, got %q", key, want, fmt.Sprint(v)))
}

// plugins reads a list of {url, token}.
func (r *reader) plugins(dst *[]PluginConfig, key string) {
	v, ok := r.raw(key)
	if !ok {
		return
	}
	list, ok := v.([]any)
	if !ok {
		r.fail(key, "a list of {url, token}", v)
		return
	}
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			r.fail(fmt.Sprintf("%s[%d]", key, i), "{url, token}", item)
			continue
		}
		get := func(k string) string {
			if s, ok := m[k]; ok && s != nil {
				return strings.TrimSpace(fmt.Sprint(s))
			}
			return ""
		}
		*dst = append(*dst, PluginConfig{URL: get("url"), Token: get("token")})
	}
}

// names reads a list of names, as a list or as one comma-separated
// string, which is what an environment variable can hold.
func (r *reader) names(dst *[]string, key string) {
	v, ok := r.raw(key)
	if !ok {
		return
	}
	var items []string
	switch t := v.(type) {
	case string:
		items = strings.Split(t, ",")
	case []any:
		for _, item := range t {
			items = append(items, fmt.Sprint(item))
		}
	default:
		r.fail(key, "a list of names", v)
		return
	}
	for _, s := range items {
		if s = strings.TrimSpace(s); s != "" {
			*dst = append(*dst, s)
		}
	}
}

// prefixes reads a list of CIDR ranges, as a list or a comma-separated
// string. A bare address is a range of one.
func (r *reader) prefixes(dst *[]netip.Prefix, key string) {
	var items []string
	before := len(r.errs)
	r.names(&items, key)
	if len(r.errs) > before {
		return
	}
	for _, item := range items {
		p, err := netip.ParsePrefix(item)
		if err != nil {
			addr, aerr := netip.ParseAddr(item)
			if aerr != nil {
				r.fail(key, "a list of CIDR ranges, such as 10.20.0.0/16", item)
				continue
			}
			p = netip.PrefixFrom(addr.WithZone(""), addr.BitLen())
		}
		*dst = append(*dst, p.Masked())
	}
}

// oauthApps reads <key>.<cloud>.client_id and client_secret for each cloud
// that can sign in, and slug for Vercel. A cloud with only one of the
// two is an error, and so is a Vercel app without its slug.
func (r *reader) oauthApps(dst map[string]OAuthApp, key string) map[string]OAuthApp {
	for _, cloud := range oauthClouds {
		var app OAuthApp
		r.str(&app.ClientID, key+"."+cloud+".client_id")
		r.str(&app.ClientSecret, key+"."+cloud+".client_secret")
		r.str(&app.Slug, key+"."+cloud+".slug")
		switch {
		case app.ClientID == "" && app.ClientSecret == "":
			continue
		case app.ClientID == "":
			r.errs = append(r.errs, fmt.Errorf("%s.%s.client_id is required when client_secret is set", key, cloud))
			continue
		case app.ClientSecret == "":
			r.errs = append(r.errs, fmt.Errorf("%s.%s.client_secret is required when client_id is set", key, cloud))
			continue
		}
		if cloud == "vercel" && app.Slug == "" {
			r.errs = append(r.errs, fmt.Errorf("%s.%s.slug is required when client_id and client_secret are set", key, cloud))
			continue
		}
		if dst == nil {
			dst = map[string]OAuthApp{}
		}
		dst[cloud] = app
	}
	return dst
}

func (r *reader) str(dst *string, key string) {
	if v, ok := r.raw(key); ok {
		*dst = strings.TrimSpace(fmt.Sprint(v))
	}
}

func (r *reader) integer(dst *int, key string) {
	v, ok := r.raw(key)
	if !ok {
		return
	}
	switch t := v.(type) {
	case int:
		*dst = t
	case int64:
		*dst = int(t)
	case float64:
		if t != float64(int(t)) {
			r.fail(key, "a whole number", v)
			return
		}
		*dst = int(t)
	default:
		n, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(v)))
		if err != nil {
			r.fail(key, "a whole number", v)
			return
		}
		*dst = n
	}
}

// int64 reads a whole number that may not fit an int on 32-bit platforms,
// such as a GitHub App ID.
func (r *reader) int64(dst *int64, key string) {
	if _, ok := r.raw(key); !ok {
		return
	}
	var n int
	r.integer(&n, key)
	*dst = int64(n)
}

// boolean reads a bool. confy turns yes/no/on/off into bools even when
// quoted, and variables arrive as strings, so both are handled.
func (r *reader) boolean(dst *bool, key string) {
	v, ok := r.raw(key)
	if !ok {
		return
	}
	if b, isBool := v.(bool); isBool {
		*dst = b
		return
	}
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(v))) {
	case "true", "yes", "on", "1":
		*dst = true
	case "false", "no", "off", "0":
		*dst = false
	default:
		r.fail(key, "true or false", v)
	}
}

// optionalBool reads a bool that stays nil when it isn't set, so "off" and
// "not set" differ.
func (r *reader) optionalBool(dst **bool, key string) {
	if _, ok := r.raw(key); !ok {
		return
	}
	var b bool
	n := len(r.errs)
	r.boolean(&b, key)
	if len(r.errs) == n {
		*dst = &b
	}
}

func (r *reader) duration(dst *time.Duration, key string) {
	v, ok := r.raw(key)
	if !ok {
		return
	}
	d, err := time.ParseDuration(strings.TrimSpace(fmt.Sprint(v)))
	if err != nil {
		r.fail(key, "a duration such as 2s", v)
		return
	}
	*dst = d
}
