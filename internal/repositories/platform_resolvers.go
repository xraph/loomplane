package repositories

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// memo keeps what was built from one saved version of an app, so a GitHub
// App's key is parsed once per save rather than once per call.
type memo[T any] struct {
	mu  sync.Mutex
	sig string
	val T
	ok  bool
}

func (m *memo[T]) get(sig string, build func() (T, bool)) (T, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sig != sig {
		m.sig = sig
		m.val, m.ok = build()
	}
	return m.val, m.ok
}

func signature(a PlatformApp) string {
	return fmt.Sprintf("%d|%s|%s|%d", a.AppID, a.ClientID, a.Slug, a.UpdatedAt.UnixNano())
}

// GitHubAppSource returns the GitHub App saved for the platform, reached at
// GitHub's apiURL and webURL, or nil when there's none or its private key
// doesn't parse. Use it with Connections.UseApp.
func (p *PlatformApps) GitHubAppSource(apiURL, webURL string) func(context.Context) GitHubApp {
	var m memo[*App]
	return func(ctx context.Context) GitHubApp {
		a, ok := p.Get(ctx, PlatformGitHub)
		if !ok || a.AppID == 0 {
			return nil
		}
		app, ok := m.get(signature(a), func() (*App, bool) {
			built, err := NewApp(AppConfig{
				ID: a.AppID, Slug: a.Slug, PrivateKey: a.PrivateKey, ClientID: a.ClientID, ClientSecret: a.ClientSecret,
				APIURL: apiURL, WebURL: webURL,
			})
			if err != nil {
				p.logf("repositories: the saved GitHub App can't be used: %v", err)
				return nil, false
			}
			return built, true
		})
		if !ok {
			return nil // never a typed nil in the interface
		}
		return app
	}
}

// GitHubWebhookSecret returns the webhook secret of the GitHub App saved for
// the platform, empty when there's none.
func (p *PlatformApps) GitHubWebhookSecret(ctx context.Context) []byte {
	a, ok := p.Get(ctx, PlatformGitHub)
	if !ok || a.WebhookSecret == "" {
		return nil
	}
	return []byte(a.WebhookSecret)
}

// BitbucketSource returns a client for the Bitbucket consumer saved for the
// platform, reached at Bitbucket's apiURL and webURL, or nil when there's
// none. Use it with BitbucketConnections.UseConsumer.
func (p *PlatformApps) BitbucketSource(apiURL, webURL string) func(context.Context) BitbucketAPI {
	var m memo[*BitbucketOAuth]
	return func(ctx context.Context) BitbucketAPI {
		a, ok := p.Get(ctx, PlatformBitbucket)
		if !ok {
			return nil
		}
		api, _ := m.get(signature(a), func() (*BitbucketOAuth, bool) {
			return NewBitbucketOAuth(BitbucketOAuthConfig{
				APIURL: apiURL, WebURL: webURL, ClientID: a.ClientID, ClientSecret: a.ClientSecret,
			}), true
		})
		return api
	}
}

// GitLabSource returns gitlab.com, reached at webURL, as an operator host
// with the OAuth application saved for the platform, or nothing when there's
// none. Use it as GitLabRegistry.Managed.
func (p *PlatformApps) GitLabSource(webURL string) func(context.Context) []GitLabInstanceApp {
	webURL = strings.TrimRight(webURL, "/")
	if webURL == "" {
		webURL = DefaultGitLabURL
	}
	return func(ctx context.Context) []GitLabInstanceApp {
		a, ok := p.Get(ctx, PlatformGitLab)
		if !ok {
			return nil
		}
		return []GitLabInstanceApp{{
			Host:   GitLabHost{WebURL: webURL, Host: HostGitLab, ClientID: a.ClientID, Instance: true},
			Secret: a.ClientSecret,
		}}
	}
}
