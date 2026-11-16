package connections

import (
	"net/url"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/connections"
)

// ConsoleProvider is who issues an integration's OAuth app, for the
// console's setup steps: where to register it, what to register and what
// trips people up.
type ConsoleProvider struct {
	Name string `json:"name"`
	// ConsoleURL is the provider's page for registering the app, empty when
	// there's no stable one.
	ConsoleURL string `json:"consoleUrl,omitempty"`
	// DocsURL is Loomplane's own guide to it.
	DocsURL string   `json:"docsUrl,omitempty"`
	Notes   []string `json:"notes,omitempty"`
	// Scopes are the ones Loomplane asks for, to allow on the app.
	Scopes []string `json:"scopes,omitempty"`
	// Fields are what the app needs besides its client ID and secret, in
	// the order a form asks for them.
	Fields []ConsoleField `json:"fields,omitempty"`
	// ChangeWarning says what breaks when the app is replaced or removed,
	// for a host whose connections belong to their app. Empty for a cloud,
	// whose warning counts its sign-ins.
	ChangeWarning string `json:"changeWarning,omitempty"`
}

// ConsoleField is one more value an app needs. Name is its SaveInput field.
type ConsoleField struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Hint  string `json:"hint,omitempty"`
	// Kind is text, number, secret, or pem: a secret pasted over several
	// lines.
	Kind     string `json:"kind"`
	Optional bool   `json:"optional,omitempty"`
}

// The kinds of field.
const (
	FieldText   = "text"
	FieldNumber = "number"
	FieldSecret = "secret"
	FieldPEM    = "pem"
)

const docsBase = "https://loomplane.dev/docs"

func cloudSignInDocs(anchor string) string {
	return docsBase + "/clouds/cloud-sign-in#" + anchor
}

// cloudProviders are the clouds people sign in to, keyed by connection
// cloud. Keep the notes in step with apps/docs/content/docs/clouds/cloud-sign-in.mdx.
var cloudProviders = map[string]ConsoleProvider{
	connections.DigitalOcean: {
		Name:       "DigitalOcean",
		ConsoleURL: "https://cloud.digitalocean.com/account/api/applications",
		DocsURL:    cloudSignInDocs("digitalocean"),
		Scopes:     []string{"read", "write"},
		Notes: []string{
			"Open API, then OAuth Applications, and register a new application with the callback URL below.",
		},
	},
	connections.Vercel: {
		Name:       "Vercel",
		ConsoleURL: "https://vercel.com/dashboard/integrations/console",
		DocsURL:    cloudSignInDocs("vercel"),
		Notes: []string{
			"Make an Integration, not a plain OAuth app, and set its redirect URL to the callback URL below.",
			"Give it read and write on projects, project environment variables, deployments and domains, and read on the team and the user.",
			"Vercel's grants don't depend on the Integration once issued, so changing it never makes anyone sign in again.",
		},
		Fields: []ConsoleField{{
			Name: "slug", Label: "Integration slug", Kind: FieldText,
			Hint: "The last part of the Integration's address: for vercel.com/integrations/loomplane-acme, type loomplane-acme.",
		}},
	},
	connections.GCP: {
		Name:       "Google Cloud",
		ConsoleURL: "https://console.cloud.google.com/apis/credentials",
		DocsURL:    cloudSignInDocs("google-cloud"),
		Scopes:     []string{"openid", "email", gcp.Scope},
		Notes: []string{
			"Set up the OAuth consent screen first and add the cloud-platform scope under its data access.",
			"Create an OAuth client ID of type Web application, with the callback URL under Authorized redirect URIs.",
			"Publish the app: while it's in testing, Google ends every sign-in after 7 days. An External app also needs Google's verification; an Internal one doesn't.",
		},
	},
	connections.Azure: {
		Name:       "Microsoft Entra ID",
		ConsoleURL: "https://portal.azure.com/#view/Microsoft_AAD_RegisteredApps/ApplicationsListBlade",
		DocsURL:    cloudSignInDocs("azure"),
		Scopes:     []string{"openid", "profile", "offline_access", azure.DefaultManagement + "/user_impersonation"},
		Notes: []string{
			"Register an application (multitenant if people sign in from other tenants) and add a Web platform with the callback URL as its redirect URI.",
			"Add a client secret and copy its Value, not its Secret ID. It expires; note the date.",
			"Under API permissions, add Azure Service Management with the delegated user_impersonation permission.",
		},
	},
	connections.Cloudflare: {
		Name:       "Cloudflare",
		ConsoleURL: "https://developers.cloudflare.com/fundamentals/oauth/create-an-oauth-client/",
		DocsURL:    cloudSignInDocs("cloudflare"),
		Scopes:     []string{"account-settings.read", "offline_access", "zone.read", "dns.write", "workers-r2.write"},
		Notes: []string{
			"Under Manage Account, then OAuth clients, create a client with Authorization Code and Refresh Token grants and client_secret_basic token authentication.",
			"zone.read and dns.write are for DNS, workers-r2.write for R2 storage; register the ones you offer.",
			"A private client works only for members of its own Cloudflare account. Making one public is permanent.",
		},
	},
}

// repository hosts' integration keys.
const (
	keyGitHub        = "github"
	keyGitLab        = "gitlab"
	keyBitbucket     = "bitbucket"
	gitLabHostPrefix = "gitlab-host:"
)

// gitLabHostKey is the key of a self-managed GitLab from config.
func gitLabHostKey(webURL string) string {
	host := webURL
	if u, err := url.Parse(webURL); err == nil && u.Host != "" {
		host = u.Host
	}
	return gitLabHostPrefix + strings.ToLower(host)
}

// githubProvider is the GitHub App's setup, on GitHub or GitHub Enterprise
// Server at webURL, for a Loomplane at publicURL.
func githubProvider(webURL, slug, publicURL string) ConsoleProvider {
	consoleURL := strings.TrimRight(webURL, "/") + "/settings/apps/new"
	if slug != "" {
		consoleURL = strings.TrimRight(webURL, "/") + "/settings/apps/" + url.PathEscape(slug)
	}
	return ConsoleProvider{
		Name:       "GitHub",
		ConsoleURL: consoleURL,
		DocsURL:    docsBase + "/guides/private-repositories",
		Notes: []string{
			"Make a GitHub App, not an OAuth app, with the callback URL below and Request user authorization (OAuth) during installation turned on.",
			"Give it Contents and Metadata read-only, and Issues read and write only if you run Loomplane's agent.",
			"For push events, make its webhook active at " + strings.TrimRight(publicURL, "/") + "/api/v1/github/webhook with a secret you generate, subscribed to Push.",
			"On its page, note the App ID and Client ID, generate a client secret and a private key, and paste them here.",
		},
		Fields: []ConsoleField{
			{Name: "appId", Label: "App ID", Kind: FieldNumber},
			{Name: "slug", Label: "App slug", Kind: FieldText, Hint: "The name in its address: github.com/apps/<slug>."},
			{Name: "privateKey", Label: "Private key", Kind: FieldPEM, Hint: "The contents of the .pem file GitHub downloaded."},
			{
				Name: "webhookSecret", Label: "Webhook secret", Kind: FieldSecret, Optional: true,
				Hint: "The secret on the App's webhook. Without one, push events are turned off.",
			},
		},
		ChangeWarning: "Organizations' installations belong to the App. With a different App ID, each organization installs the new App and connects it again.",
	}
}

func gitLabProvider(name, webURL string) ConsoleProvider {
	return ConsoleProvider{
		Name:       name,
		ConsoleURL: strings.TrimRight(webURL, "/") + "/-/user_settings/applications",
		DocsURL:    docsBase + "/guides/gitlab-repositories",
		Scopes:     []string{"read_api"},
		Notes: []string{
			"Add a confidential application (under a group, if a group should own it) with the callback URL below as its Redirect URI and read_api as its only scope.",
			"GitLab shows the secret once.",
		},
		ChangeWarning: "GitLab sign-ins belong to their application. With a different one, each GitLab connection made with the old one has to sign in again.",
	}
}

var bitbucketProvider = ConsoleProvider{
	Name:    "Bitbucket",
	DocsURL: docsBase + "/guides/bitbucket-repositories",
	Notes: []string{
		"In any workspace's Settings, open OAuth consumers and add one with the callback URL below.",
		"Give it Account read, Repositories read and Webhooks read and write, and nothing else.",
		"Its Key is the client ID.",
	},
	ChangeWarning: "Workspace sign-ins belong to their consumer. With a different one, each connected workspace has to be connected again.",
}
