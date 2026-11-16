package connections

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/forge/extensions/dashboard/contract"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/repositories"
)

// Audit actions for the repository hosts' apps.
const (
	auditRepositoryAppSet    = "repository_app.set"
	auditRepositoryAppDelete = "repository_app.delete"
)

// The secret fields an app can hold, as SavedSecrets names them.
const (
	secretClient  = "clientSecret"
	secretKey     = "privateKey"
	secretWebhook = "webhookSecret"
)

// repositories are the repository hosts that sign in through OAuth: the
// GitHub App, gitlab.com and Bitbucket, whose apps the console can set when
// configuration doesn't, and the self-managed GitLabs configuration lists,
// which it only shows.
func (c *console) repositories(ctx context.Context) ([]ConsoleIntegration, error) {
	saved := map[string]repositories.PlatformApp{}
	if c.repos != nil {
		got, err := c.repos.Fresh(ctx)
		if err != nil {
			return nil, c.fail("read the repository apps", err)
		}
		saved = got
	}
	base := strings.TrimRight(c.cfg.PublicURL, "/")
	repo := func(key, name, callback string, prov ConsoleProvider) ConsoleIntegration {
		it := ConsoleIntegration{
			Key: key, Name: name, Kind: KindRepository, Source: SourceNone, Editable: c.repos != nil,
			CallbackURL: base + callback, Provider: prov,
		}
		if a, ok := saved[key]; ok && a.ClientID != "" {
			fromPlatform(&it, a)
		}
		return it
	}
	fixed := func(it *ConsoleIntegration, clientID string) {
		*it = ConsoleIntegration{
			Key: it.Key, Name: it.Name, Kind: it.Kind, Source: SourceConfig, ClientID: clientID,
			CallbackURL: it.CallbackURL, Provider: it.Provider,
		}
	}

	gh := c.cfg.GitHub
	github := repo(keyGitHub, "GitHub App", "/integrations/github/setup", githubProvider(gh.WebURL, gh.AppSlug, base))
	if gh.AppConfigured() {
		fixed(&github, gh.ClientID)
		github.AppID, github.Slug = gh.AppID, gh.AppSlug
	} else if github.Slug != "" {
		github.Provider = githubProvider(gh.WebURL, github.Slug, base)
	}
	gitlab := repo(keyGitLab, "GitLab.com", "/integrations/gitlab/callback", gitLabProvider("GitLab", c.cfg.GitLab.APIURL))
	if c.cfg.GitLab.ClientID != "" {
		fixed(&gitlab, c.cfg.GitLab.ClientID)
	}
	out := []ConsoleIntegration{github, gitlab}
	for _, h := range c.cfg.GitLab.Hosts {
		key := gitLabHostKey(h.WebURL)
		name := "GitLab at " + strings.TrimPrefix(key, gitLabHostPrefix)
		it := ConsoleIntegration{
			Key: key, Name: name, Kind: KindRepository, Source: SourceNone,
			CallbackURL: base + "/integrations/gitlab/callback", Provider: gitLabProvider(name, h.WebURL),
		}
		if h.ClientID != "" {
			it.Source, it.ClientID = SourceConfig, h.ClientID
		}
		out = append(out, it)
	}
	bitbucket := repo(keyBitbucket, "Bitbucket", "/integrations/bitbucket/callback", bitbucketProvider)
	if c.cfg.Bitbucket.Configured() {
		fixed(&bitbucket, c.cfg.Bitbucket.ClientID)
	}
	return append(out, bitbucket), nil
}

// fromPlatform fills it from the app saved for the platform.
func fromPlatform(it *ConsoleIntegration, a repositories.PlatformApp) {
	it.Source, it.ClientID, it.Slug, it.AppID, it.UpdatedBy = SourcePlatform, a.ClientID, a.Slug, a.AppID, a.UpdatedBy
	if !a.UpdatedAt.IsZero() {
		at := a.UpdatedAt.UTC()
		it.UpdatedAt = &at
	}
	for name, set := range map[string]bool{
		secretClient: a.ClientSecret != "", secretKey: a.PrivateKey != "", secretWebhook: a.WebhookSecret != "",
	} {
		if set {
			it.SavedSecrets = append(it.SavedSecrets, name)
		}
	}
	// Map order isn't stable; the wire's is.
	slices.Sort(it.SavedSecrets)
}

// isRepository reports whether key is a repository host's.
func (c *console) isRepository(key string) bool {
	key = strings.TrimSpace(key)
	switch key {
	case keyGitHub, keyGitLab, keyBitbucket:
		return true
	}
	for _, h := range c.cfg.GitLab.Hosts {
		if gitLabHostKey(h.WebURL) == key {
			return true
		}
	}
	return false
}

// configuredRepository reports whether configuration sets key's app, so the
// console can't.
func (c *console) configuredRepository(key string) bool {
	switch key {
	case keyGitHub:
		return c.cfg.GitHub.AppConfigured()
	case keyGitLab:
		return c.cfg.GitLab.ClientID != ""
	case keyBitbucket:
		return c.cfg.Bitbucket.Configured()
	}
	return true // a self-managed GitLab lives in configuration
}

// editableRepository is key, when the console can set its app.
func (c *console) editableRepository(key string) (string, error) {
	key = strings.TrimSpace(key)
	if strings.HasPrefix(key, gitLabHostPrefix) {
		return "", &contract.Error{Code: contract.CodeConflict,
			Message: "Self-managed GitLabs are set in Loomplane's configuration (gitlab.hosts). Change them there and restart loomplane and loomplane-worker."}
	}
	if c.configuredRepository(key) {
		return "", errLocked
	}
	if c.repos == nil {
		return "", &contract.Error{Code: contract.CodeUnavailable, Message: "This server can't keep repository apps."}
	}
	return key, nil
}

var errLocked = &contract.Error{Code: contract.CodeConflict,
	Message: "This app is set in Loomplane's configuration, which wins over the console. Change it there, or remove it there to manage it here."}

func (c *console) saveRepository(ctx context.Context, in SaveInput, p contract.Principal) (ackResponse, error) {
	key, err := c.editableRepository(in.Key)
	if err != nil {
		return ackResponse{}, err
	}
	current, err := c.repos.Fresh(ctx)
	if err != nil {
		return ackResponse{}, c.fail("read the repository apps", err)
	}
	prev := current[key]
	next := repositories.PlatformApp{
		ClientID: strings.TrimSpace(in.ClientID), ClientSecret: strings.TrimSpace(in.ClientSecret),
		UpdatedBy: p.User.Subject, UpdatedAt: c.now().UTC(),
	}
	if next.ClientID == "" {
		return ackResponse{}, badField("clientId", "Enter the client ID the host gave you.")
	}
	// A secret belongs to its client: a new client ID needs its own.
	if next.ClientSecret == "" && prev.ClientID == next.ClientID {
		next.ClientSecret = prev.ClientSecret
	}
	if next.ClientSecret == "" {
		return ackResponse{}, badField("clientSecret", "Enter the client secret the host gave you.")
	}
	if key == keyGitHub {
		if err := c.githubFields(in, prev, &next); err != nil {
			return ackResponse{}, err
		}
	}
	if err := c.repos.Save(ctx, key, next); err != nil {
		return ackResponse{}, c.fail("save the repository app", err)
	}
	c.record(ctx, auditRepositoryAppSet, p, key, next.ClientID)
	return ackResponse{Key: key}, nil
}

// githubFields fills the GitHub App's own values into next, keeping prev's
// private key while the App ID is the same, and checks the key parses.
func (c *console) githubFields(in SaveInput, prev repositories.PlatformApp, next *repositories.PlatformApp) error {
	id, err := strconv.ParseInt(strings.TrimSpace(in.AppID), 10, 64)
	if err != nil || id <= 0 {
		return badField("appId", "Enter the App ID from the GitHub App's page, a number.")
	}
	next.AppID, next.Slug = id, strings.TrimSpace(in.Slug)
	if next.Slug == "" {
		return badField("slug", "Enter the App's slug, the name in github.com/apps/<slug>.")
	}
	next.PrivateKey = strings.TrimSpace(in.PrivateKey)
	if next.PrivateKey == "" && prev.AppID == id {
		next.PrivateKey = prev.PrivateKey
	}
	if next.PrivateKey == "" {
		return badField("privateKey", "Paste the private key GitHub downloaded for the App.")
	}
	next.WebhookSecret = strings.TrimSpace(in.WebhookSecret)
	if next.WebhookSecret == "" && !in.ClearWebhookSecret {
		next.WebhookSecret = prev.WebhookSecret
	}
	if _, err := repositories.NewApp(repositories.AppConfig{
		ID: next.AppID, Slug: next.Slug, PrivateKey: next.PrivateKey, ClientID: next.ClientID, ClientSecret: next.ClientSecret,
		APIURL: c.cfg.GitHub.APIURL, WebURL: c.cfg.GitHub.WebURL,
	}); err != nil {
		return badField("privateKey", sentence(err.Error()))
	}
	return nil
}

func (c *console) removeRepository(ctx context.Context, key string, p contract.Principal) (ackResponse, error) {
	key, err := c.editableRepository(key)
	if err != nil {
		return ackResponse{}, err
	}
	current, err := c.repos.Fresh(ctx)
	if err != nil {
		return ackResponse{}, c.fail("read the repository apps", err)
	}
	prev, ok := current[key]
	if !ok {
		return ackResponse{}, &contract.Error{Code: contract.CodeNotFound, Message: "There's no app saved here for it."}
	}
	if err := c.repos.Remove(ctx, key); err != nil {
		return ackResponse{}, c.fail("remove the repository app", err)
	}
	c.record(ctx, auditRepositoryAppDelete, p, key, prev.ClientID)
	return ackResponse{Key: key}, nil
}

func (c *console) record(ctx context.Context, action string, p contract.Principal, key, clientID string) {
	if c.audit != nil {
		c.audit.RecordRepositoryApp(ctx, action, p.User.Subject, key, clientID)
	}
}

// repoApp is the app a check uses: the values typed, with the saved secrets
// where they were left blank, or with none typed, the app in use.
type repoApp struct {
	clientID, clientSecret string
	appID                  int64
	slug, privateKey       string
}

func (c *console) checkRepository(ctx context.Context, in CheckInput) (connections.AppCheck, error) {
	key := strings.TrimSpace(in.Key)
	app, err := c.repoAppToCheck(ctx, key, in)
	if err != nil {
		return connections.AppCheck{}, err
	}
	base := strings.TrimRight(c.cfg.PublicURL, "/")
	var res repositories.ClientCheck
	switch key {
	case keyGitHub:
		res = c.checkGitHub(ctx, app)
	case keyBitbucket:
		res = repositories.CheckClient(ctx, repositories.ClientProbe{
			Host: "Bitbucket", TokenURL: strings.TrimRight(c.cfg.Bitbucket.WebURL, "/") + "/site/oauth2/access_token",
			ClientID: app.clientID, ClientSecret: app.clientSecret, BasicAuth: true, HTTP: c.http,
		})
	default:
		webURL := strings.TrimRight(c.cfg.GitLab.APIURL, "/")
		host := "GitLab"
		for _, h := range c.cfg.GitLab.Hosts {
			if gitLabHostKey(h.WebURL) == key {
				webURL, host = strings.TrimRight(h.WebURL, "/"), strings.TrimPrefix(key, gitLabHostPrefix)
			}
		}
		res = repositories.CheckClient(ctx, repositories.ClientProbe{
			Host: host, TokenURL: webURL + "/oauth/token", RedirectURL: base + "/integrations/gitlab/callback",
			ClientID: app.clientID, ClientSecret: app.clientSecret, HTTP: c.http,
		})
	}
	return connections.AppCheck{Result: res.Result, Summary: res.Summary, Detail: res.Detail, CheckedAt: c.now().UTC()}, nil
}

// repoAppToCheck resolves what a check of key sends.
func (c *console) repoAppToCheck(ctx context.Context, key string, in CheckInput) (repoApp, error) {
	var saved repositories.PlatformApp
	if c.repos != nil && !c.configuredRepository(key) {
		current, err := c.repos.Fresh(ctx)
		if err != nil {
			return repoApp{}, c.fail("read the repository apps", err)
		}
		saved = current[key]
	}
	typed := strings.TrimSpace(in.ClientID)
	if typed == "" {
		app, ok := c.repoAppInUse(key, saved)
		if !ok {
			return repoApp{}, badField("clientId", "It has no app yet. Enter one to check it.")
		}
		return app, nil
	}
	if c.configuredRepository(key) {
		return repoApp{}, errLocked
	}
	app := repoApp{clientID: typed, clientSecret: strings.TrimSpace(in.ClientSecret)}
	if app.clientSecret == "" && saved.ClientID == typed {
		app.clientSecret = saved.ClientSecret
	}
	if app.clientSecret == "" {
		return repoApp{}, badField("clientSecret", "Enter the client secret to check it.")
	}
	if key == keyGitHub {
		if id, err := strconv.ParseInt(strings.TrimSpace(in.AppID), 10, 64); err == nil && id > 0 {
			app.appID = id
			app.privateKey = strings.TrimSpace(in.PrivateKey)
			if app.privateKey == "" && saved.AppID == id {
				app.privateKey = saved.PrivateKey
			}
		}
	}
	return app, nil
}

// repoAppInUse is key's app as Loomplane uses it now: configuration's, or
// the one saved for the platform.
func (c *console) repoAppInUse(key string, saved repositories.PlatformApp) (repoApp, bool) {
	switch {
	case key == keyGitHub && c.cfg.GitHub.AppConfigured():
		g := c.cfg.GitHub
		return repoApp{clientID: g.ClientID, clientSecret: g.ClientSecret, appID: g.AppID, slug: g.AppSlug, privateKey: c.githubKey()}, true
	case key == keyGitLab && c.cfg.GitLab.ClientID != "":
		return repoApp{clientID: c.cfg.GitLab.ClientID, clientSecret: c.cfg.GitLab.ClientSecret}, true
	case key == keyBitbucket && c.cfg.Bitbucket.Configured():
		return repoApp{clientID: c.cfg.Bitbucket.ClientID, clientSecret: c.cfg.Bitbucket.ClientSecret}, true
	case strings.HasPrefix(key, gitLabHostPrefix):
		for _, h := range c.cfg.GitLab.Hosts {
			if gitLabHostKey(h.WebURL) == key && h.ClientID != "" {
				return repoApp{clientID: h.ClientID, clientSecret: h.ClientSecret}, true
			}
		}
		return repoApp{}, false
	}
	if saved.ClientID == "" {
		return repoApp{}, false
	}
	return repoApp{
		clientID: saved.ClientID, clientSecret: saved.ClientSecret,
		appID: saved.AppID, slug: saved.Slug, privateKey: saved.PrivateKey,
	}, true
}

// githubKey is configuration's App key, read from its file when it's there.
func (c *console) githubKey() string {
	if c.cfg.GitHub.PrivateKeyFile != "" && c.readFile != nil {
		if raw, err := c.readFile(c.cfg.GitHub.PrivateKeyFile); err == nil {
			return string(raw)
		}
	}
	return c.cfg.GitHub.PrivateKey
}

// checkGitHub checks the App's client with GitHub's token endpoint, and,
// when it has an App ID and key, that GitHub takes them as a pair.
func (c *console) checkGitHub(ctx context.Context, app repoApp) repositories.ClientCheck {
	res := repositories.CheckClient(ctx, repositories.ClientProbe{
		Host: "GitHub", TokenURL: strings.TrimRight(c.cfg.GitHub.WebURL, "/") + "/login/oauth/access_token",
		ClientID: app.clientID, ClientSecret: app.clientSecret, HTTP: c.http,
	})
	if app.appID == 0 || app.privateKey == "" {
		return res
	}
	gh, err := repositories.NewApp(repositories.AppConfig{
		ID: app.appID, Slug: app.slug, PrivateKey: app.privateKey, ClientID: app.clientID, ClientSecret: app.clientSecret,
		APIURL: c.cfg.GitHub.APIURL, WebURL: c.cfg.GitHub.WebURL,
	})
	if err != nil {
		return repositories.ClientCheck{Result: repositories.ClientRejected, Summary: sentence(err.Error())}
	}
	ictx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	slug, err := gh.Identify(ictx)
	switch {
	case errors.Is(err, repositories.ErrAppKeyRejected):
		return repositories.ClientCheck{Result: repositories.ClientRejected, Summary: sentence(err.Error())}
	case err != nil:
		return repositories.ClientCheck{Result: repositories.ClientUnreachable, Summary: "Couldn't ask GitHub about the App ID and private key.", Detail: err.Error()}
	case app.slug != "" && !strings.EqualFold(slug, app.slug):
		return repositories.ClientCheck{Result: repositories.ClientRejected,
			Summary: "GitHub says App " + strconv.FormatInt(app.appID, 10) + " is " + slug + ", not " + app.slug + ". Fix the slug, or the App ID."}
	}
	if res.Result == repositories.ClientAccepted {
		res.Summary = "GitHub accepted the App ID and private key, and the client. " + res.Summary
	}
	return res
}
