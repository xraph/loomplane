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
