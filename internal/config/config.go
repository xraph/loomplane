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
	"net/url"
	"path/filepath"
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
	Dashboard     DashboardConfig
	Orchestration OrchestrationConfig
	GitHub        GitHubConfig
	Analysis      AnalysisConfig
	Planning      PlanningConfig
	Clouds        CloudsConfig
	Storage       StorageConfig
	Drift         DriftConfig
	Worker        WorkerConfig
	Providers     ProvidersConfig
}

// ProvidersConfig adds deployment targets Loomplane doesn't ship with.
type ProvidersConfig struct {
	// Plugins are providers that run as their own services and speak the
	// protocol in pkg/provider/sdk.
	Plugins []PluginConfig
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
	// URL names the storage: file:///absolute/path for a directory. Empty
	// means .loomplane/storage under the working directory, for
	// development.
	URL string
}

// CloudsConfig points Loomplane's cloud clients somewhere other than the
// clouds' own APIs, for tests. Leave both empty in every real
// deployment.
type CloudsConfig struct {
	// GoogleEndpoint replaces every Google Cloud API host.
	GoogleEndpoint string
	// VercelEndpoint replaces Vercel's API.
	VercelEndpoint string
	// AWSEndpoint replaces every AWS endpoint.
	AWSEndpoint string
}

// Bounds on the approval window: long enough to reach a person, short
// enough that an approved plan still describes the world.
const (
	minApprovalWindow = time.Minute
	maxApprovalWindow = 90 * 24 * time.Hour
	approvalWindow    = 7 * 24 * time.Hour
)

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
		},
		Dashboard: DashboardConfig{Dir: "apps/dashboard/dist"},
		Orchestration: OrchestrationConfig{
			Concurrency:  4,
			PollInterval: time.Second,
		},
		GitHub:   GitHubConfig{APIURL: "https://api.github.com", WebURL: "https://github.com"},
		Analysis: AnalysisConfig{Concurrency: 2},
		Planning: PlanningConfig{ApprovalWindow: approvalWindow},
		Drift:    DriftConfig{Interval: 30 * time.Minute},
		Worker:   WorkerConfig{HTTPAddr: ":8781"},
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
	r.str(&c.SecretKey, "secret_key")

	r.str(&c.Database.URL, "database.url")
	r.integer(&c.Database.PoolSize, "database.pool_size")

	r.str(&c.Identity.BasePath, "identity.base_path")
	r.str(&c.Identity.AppID, "identity.app_id")
	r.str(&c.Identity.PublishableKey, "identity.publishable_key")
	r.boolean(&c.Identity.RequireEmailVerification, "identity.require_email_verification")

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
	r.str(&c.Storage.URL, "storage.url")
	r.duration(&c.Drift.Interval, "drift.interval")
	r.str(&c.Clouds.GoogleEndpoint, "clouds.google_endpoint")
	r.str(&c.Clouds.VercelEndpoint, "clouds.vercel_endpoint")
	r.str(&c.Clouds.AWSEndpoint, "clouds.aws_endpoint")
	r.plugins(&c.Providers.Plugins, "providers.plugins")

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
	switch c.Environment {
	case EnvDevelopment, EnvStaging, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("environment must be development, staging or production, got %q", c.Environment))
	}
	if key, err := base64.StdEncoding.DecodeString(c.SecretKey); err != nil || len(key) != secretKeyBytes {
		errs = append(errs, fmt.Errorf("secret_key must be %d bytes, base64 encoded (openssl rand -base64 32)", secretKeyBytes))
	}
	if c.Database.URL == "" {
		errs = append(errs, errors.New("database.url is required (LOOMPLANE_DATABASE_URL)"))
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
