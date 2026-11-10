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

	"github.com/xraph/loomplane/internal/repositories"
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
	Bitbucket     BitbucketConfig
	GitLab        GitLabConfig
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
	Push          PushConfig
	Nodes         NodesConfig
	Chronicle     ChronicleConfig
	Hallproof     HallproofConfig
	Mobile        MobileConfig
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

// BitbucketConfig is how Loomplane reaches Bitbucket Cloud. Public
// repositories need none of it.
type BitbucketConfig struct {
	// APIURL is Bitbucket Cloud's REST API: https://api.bitbucket.org/2.0.
	APIURL string
	// WebURL is Bitbucket itself, where repository archives are served:
	// https://bitbucket.org.
	WebURL string

	// ClientID and ClientSecret are an OAuth consumer's key and secret,
	// which connect an organization to its private repositories. Leave
	// both empty to read public repositories only; set both otherwise.
	ClientID     string
	ClientSecret string
}

// GitLabConfig is how Loomplane reaches GitLab.
type GitLabConfig struct {
	// APIURL is the address Loomplane reads gitlab.com from:
	// https://gitlab.com.
	APIURL string
	// ClientID and ClientSecret are gitlab.com's OAuth application. Set
	// both to let people connect a gitlab.com account, or neither.
	ClientID     string
	ClientSecret string
	// Hosts are the self-managed GitLabs this Loomplane reads for every
	// organization. They are yours, so they may be plain http and on a
	// private network.
	Hosts []GitLabHostConfig
	// AllowOrganizationHosts lets organizations register a GitLab of their
	// own. Loomplane connects to those only at public addresses.
	AllowOrganizationHosts bool
	// WebhookURL is where GitLab reaches this Loomplane's webhook, when
	// PublicURL isn't an address GitLab can reach. Empty means PublicURL's.
	WebhookURL string
}

// GitLabHostConfig is one self-managed GitLab.
type GitLabHostConfig struct {
	// WebURL is the GitLab's address with no path: https://git.example.com.
	WebURL string
	// ClientID and ClientSecret are the OAuth application registered in
	// that GitLab. Set both, or neither to read public projects only.
	ClientID     string
	ClientSecret string
}

// Configured reports whether an OAuth consumer is set up.
func (b BitbucketConfig) Configured() bool {
	return b.ClientID != "" && b.ClientSecret != ""
}

// Validate reports GitLab settings Loomplane can't run with.
func (g GitLabConfig) Validate() error {
	var errs []error
	if u, err := url.Parse(g.APIURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		errs = append(errs, fmt.Errorf("gitlab.api_url must be an absolute URL, got %q", g.APIURL))
	}
	if g.WebhookURL != "" {
		if u, err := url.Parse(g.WebhookURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			errs = append(errs, fmt.Errorf("gitlab.webhook_url must be an absolute URL, got %q", g.WebhookURL))
		}
	}
	if (g.ClientID == "") != (g.ClientSecret == "") {
		missing := "gitlab.client_secret"
		if g.ClientID == "" {
			missing = "gitlab.client_id"
		}
		errs = append(errs, fmt.Errorf("%s is required when the other of gitlab.client_id and gitlab.client_secret is set", missing))
	}
	seen := map[string]bool{}
	for i, h := range g.Hosts {
		key := fmt.Sprintf("gitlab.hosts[%d]", i)
		_, host, err := repositories.NormalizeGitLabURL(h.WebURL, true)
		switch {
		case err != nil || h.WebURL == "":
			if err == nil {
				err = errors.New("it is required")
			}
			errs = append(errs, fmt.Errorf("%s.web_url must be the address of a GitLab with no path, such as https://git.example.com: %w", key, err))
		case seen[host]:
			errs = append(errs, fmt.Errorf("%s.web_url lists %s twice", key, host))
		default:
			seen[host] = true
		}
		if (h.ClientID == "") != (h.ClientSecret == "") {
			missing := "client_secret"
			if h.ClientID == "" {
				missing = "client_id"
			}
			errs = append(errs, fmt.Errorf("%s.%s is required when the other of client_id and client_secret is set", key, missing))
		}
	}
	return errors.Join(errs...)
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
	// EmailVerification is identity.require_email_verification as set.
	// Nil, the default, means on wherever the instance can send mail.
	EmailVerification *bool
	// RequireEmailVerification is the setting resolved: whether an account
	// must verify its email before it can sign in or use the API.
	RequireEmailVerification bool
	// SessionCookie names the HttpOnly cookie the dashboard's session is
	// kept in. Cookies are shared by every port on a host, so two authsome
	// apps on one host need different names, or each replaces the other's.
	SessionCookie string
	// Waitlist turns the sign-up waitlist on or off. Nil leaves it to the
	// instance's stored setting.
	Waitlist *bool
	// PasskeyRPID is the WebAuthn relying party ID passkeys are bound to:
	// a bare domain, which every origin below must be or sit under.
	// Changing it strands every passkey already registered. Empty means
	// public_url's host.
	PasskeyRPID string
	// PasskeyOrigins are the origins a browser may register or use a
	// passkey from. Empty means public_url's origin. On localhost, authsome
	// also accepts the page's own origin, so the Vite dev server works.
	PasskeyOrigins []string
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
	MultiLocationDelivery bool
	ManagedClusters       ManagedClustersConfig
	ClusterCapacity       ClusterCapacityConfig
	AutomaticCapacity     AutomaticCapacityConfig

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
	// from, for server and Kubernetes routes. Empty means Let's Encrypt's
	// production directory.
	ACMEDirectory string
	// ACMERootsFile is a PEM file of the CA certificates ACMEDirectory's
	// own TLS certificate chains to, for a directory the proxies' system
	// roots don't trust, such as a test CA. Server proxies get a copy.
	// Kubernetes routes don't use it. Empty uses the system roots.
	ACMERootsFile string
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
var oauthClouds = []string{"digitalocean", "vercel", "gcp", "azure", "cloudflare"}

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
		GitHub:    GitHubConfig{APIURL: "https://api.github.com", WebURL: "https://github.com"},
		Bitbucket: BitbucketConfig{APIURL: "https://api.bitbucket.org/2.0", WebURL: "https://bitbucket.org"},
		GitLab:    GitLabConfig{APIURL: "https://gitlab.com", AllowOrganizationHosts: true},
		Analysis:  AnalysisConfig{Concurrency: 2},
		Planning:  PlanningConfig{ApprovalWindow: approvalWindow},
		Drift:     DriftConfig{Interval: 30 * time.Minute},
		Worker:    WorkerConfig{HTTPAddr: ":8781"},
		AI: AIConfig{
			Provider:  AIProviderAnthropic,
			Model:     DefaultAIModel,
			Models:    AIModelsConfig{HuggingFaceURL: DefaultHuggingFaceURL},
			Knowledge: AIKnowledgeConfig{Dimensions: DefaultEmbeddingDimensions, DocsBudgetUSD: DefaultDocsBudgetUSD},
		},
		Push:  PushConfig{Enabled: true, GatewayURL: DefaultPushGatewayURL},
		Nodes: NodesConfig{JoinRateLimit: DefaultNodeJoinRateLimit, ChallengeRateLimit: DefaultNodeChallengeRateLimit},
		Chronicle: ChronicleConfig{Checkpoints: CheckpointsConfig{
			Enabled:       true,
			EveryEvents:   DefaultCheckpointEveryEvents,
			EveryInterval: DefaultCheckpointEveryInterval,
		}},
		Mobile: MobileConfig{
			AndroidPackage:          DefaultAndroidPackage,
			AndroidCertFingerprints: slices.Clone(DefaultAndroidCertFingerprints),
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
	r.optionalBool(&c.Identity.EmailVerification, "identity.require_email_verification")
	r.str(&c.Identity.SessionCookie, "identity.session_cookie")
	r.optionalBool(&c.Identity.Waitlist, "identity.waitlist")
	r.str(&c.Identity.PasskeyRPID, "identity.passkey_rp_id")
	r.names(&c.Identity.PasskeyOrigins, "identity.passkey_origins")
	r.str(&c.Console.Path, "console.path")
	r.str(&c.Notifications.ResendAPIKey, "notifications.resend_api_key")
	r.str(&c.Notifications.MailDir, "notifications.mail_dir")
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
	r.str(&c.Bitbucket.APIURL, "bitbucket.api_url")
	r.str(&c.Bitbucket.WebURL, "bitbucket.web_url")
	r.str(&c.Bitbucket.ClientID, "bitbucket.client_id")
	r.str(&c.Bitbucket.ClientSecret, "bitbucket.client_secret")
	r.str(&c.GitLab.APIURL, "gitlab.api_url")
	r.str(&c.GitLab.ClientID, "gitlab.client_id")
	r.str(&c.GitLab.ClientSecret, "gitlab.client_secret")
	r.gitlabHosts(&c.GitLab.Hosts, "gitlab.hosts")
	r.boolean(&c.GitLab.AllowOrganizationHosts, "gitlab.allow_organization_hosts")
	r.str(&c.GitLab.WebhookURL, "gitlab.webhook_url")
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
	r.str(&c.Clouds.ACMERootsFile, "clouds.acme_roots_file")
	r.boolean(&c.Clouds.KubernetesInCluster, "clouds.kubernetes_in_cluster")
	r.boolean(&c.Clouds.ManagedClusters.Enabled, "clouds.managed_clusters.enabled")
	r.boolean(&c.Clouds.ClusterCapacity.Enabled, "clouds.cluster_capacity.enabled")
	r.boolean(&c.Clouds.AutomaticCapacity.Enabled, "clouds.automatic_capacity.enabled")
	r.boolean(&c.Clouds.MultiLocationDelivery, "clouds.multi_location_delivery")
	r.str(&c.Clouds.ManagedClusters.ManagementProbe.Provider, "clouds.managed_clusters.management_probe.provider")
	r.str(&c.Clouds.ManagedClusters.ManagementProbe.NetworkID, "clouds.managed_clusters.management_probe.network_id")
	r.str(&c.Clouds.ManagedClusters.ManagementProbe.EvidenceID, "clouds.managed_clusters.management_probe.evidence_id")
	r.str(&c.Clouds.ManagedClusters.ManagementProbe.URL, "clouds.managed_clusters.management_probe.url")
	r.str(&c.Clouds.ManagedClusters.ManagementProbe.SPKISHA256, "clouds.managed_clusters.management_probe.spki_sha256")
	r.str(&c.Clouds.ManagedClusters.ManagementProbe.ExpiresAt, "clouds.managed_clusters.management_probe.expires_at")
	r.str(&c.Clouds.ManagedClusters.QualificationOrgID, "clouds.managed_clusters.qualification_org_id")
	r.str(&c.Clouds.ManagedClusters.QualificationOperatorID, "clouds.managed_clusters.qualification_operator_id")
	r.str(&c.Clouds.ManagedClusters.QualificationAccountID, "clouds.managed_clusters.qualification_account_id")
	r.str(&c.Clouds.ManagedClusters.QualificationRegion, "clouds.managed_clusters.qualification_region")
	r.integer(&c.Clouds.ManagedClusters.MaxNodes, "clouds.managed_clusters.max_nodes")
	r.int64(&c.Clouds.ManagedClusters.MaxNodeHourlyMicroUSD, "clouds.managed_clusters.max_node_hourly_micro_usd")
	r.integer(&c.Clouds.ManagedClusters.MaxAccountCreates, "clouds.managed_clusters.max_account_creates")
	c.Clouds.OAuth = r.oauthApps(c.Clouds.OAuth, "clouds.oauth")
	r.plugins(&c.Providers.Plugins, "providers.plugins")
	r.names(&c.Providers.Paused, "providers.paused")
	r.boolean(&c.Federation.Enabled, "federation.enabled")
	r.boolean(&c.AI.Enabled, "ai.enabled")
	r.str(&c.AI.Provider, "ai.provider")
	r.str(&c.AI.APIKey, "ai.api_key")
	r.str(&c.AI.BaseURL, "ai.base_url")
	r.str(&c.AI.Model, "ai.model")
	r.prices(&c.AI.Prices, "ai.prices")
	r.str(&c.AI.Knowledge.EmbeddingProvider, "ai.knowledge.embedding_provider")
	r.str(&c.AI.Knowledge.EmbeddingAPIKey, "ai.knowledge.embedding_api_key")
	r.str(&c.AI.Knowledge.EmbeddingBaseURL, "ai.knowledge.embedding_base_url")
	r.str(&c.AI.Knowledge.EmbeddingModel, "ai.knowledge.embedding_model")
	r.integer(&c.AI.Knowledge.Dimensions, "ai.knowledge.dimensions")
	r.str(&c.AI.Knowledge.DocsBudgetUSD, "ai.knowledge.docs_budget_usd")
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
	r.boolean(&c.Push.Enabled, "push.enabled")
	r.str(&c.Push.GatewayURL, "push.gateway_url")
	r.str(&c.Nodes.AdvertiseURL, "nodes.advertise_url")
	r.boolean(&c.Nodes.HopInsecure, "nodes.hop_insecure")
	r.str(&c.Nodes.HopCAFile, "nodes.hop_ca_file")
	r.integer(&c.Nodes.JoinRateLimit, "nodes.join_rate_limit")
	r.integer(&c.Nodes.ChallengeRateLimit, "nodes.challenge_rate_limit")
	r.boolean(&c.Chronicle.Checkpoints.Enabled, "chronicle.checkpoints.enabled")
	r.integer(&c.Chronicle.Checkpoints.EveryEvents, "chronicle.checkpoints.every_events")
	r.duration(&c.Chronicle.Checkpoints.EveryInterval, "chronicle.checkpoints.every_interval")
	r.str(&c.Hallproof.Endpoint, "hallproof.endpoint")
	r.str(&c.Hallproof.IngestKey, "hallproof.ingest_key")
	r.str(&c.Hallproof.IngestKeyFile, "hallproof.ingest_key_file")
	r.names(&c.Hallproof.StaffOrgs, "hallproof.staff_orgs")
	r.str(&c.Mobile.AndroidPackage, "mobile.android_package")
	// A list read appends, so a set value replaces the store app's keys
	// rather than joining them.
	var fingerprints []string
	r.names(&fingerprints, "mobile.android_cert_fingerprints")
	if len(fingerprints) > 0 {
		c.Mobile.AndroidCertFingerprints = fingerprints
	}

	if err := errors.Join(r.errs...); err != nil {
		return Config{}, err
	}
	// Unset, verification follows mail: on wherever the code can be sent,
	// since without mail nobody after the setup owner could verify.
	c.Identity.RequireEmailVerification = c.Notifications.MailEnabled()
	if c.Identity.EmailVerification != nil {
		c.Identity.RequireEmailVerification = *c.Identity.EmailVerification
	}
	c.Identity.resolvePasskeys(c.PublicURL)
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate reports settings Loomplane can't run with.
func (c *Config) Validate() error {
	var errs []error
	errs = append(errs, c.Notifications.Validate(c.Environment), c.GitLab.Validate(), c.Federation.Validate(), c.AI.Models.Validate(), c.Push.Validate(c.Environment), c.Nodes.Validate(), c.Chronicle.Checkpoints.Validate(), c.Hallproof.Validate(), c.Mobile.Validate())
	if c.AI.Enabled {
		errs = append(errs, c.AI.Validate(c.Environment))
	}
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
	errs = append(errs, c.Identity.validatePasskeys()...)
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
	if u, err := url.Parse(c.Bitbucket.APIURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		errs = append(errs, fmt.Errorf("bitbucket.api_url must be an absolute URL, got %q", c.Bitbucket.APIURL))
	}
	if u, err := url.Parse(c.Bitbucket.WebURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		errs = append(errs, fmt.Errorf("bitbucket.web_url must be an absolute URL, got %q", c.Bitbucket.WebURL))
	}
	if (c.Bitbucket.ClientID == "") != (c.Bitbucket.ClientSecret == "") {
		errs = append(errs, errors.New("a Bitbucket OAuth consumer needs both bitbucket.client_id and client_secret"))
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
	if c.Identity.RequireEmailVerification && !c.Notifications.MailEnabled() {
		errs = append(errs, errors.New("identity.require_email_verification is on but email is off, so nobody could get a code; set up SMTP or Resend, or set LOOMPLANE_REQUIRE_EMAIL_VERIFICATION=false"))
	}
	if strings.HasPrefix(c.PublicURL, "http://") {
		errs = append(errs, errors.New("production needs an https public_url"))
	}
	return errs
}

// resolvePasskeys fills the passkey settings left empty from public_url.
// A public_url that doesn't parse is left to Validate to report.
func (i *IdentityConfig) resolvePasskeys(publicURL string) {
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return
	}
	if i.PasskeyRPID == "" {
		i.PasskeyRPID = u.Hostname()
	}
	if len(i.PasskeyOrigins) == 0 {
		i.PasskeyOrigins = []string{u.Scheme + "://" + u.Host}
	}
}

// validatePasskeys checks the relying party ID is a bare host and that
// every origin is one a browser would accept for it: the host itself or a
// subdomain of it.
func (i *IdentityConfig) validatePasskeys() []error {
	rpID := i.PasskeyRPID
	if rpID == "" || strings.ContainsAny(rpID, ":/") {
		return []error{fmt.Errorf("identity.passkey_rp_id must be a bare host such as example.com, got %q", rpID)}
	}
	var errs []error
	for _, o := range i.PasskeyOrigins {
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" {
			errs = append(errs, fmt.Errorf("identity.passkey_origins must be origins such as https://lp.example.com, got %q", o))
			continue
		}
		if h := u.Hostname(); h != rpID && !strings.HasSuffix(h, "."+rpID) {
			errs = append(errs, fmt.Errorf("identity.passkey_origins: %q isn't %s or a subdomain of it (identity.passkey_rp_id)", o, rpID))
		}
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

// gitlabHosts reads a list of {web_url, client_id, client_secret}.
func (r *reader) gitlabHosts(dst *[]GitLabHostConfig, key string) {
	v, ok := r.raw(key)
	if !ok {
		return
	}
	list, ok := v.([]any)
	if !ok {
		r.fail(key, "a list of {web_url, client_id, client_secret}", v)
		return
	}
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			r.fail(fmt.Sprintf("%s[%d]", key, i), "{web_url, client_id, client_secret}", item)
			continue
		}
		get := func(k string) string {
			if s, ok := m[k]; ok && s != nil {
				return strings.TrimSpace(fmt.Sprint(s))
			}
			return ""
		}
		*dst = append(*dst, GitLabHostConfig{WebURL: get("web_url"), ClientID: get("client_id"), ClientSecret: get("client_secret")})
	}
}

// prices reads a list of {provider, model, input_per_million,
// output_per_million}.
func (r *reader) prices(dst *[]AIPrice, key string) {
	v, ok := r.raw(key)
	if !ok {
		return
	}
	list, ok := v.([]any)
	if !ok {
		r.fail(key, "a list of {provider, model, input_per_million, output_per_million}", v)
		return
	}
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			r.fail(fmt.Sprintf("%s[%d]", key, i), "{provider, model, input_per_million, output_per_million}", item)
			continue
		}
		get := func(k string) string {
			if s, ok := m[k]; ok && s != nil {
				return strings.TrimSpace(fmt.Sprint(s))
			}
			return ""
		}
		*dst = append(*dst, AIPrice{
			Provider: get("provider"), Model: get("model"),
			InputPerMillion: get("input_per_million"), OutputPerMillion: get("output_per_million"),
		})
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
	case bool:
		// confy turns "1" and "0" variables into bools, so read them as numbers.
		*dst = map[bool]int{true: 1, false: 0}[t]
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

// ManagedClustersConfig restricts purchases to explicitly scoped qualification runs.
// General purchase rollout requires the retirement qualification in the lifecycle plan.
type ManagedClustersConfig struct {
	ManagementProbe         ManagedClusterManagementProbeConfig
	MaxNodeHourlyMicroUSD   int64
	Enabled                 bool
	QualificationOrgID      string
	QualificationOperatorID string
	QualificationAccountID  string
	QualificationRegion     string
	MaxNodes                int
	MaxAccountCreates       int
}

// ManagedClusterManagementProbeConfig identifies an operator-qualified existing
// management path. Its material does not belong in API reviews or logs.
type ManagedClusterManagementProbeConfig struct {
	Provider   string `json:"-"`
	NetworkID  string `json:"-"`
	EvidenceID string `json:"-"`
	URL        string `json:"-"`
	SPKISHA256 string `json:"-"`
	ExpiresAt  string `json:"-"`
}

func (ManagedClusterManagementProbeConfig) String() string     { return "[management path qualification]" }
func (c ManagedClusterManagementProbeConfig) GoString() string { return c.String() }

// ClusterCapacityConfig gates reviewed cluster scaling independently of server automation.
type ClusterCapacityConfig struct{ Enabled bool }

// AutomaticCapacityConfig lets server pools grow on their own from saved
// capacity policies. It stays off until integrated qualification.
type AutomaticCapacityConfig struct{ Enabled bool }
