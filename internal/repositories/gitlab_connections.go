package repositories

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
	"github.com/xraph/loomplane/internal/security"
)

// Errors from managing an organization's GitLab hosts.
var (
	// ErrGitLabHostsOff means this Loomplane doesn't let organizations
	// register GitLab hosts of their own.
	ErrGitLabHostsOff = errors.New("this Loomplane doesn't let organizations add GitLab hosts")
	// ErrGitLabHostIncomplete means a host was added without its OAuth
	// application's ID or secret.
	ErrGitLabHostIncomplete = errors.New("a GitLab host needs its OAuth application's ID and secret")
	// ErrGitLabSigningToken means a pasted webhook signing token isn't
	// "whsec_" followed by the base64 of 32 bytes.
	ErrGitLabSigningToken = errors.New("a webhook signing token is whsec_ followed by the base64 of 32 bytes")
)

// Audit actions for GitLab hosts and connections.
const (
	AuditGitLabConnect       = "gitlab.connect"
	AuditGitLabDisconnect    = "gitlab.disconnect"
	AuditGitLabHostAdd       = "gitlab.host.add"
	AuditGitLabHostRemove    = "gitlab.host.remove"
	AuditGitLabWebhookSecret = "gitlab.webhook.secret" //nolint:gosec // G101: an audit action's name, not a credential
	// AuditGitLabListFailed records a connection left out of a project
	// listing because GitLab couldn't list its projects.
	AuditGitLabListFailed = "gitlab.list.failed"
)

// GitLabVaultAppID is the Vault app GitLab sign-ins, webhook secrets and
// organization hosts' client secrets are kept under, apart from every other
// credential.
const GitLabVaultAppID = "loomplane-gitlab"

// GitLabVaultKeyLabel names the key, derived from secret_key, that Vault
// encrypts them with. No other credential shares it.
const GitLabVaultKeyLabel = "loomplane/gitlab credentials v1"

// instanceHostPrefix starts the view ID of an instance host, which has no
// row and so no ID of its own.
const instanceHostPrefix = "instance:"

// gitlabMaxRenewals caps the renewals one process runs against the database
// at once, as cloud sign-ins do: each holds a pooled connection for its lock.
const gitlabMaxRenewals = 4

// ErrSecretNotFound means Secrets holds nothing under that ID. Reveal's
// error for a missing secret is one, so callers can tell it from Vault
// failing.
var ErrSecretNotFound = errors.New("no secret is kept under that ID")

// Secrets keeps credentials sealed, per organization. A connection's grant
// is under its ID, its webhook secret under "<ID>/webhook", and an
// organization host's client secret under "host/<host ID>". Reveal of a
// missing secret is an error that is ErrSecretNotFound.
// *connections.VaultSecrets is the real one.
type Secrets interface {
	Put(ctx context.Context, orgID, id string, value []byte) error
	Reveal(ctx context.Context, orgID, id string) ([]byte, error)
	Delete(ctx context.Context, orgID, id string) error
}

// GitLabAuditor records changes to GitLab hosts and connections. Details
// hold the host, username and IDs, never a secret. Recording must not fail
// the change.
type GitLabAuditor interface {
	Record(ctx context.Context, action string, p security.Principal, details map[string]any)
}

// GitLabConnectionsConfig is what GitLabConnections needs.
type GitLabConnectionsConfig struct {
	Store    GitLabStore
	Secrets  Secrets
	Authz    Authorizer
	Audit    GitLabAuditor
	Registry *GitLabRegistry
	// InstanceSecrets maps an instance host to its OAuth client secret.
	InstanceSecrets map[string]string
	// CallbackURL is <public URL>/integrations/gitlab/callback, where
	// GitLab sends people back after signing in.
	CallbackURL string
	// WebhookBaseURL is where GitLab reaches this Loomplane's webhooks.
	WebhookBaseURL string
	// Client returns the API client of a host. Nil uses the registry's
	// rule: unguarded for instance hosts, and guarded against private
	// addresses and unsafe redirects for organization hosts.
	Client func(h GitLabHost) *GitLab
	Now    func() time.Time
}

// GitLabConnections manages an organization's GitLab hosts and connected
// accounts, and mints the tokens analyses read private projects with.
type GitLabConnections struct {
	cfg  GitLabConnectionsConfig
	gate *oauth.Gate
}

var _ TokenSource = (*GitLabConnections)(nil)

// NewGitLabConnections returns GitLabConnections. Audit, Client and Now may
// be nil, and a nil Registry knows no hosts.
func NewGitLabConnections(cfg GitLabConnectionsConfig) *GitLabConnections {
	if cfg.Registry == nil {
		cfg.Registry = &GitLabRegistry{}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &GitLabConnections{cfg: cfg, gate: oauth.NewGate(gitlabMaxRenewals)}
}

// GitLabStatus is the organization's GitLab: the hosts it can sign in to,
// its connections, and whether the caller may connect another.
type GitLabStatus struct {
	Hosts       []GitLabHostView       `json:"hosts"`
	Connections []GitLabConnectionView `json:"connections"`
	CanConnect  bool                   `json:"can_connect"`
	// OrganizationHosts says whether organizations may add hosts.
	OrganizationHosts bool `json:"organization_hosts"`
	// CallbackURL is the redirect URL an OAuth application must allow.
	CallbackURL string `json:"callback_url"`
}

// GitLabHostView is a GitLab host as the API shows it. The client secret is
// never in it.
type GitLabHostView struct {
	// ID is "instance:<host>" for an instance host, so the dashboard has a
	// stable key, and the host's typeid otherwise.
	ID        string     `json:"id" forge:"id"`
	WebURL    string     `json:"web_url"`
	Host      string     `json:"host"`
	Instance  bool       `json:"instance"`
	ClientID  string     `json:"client_id"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
}

// GitLabConnectionView is a connected GitLab account as the API shows it.
type GitLabConnectionView struct {
	ID        string `json:"id" forge:"id"`
	Host      string `json:"host"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
	// Version is the GitLab release seen at sign-in, and VersionSupported
	// whether Loomplane supports it.
	Version          string `json:"version"`
	VersionSupported bool   `json:"version_supported"`
	// Lapsed means GitLab stopped honoring the sign-in, and someone has to
	// sign in again.
	Lapsed     bool `json:"lapsed"`
	WebhookSet bool `json:"webhook_set"`
	// WebhookURL is the address to give GitLab for this connection's
	// projects' webhooks.
	WebhookURL string    `json:"webhook_url"`
	CreatedAt  time.Time `json:"created_at"`
}

// SetGitLabWebhookInput creates or rotates a connection's webhook secret.
type SetGitLabWebhookInput struct {
	// SigningToken is a token to use as the secret: "whsec_" and the
	// standard base64 of 32 bytes. Empty makes a random one.
	SigningToken string `json:"signing_token,omitempty"`
}

// GitLabWebhookSecret is a connection's webhook secret in the two forms
// GitLab takes, and the address to give it. It is shown once, when the
// secret is made.
type GitLabWebhookSecret struct {
	URL string `json:"url"`
	// SigningToken is for GitLab 19.0 and newer, which signs deliveries.
	SigningToken string `json:"signing_token"`
	// SecretToken is the same bytes for older versions, which send them
	// in X-Gitlab-Token.
	SecretToken string `json:"secret_token"`
}

// webhookSecretLen is how many random bytes a webhook secret is.
const webhookSecretLen = 32

// signingTokenPrefix starts a webhook signing token.
const signingTokenPrefix = "whsec_"

// AddGitLabHostInput registers an organization's own GitLab, with the OAuth
// application made on it.
type AddGitLabHostInput struct {
	WebURL       string `json:"web_url"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// GitLabRepository is a project one of the organization's connections can
// read.
type GitLabRepository struct {
	// Key is what a source stores: host/namespace/project.
	Key       string `json:"key"`
	FullName  string `json:"full_name"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Host      string `json:"host"`
	// Connection is the ID of the connection that listed it.
	Connection    string `json:"connection"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
}

// Status returns the organization's GitLab hosts and connections.
func (c *GitLabConnections) Status(ctx context.Context, p security.Principal) (*GitLabStatus, error) {
	if err := c.cfg.Authz.Require(ctx, p, security.ProjectRead, ""); err != nil {
		return nil, err
	}
	st := &GitLabStatus{
		Hosts:             []GitLabHostView{},
		Connections:       []GitLabConnectionView{},
		OrganizationHosts: c.cfg.Registry.AllowOrganizationHosts,
		CallbackURL:       c.cfg.CallbackURL,
	}
	hosts, err := c.signInHosts(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	for _, h := range hosts {
		st.Hosts = append(st.Hosts, hostView(h))
	}
	conns, err := c.cfg.Store.ListConnections(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	sortOldestFirst(conns)
	for _, conn := range conns {
		st.Connections = append(st.Connections, c.connectionView(conn))
	}
	st.CanConnect, err = c.cfg.Authz.Allowed(ctx, p, security.RepositoryConnect)
	if err != nil {
		return nil, err
	}
	return st, nil
}

// AddHost registers a GitLab of the organization's own. Its client secret
// goes to Vault, and nowhere else.
func (c *GitLabConnections) AddHost(ctx context.Context, p security.Principal, in AddGitLabHostInput) (*GitLabHostView, error) {
	if err := c.cfg.Authz.Require(ctx, p, security.RepositoryConnect, ""); err != nil {
		return nil, err
	}
	if !c.cfg.Registry.AllowOrganizationHosts {
		return nil, ErrGitLabHostsOff
	}
	webURL, host, err := NormalizeGitLabURL(in.WebURL, false)
	if err != nil {
		return nil, err
	}
	clientID := strings.TrimSpace(in.ClientID)
	if clientID == "" || in.ClientSecret == "" {
		return nil, ErrGitLabHostIncomplete
	}
	if _, ok := c.cfg.Registry.instance(host); ok {
		return nil, ErrGitLabHostExists
	}
	h := &GitLabHost{
		OrgID: p.OrgID, WebURL: webURL, Host: host, ClientID: clientID,
		CreatedBy: p.UserID, CreatedAt: c.cfg.Now().UTC(),
	}
	if err := c.cfg.Store.SaveHost(ctx, h); err != nil {
		return nil, err
	}
	if err := c.cfg.Secrets.Put(ctx, p.OrgID, hostSecretKey(h.ID), []byte(in.ClientSecret)); err != nil {
		// A host whose secret wasn't kept can't sign anyone in.
		if delErr := c.cfg.Store.DeleteHost(context.WithoutCancel(ctx), p.OrgID, h.ID); delErr != nil {
			return nil, errors.Join(fmt.Errorf("keep the GitLab host's secret: %w", err), delErr)
		}
		return nil, fmt.Errorf("keep the GitLab host's secret: %w", err)
	}
	c.record(ctx, AuditGitLabHostAdd, p, map[string]any{"host": h.Host, "host_id": h.ID})
	v := hostView(*h)
	return &v, nil
}

// RemoveHost removes one of the caller's organization's hosts, and its
// secret. An instance host, or another organization's, is
// ErrGitLabHostNotFound, and one a connection still uses is
// ErrGitLabHostInUse, which keeps both. The secret goes first, so a Vault
// that fails leaves the host in place to remove again rather than a secret
// nothing points at.
func (c *GitLabConnections) RemoveHost(ctx context.Context, p security.Principal, id string) error {
	if err := c.cfg.Authz.Require(ctx, p, security.RepositoryConnect, ""); err != nil {
		return err
	}
	if id == "" || strings.HasPrefix(id, instanceHostPrefix) {
		return ErrGitLabHostNotFound
	}
	h, err := c.ownHost(ctx, p.OrgID, id)
	if err != nil {
		return err
	}
	conns, err := c.cfg.Store.ListConnections(ctx, p.OrgID)
	if err != nil {
		return err
	}
	for _, conn := range conns {
		if conn.HostID == id {
			return ErrGitLabHostInUse
		}
	}
	// Kept to put back should the row's delete fail after all, such as
	// for a connection made since the check above.
	secret, revealErr := c.cfg.Secrets.Reveal(ctx, p.OrgID, hostSecretKey(id))
	if err := c.cfg.Secrets.Delete(ctx, p.OrgID, hostSecretKey(id)); err != nil {
		return fmt.Errorf("forget the GitLab host's secret: %w", err)
	}
	if err := c.cfg.Store.DeleteHost(ctx, p.OrgID, id); err != nil {
		if revealErr == nil {
			if putErr := c.cfg.Secrets.Put(context.WithoutCancel(ctx), p.OrgID, hostSecretKey(id), secret); putErr != nil {
				return errors.Join(err, fmt.Errorf("keep the GitLab host's secret: %w", putErr))
			}
		}
		return err
	}
	c.record(ctx, AuditGitLabHostRemove, p, map[string]any{"host": h.Host, "host_id": id})
	return nil
}

// Disconnect revokes a connection's sign-in at GitLab, forgets its grant
// and webhook secret, and deletes it. A revoke GitLab doesn't take is
// recorded in the audit, not returned: the connection goes either way.
func (c *GitLabConnections) Disconnect(ctx context.Context, p security.Principal, id string) error {
	if err := c.cfg.Authz.Require(ctx, p, security.RepositoryConnect, ""); err != nil {
		return err
	}
	conn, err := c.ownConnection(ctx, p.OrgID, id)
	if err != nil {
		return err
	}
	details := map[string]any{"host": conn.Host, "username": conn.Username, "connection_id": conn.ID}
	// The grant lock keeps a renewal from writing a new grant after it's
	// revoked and forgotten.
	err = c.cfg.Store.WithGrantLock(ctx, conn.OrgID, conn.ID, func() error {
		// The lock holds a database transaction, so a GitLab that hangs
		// mustn't hold it for as long as the request lives. Nor should a
		// caller giving up leave the grant unrevoked.
		revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitlabRenewTimeout)
		defer cancel()
		if revokeErr := c.revoke(revokeCtx, conn); revokeErr != nil {
			details["revoke_error"] = revokeErr.Error()
		}
		if delErr := c.cfg.Secrets.Delete(ctx, conn.OrgID, conn.ID); delErr != nil {
			return fmt.Errorf("forget the GitLab sign-in: %w", delErr)
		}
		if delErr := c.cfg.Secrets.Delete(ctx, conn.OrgID, webhookSecretKey(conn.ID)); delErr != nil {
			return fmt.Errorf("forget the GitLab webhook secret: %w", delErr)
		}
		return c.cfg.Store.DeleteConnection(ctx, conn.OrgID, conn.ID)
	})
	if err != nil {
		return err
	}
	c.record(ctx, AuditGitLabDisconnect, p, details)
	return nil
}

// SetWebhookSecret creates or replaces the webhook secret of one of the
// caller's organization's connections, and returns it. The bytes are kept
// in Vault and shown only here, as a signing token for GitLab 19 and newer
// and as a secret token for older versions. The audit names the connection
// and never holds either.
func (c *GitLabConnections) SetWebhookSecret(ctx context.Context, p security.Principal, id string, in SetGitLabWebhookInput) (*GitLabWebhookSecret, error) {
	if err := c.cfg.Authz.Require(ctx, p, security.RepositoryConnect, ""); err != nil {
		return nil, err
	}
	conn, err := c.ownConnection(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	key, err := webhookSecretBytes(in.SigningToken)
	if err != nil {
		return nil, err
	}
	if err := c.cfg.Secrets.Put(ctx, conn.OrgID, webhookSecretKey(conn.ID), key); err != nil {
		return nil, fmt.Errorf("keep the GitLab webhook secret: %w", err)
	}
	if err := c.cfg.Store.SetWebhookSet(ctx, conn.OrgID, conn.ID); err != nil {
		// A connection disconnected meanwhile has no use for the secret.
		if errors.Is(err, ErrGitLabConnectionNotFound) {
			if delErr := c.cfg.Secrets.Delete(context.WithoutCancel(ctx), conn.OrgID, webhookSecretKey(conn.ID)); delErr != nil {
				return nil, errors.Join(err, fmt.Errorf("forget the GitLab webhook secret: %w", delErr))
			}
		}
		return nil, err
	}
	c.record(ctx, AuditGitLabWebhookSecret, p, map[string]any{"host": conn.Host, "connection_id": conn.ID})
	return &GitLabWebhookSecret{
		URL:          c.connectionView(conn).WebhookURL,
		SigningToken: signingTokenPrefix + base64.StdEncoding.EncodeToString(key),
		SecretToken:  base64.RawURLEncoding.EncodeToString(key),
	}, nil
}

// webhookSecretBytes reads a pasted signing token, or makes 32 random bytes
// when none was given. The error never quotes the token.
func webhookSecretBytes(token string) ([]byte, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		key := make([]byte, webhookSecretLen)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("make a webhook secret: %w", err)
		}
		return key, nil
	}
	enc, ok := strings.CutPrefix(token, signingTokenPrefix)
	if !ok {
		return nil, ErrGitLabSigningToken
	}
	key, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(key) != webhookSecretLen {
		return nil, ErrGitLabSigningToken
	}
	return key, nil
}

// WebhookTarget finds what a GitLab delivery for connection id is checked
// against: the connection's organization and host, and its webhook secret.
// It takes no principal, since the delivery has none; the ID in the URL
// names the connection, and the secret is what proves the sender. A
// connection that is gone, or has no secret yet, is
// ErrGitLabConnectionNotFound. So is one marked as having a secret that
// Vault doesn't hold: a 404 tells GitLab the hook is unknown, where a 500
// on every delivery would only get the hook disabled. Any other failure
// reading the secret is returned as it is.
func (c *GitLabConnections) WebhookTarget(ctx context.Context, id string) (orgID, host string, secret []byte, err error) {
	conn, err := c.cfg.Store.GetConnection(ctx, id)
	if err != nil {
		return "", "", nil, err
	}
	if !conn.WebhookSet {
		return "", "", nil, ErrGitLabConnectionNotFound
	}
	secret, err = c.cfg.Secrets.Reveal(ctx, conn.OrgID, webhookSecretKey(conn.ID))
	if errors.Is(err, ErrSecretNotFound) {
		return "", "", nil, ErrGitLabConnectionNotFound
	}
	if err != nil {
		return "", "", nil, fmt.Errorf("read the GitLab webhook secret: %w", err)
	}
	if len(secret) == 0 {
		return "", "", nil, ErrGitLabConnectionNotFound
	}
	return conn.OrgID, conn.Host, secret, nil
}

// revoke asks GitLab to stop honoring conn's grant. Its error never holds
// a token: Revoke's errors carry only a status and an OAuth error code.
func (c *GitLabConnections) revoke(ctx context.Context, conn *GitLabConnection) error {
	h, ok, err := c.cfg.Registry.Host(ctx, conn.OrgID, conn.Host)
	if err != nil {
		return err
	}
	if !ok {
		return ErrGitLabHostNotFound
	}
	g, err := c.readGrant(ctx, conn)
	if err != nil {
		return err
	}
	return c.revokeGrant(ctx, h, g)
}

// revokeGrant asks h to stop honoring g. It revokes the refresh token,
// which GitLab revokes whether or not its access token has expired, and
// falls back to the access token only when g has no refresh token.
func (c *GitLabConnections) revokeGrant(ctx context.Context, h GitLabHost, g oauth.Grant) error {
	app, err := c.app(ctx, h)
	if err != nil {
		return err
	}
	if g.RefreshToken != "" {
		return c.client(h).Revoke(ctx, app, g.RefreshToken, GitLabRefreshTokenHint)
	}
	return c.client(h).Revoke(ctx, app, g.AccessToken, GitLabAccessTokenHint)
}

// Repositories lists the projects the organization's live connections can
// read, by host, namespace and path, each once. A lapsed connection, or one
// that lapses on the way, is left out. So is one whose listing fails, such
// as a token GitLab revoked early, so one bad connection doesn't blank the
// rest: the failure goes to the audit, by connection, and is returned only
// when every connection asked failed.
func (c *GitLabConnections) Repositories(ctx context.Context, p security.Principal) ([]GitLabRepository, error) {
	if err := c.cfg.Authz.Require(ctx, p, security.ProjectRead, ""); err != nil {
		return nil, err
	}
	conns, err := c.cfg.Store.ListConnections(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	sortOldestFirst(conns)
	out := []GitLabRepository{}
	seen := map[string]bool{}
	var (
		asked, failed int
		lastErr       error
	)
	for _, conn := range conns {
		if conn.LapsedAt != nil {
			continue
		}
		h, ok, err := c.cfg.Registry.Host(ctx, p.OrgID, conn.Host)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		projects, err := c.listProjects(ctx, h, conn)
		if errors.Is(err, ErrGitLabSignInLapsed) {
			continue
		}
		asked++
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			failed++
			lastErr = err
			c.record(ctx, AuditGitLabListFailed, p, map[string]any{
				"host": conn.Host, "username": conn.Username, "connection_id": conn.ID, "list_error": err.Error(),
			})
			continue
		}
		for _, pr := range projects {
			r, ok := gitlabRepository(h.Host, conn.ID, pr)
			if !ok || seen[strings.ToLower(r.Key)] {
				continue
			}
			seen[strings.ToLower(r.Key)] = true
			out = append(out, r)
		}
	}
	if asked > 0 && failed == asked {
		return nil, lastErr
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !strings.EqualFold(a.Host, b.Host) {
			return strings.ToLower(a.Host) < strings.ToLower(b.Host)
		}
		if !strings.EqualFold(a.Namespace, b.Namespace) {
			return strings.ToLower(a.Namespace) < strings.ToLower(b.Namespace)
		}
		return strings.ToLower(a.FullName) < strings.ToLower(b.FullName)
	})
	return out, nil
}

// listProjects lists the projects conn can read on h. Its errors never
// hold a token.
func (c *GitLabConnections) listProjects(ctx context.Context, h GitLabHost, conn *GitLabConnection) ([]GitLabProject, error) {
	g, err := c.grant(ctx, conn)
	if err != nil {
		return nil, err
	}
	projects, err := c.client(h).Projects(ctx, g.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("list the projects of %s on %s: %w", conn.Username, conn.Host, err)
	}
	return projects, nil
}

// gitlabRepository reads a listed project. One whose path isn't a GitLab
// project path is skipped.
func gitlabRepository(host, connectionID string, pr GitLabProject) (GitLabRepository, bool) {
	repo, err := gitlabPath(host, pr.PathWithNamespace)
	if err != nil {
		return GitLabRepository{}, false
	}
	ns := pr.Namespace.FullPath
	if ns == "" {
		ns = repo.Owner
	}
	name := pr.Name
	if name == "" {
		name = repo.Name
	}
	return GitLabRepository{
		Key: repo.Key(), FullName: repo.FullName(), Name: name, Namespace: ns, Host: repo.Host,
		Connection: connectionID,
		// An internal project needs a sign-in as much as a private one
		// does, and a listing that leaves visibility out is read as
		// needing one too.
		Private:       pr.Visibility != "public",
		DefaultBranch: pr.DefaultBranch,
	}, true
}

// signInHosts are the hosts orgID can sign in through: the instance hosts
// with an OAuth application, then the organization's own when those are on.
func (c *GitLabConnections) signInHosts(ctx context.Context, orgID string) ([]GitLabHost, error) {
	var out []GitLabHost
	for _, h := range c.cfg.Registry.Instance {
		if h.ClientID == "" {
			continue
		}
		h.Instance = true
		h.Host = canonicalGitLabHost(h.Host)
		out = append(out, h)
	}
	if !c.cfg.Registry.AllowOrganizationHosts {
		return out, nil
	}
	own, err := c.cfg.Store.ListHosts(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for _, h := range own {
		out = append(out, *h)
	}
	return out, nil
}

// hostByID finds a host orgID can sign in through by its view ID. Only an
// instance host or orgID's own is ever found, never a bare ID across
// organizations.
func (c *GitLabConnections) hostByID(ctx context.Context, orgID, id string) (GitLabHost, error) {
	if name, ok := strings.CutPrefix(id, instanceHostPrefix); ok {
		h, ok := c.cfg.Registry.instance(name)
		if !ok || h.ClientID == "" {
			return GitLabHost{}, ErrGitLabHostNotFound
		}
		h.Host = canonicalGitLabHost(h.Host)
		return h, nil
	}
	if !c.cfg.Registry.AllowOrganizationHosts {
		return GitLabHost{}, ErrGitLabHostNotFound
	}
	h, err := c.ownHost(ctx, orgID, id)
	if err != nil {
		return GitLabHost{}, err
	}
	return *h, nil
}

// ownHost reads one of orgID's registered hosts.
func (c *GitLabConnections) ownHost(ctx context.Context, orgID, id string) (*GitLabHost, error) {
	hosts, err := c.cfg.Store.ListHosts(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for _, h := range hosts {
		if h.ID == id && h.OrgID == orgID {
			return h, nil
		}
	}
	return nil, ErrGitLabHostNotFound
}

// ownConnection reads one of orgID's connections. Another organization's is
// ErrGitLabConnectionNotFound, as a missing one is.
func (c *GitLabConnections) ownConnection(ctx context.Context, orgID, id string) (*GitLabConnection, error) {
	conn, err := c.cfg.Store.GetConnection(ctx, id)
	if err != nil {
		return nil, err
	}
	if conn.OrgID != orgID {
		return nil, ErrGitLabConnectionNotFound
	}
	return conn, nil
}

// app is the OAuth application Loomplane signs in to h with. An instance
// host's secret is in the settings, an organization host's in Vault.
func (c *GitLabConnections) app(ctx context.Context, h GitLabHost) (oauth.App, error) {
	app := oauth.App{ClientID: h.ClientID}
	if h.Instance {
		for host, secret := range c.cfg.InstanceSecrets {
			if canonicalGitLabHost(host) == canonicalGitLabHost(h.Host) {
				app.ClientSecret = secret
				break
			}
		}
		return app, nil
	}
	secret, err := c.cfg.Secrets.Reveal(ctx, h.OrgID, hostSecretKey(h.ID))
	if err != nil {
		return oauth.App{}, fmt.Errorf("read the OAuth application secret of %s: %w", h.Host, err)
	}
	app.ClientSecret = string(secret)
	return app, nil
}

// client is the API client of h.
func (c *GitLabConnections) client(h GitLabHost) *GitLab {
	if c.cfg.Client != nil {
		return c.cfg.Client(h)
	}
	if h.Instance {
		return NewGitLab(h.WebURL, nil)
	}
	return NewGitLab(h.WebURL, orgClient())
}

func (c *GitLabConnections) record(ctx context.Context, action string, p security.Principal, details map[string]any) {
	if c.cfg.Audit != nil {
		c.cfg.Audit.Record(ctx, action, p, details)
	}
}

func (c *GitLabConnections) connectionView(conn *GitLabConnection) GitLabConnectionView {
	v := GitLabConnectionView{
		ID: conn.ID, Host: conn.Host, Username: conn.Username, AvatarURL: conn.AvatarURL,
		Version: conn.Version, VersionSupported: GitLabVersionSupported(conn.Version),
		Lapsed: conn.LapsedAt != nil, WebhookSet: conn.WebhookSet, CreatedAt: conn.CreatedAt.UTC(),
	}
	if base := strings.TrimRight(c.cfg.WebhookBaseURL, "/"); base != "" {
		v.WebhookURL = base + "/api/v1/gitlab/webhook/" + conn.ID
	}
	return v
}

func hostView(h GitLabHost) GitLabHostView {
	v := GitLabHostView{ID: h.ID, WebURL: h.WebURL, Host: h.Host, Instance: h.Instance, ClientID: h.ClientID}
	if h.Instance {
		v.ID = instanceHostPrefix + h.Host
		return v
	}
	created := h.CreatedAt.UTC()
	v.CreatedAt = &created
	return v
}

// sortOldestFirst orders connections by when they were made.
func sortOldestFirst(conns []*GitLabConnection) {
	sort.SliceStable(conns, func(i, j int) bool { return conns[i].CreatedAt.Before(conns[j].CreatedAt) })
}

func hostSecretKey(hostID string) string { return "host/" + hostID }

func webhookSecretKey(connectionID string) string { return connectionID + "/webhook" }
