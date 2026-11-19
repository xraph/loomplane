package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	vaultcore "github.com/xraph/vault"
	"github.com/xraph/vault/scope"
	"github.com/xraph/vault/secret"
)

// The repository hosts whose apps the platform can set at runtime, from the
// instance console, when Loomplane's configuration doesn't set them.
const (
	// PlatformGitHub is the GitHub App.
	PlatformGitHub = "github"
	// PlatformGitLab is gitlab.com's OAuth application.
	PlatformGitLab = "gitlab"
	// PlatformBitbucket is Bitbucket Cloud's OAuth consumer.
	PlatformBitbucket = "bitbucket"
)

// PlatformAppsVaultAppID is the Vault app the platform's repository apps
// live under, one secret per host key. It's apart from every
// organization's credentials, so no listing of those can show one.
const PlatformAppsVaultAppID = "loomplane-repository-apps"

// PlatformAppsVaultKeyLabel derives the key the platform's repository apps
// are sealed with from secret_key.
const PlatformAppsVaultKeyLabel = "loomplane/repository apps v1"

// PlatformApp is the app the platform reaches a repository host with, as
// it was saved from the console. Only the GitHub App has an AppID, Slug,
// PrivateKey and WebhookSecret.
type PlatformApp struct {
	ClientID      string
	ClientSecret  string
	AppID         int64
	Slug          string
	PrivateKey    string
	WebhookSecret string
	UpdatedBy     string
	UpdatedAt     time.Time
}

// String keeps the secrets out of logs and error messages.
func (a PlatformApp) String() string {
	return fmt.Sprintf("repositories.PlatformApp{ClientID:%q AppID:%d Slug:%q secret:%t private_key:%t webhook_secret:%t}",
		a.ClientID, a.AppID, a.Slug, a.ClientSecret != "", a.PrivateKey != "", a.WebhookSecret != "")
}

// GoString is String, so %#v hides the secrets too.
func (a PlatformApp) GoString() string { return a.String() }

// PlatformAppStore keeps the platform's repository apps by host key.
// Implementations must be safe for concurrent use.
type PlatformAppStore interface {
	List(ctx context.Context) (map[string]PlatformApp, error)
	Put(ctx context.Context, key string, a PlatformApp) error
	Delete(ctx context.Context, key string) error
}

// platformAppsTTL is how long a process trusts its copy of the apps before
// reading them again, so a save in one process reaches the others within
// it. platformAppsLoadTimeout bounds one read.
const (
	platformAppsTTL         = 30 * time.Second
	platformAppsLoadTimeout = 5 * time.Second
)

// ErrNoPlatformApps is a change with nowhere to keep it.
var ErrNoPlatformApps = errors.New("this server has nowhere to keep repository apps")

// PlatformApps is the platform's repository apps, read through a cache. A
// nil *PlatformApps has none. A save in this process is live in it at once.
type PlatformApps struct {
	store PlatformAppStore
	now   func() time.Time
	logf  func(format string, args ...any)

	mu       sync.Mutex
	cache    map[string]PlatformApp
	loadedAt time.Time
	loaded   bool
}

// NewPlatformApps reads and writes the apps through store.
func NewPlatformApps(store PlatformAppStore) *PlatformApps {
	return &PlatformApps{store: store, now: time.Now, logf: func(string, ...any) {}}
}

// SetLogf sets where a failed read of the store is reported.
func (p *PlatformApps) SetLogf(fn func(format string, args ...any)) {
	if p != nil && fn != nil {
		p.logf = fn
	}
}

// Get is the app saved for key, from the cache. It's read again once it's
// platformAppsTTL old; a read that fails keeps what was cached and waits
// platformAppsTTL before trying again, so a store outage never stalls every
// call that needs an app.
func (p *PlatformApps) Get(_ context.Context, key string) (PlatformApp, bool) {
	if p == nil {
		return PlatformApp{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.loaded || p.now().Sub(p.loadedAt) >= platformAppsTTL {
		// Its own context: a caller that gives up mustn't leave the cache
		// empty for everyone else.
		ctx, cancel := context.WithTimeout(context.Background(), platformAppsLoadTimeout)
		got, err := p.store.List(ctx)
		cancel()
		p.loadedAt, p.loaded = p.now(), true
		if err != nil {
			p.logf("repositories: read the platform's repository apps: %v", err)
		} else {
			p.cache = got
		}
	}
	a, ok := p.cache[key]
	return a, ok && a.ClientID != ""
}

// Fresh reads every app from the store, not the cache, and refreshes the
// cache with them.
func (p *PlatformApps) Fresh(ctx context.Context) (map[string]PlatformApp, error) {
	if p == nil {
		return map[string]PlatformApp{}, nil
	}
	got, err := p.store.List(ctx)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cache, p.loadedAt, p.loaded = got, p.now(), true
	return maps.Clone(got), nil
}

// Save keeps a for key and makes it live in this process at once.
func (p *PlatformApps) Save(ctx context.Context, key string, a PlatformApp) error {
	if p == nil {
		return ErrNoPlatformApps
	}
	if err := p.store.Put(ctx, key, a); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	next := maps.Clone(p.cache)
	if next == nil {
		next = map[string]PlatformApp{}
	}
	next[key] = a
	p.cache = next
	return nil
}

// Remove deletes key's app. One already gone is not an error.
func (p *PlatformApps) Remove(ctx context.Context, key string) error {
	if p == nil {
		return ErrNoPlatformApps
	}
	if err := p.store.Delete(ctx, key); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	next := maps.Clone(p.cache)
	delete(next, key)
	p.cache = next
	return nil
}

// VaultPlatformApps keeps the platform's repository apps in Vault, sealed
// with a key derived with PlatformAppsVaultKeyLabel, one secret per host.
type VaultPlatformApps struct {
	svc *secret.Service
}

var _ PlatformAppStore = (*VaultPlatformApps)(nil)

// NewVaultPlatformApps keeps the apps through svc, which should be scoped to
// PlatformAppsVaultAppID.
func NewVaultPlatformApps(svc *secret.Service) *VaultPlatformApps {
	return &VaultPlatformApps{svc: svc}
}

// storedPlatformApp is the secret's value, with its own JSON names.
type storedPlatformApp struct {
	ClientID      string    `json:"client_id"`
	ClientSecret  string    `json:"client_secret,omitempty"`
	AppID         int64     `json:"app_id,omitempty"`
	Slug          string    `json:"slug,omitempty"`
	PrivateKey    string    `json:"private_key,omitempty"`
	WebhookSecret string    `json:"webhook_secret,omitempty"`
	UpdatedBy     string    `json:"updated_by,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// platformLevel drops any tenant from ctx: the apps belong to the platform,
// and every process, whatever request it serves, reads the same ones.
func platformLevel(ctx context.Context) context.Context {
	return scope.WithTenantID(ctx, "")
}

// List implements PlatformAppStore.
func (s *VaultPlatformApps) List(ctx context.Context) (map[string]PlatformApp, error) {
	ctx = platformLevel(ctx)
	metas, err := s.svc.List(ctx, PlatformAppsVaultAppID, secret.ListOpts{Limit: 100})
	if err != nil {
		return nil, fmt.Errorf("list repository apps: %w", err)
	}
	out := make(map[string]PlatformApp, len(metas))
	for _, m := range metas {
		sec, err := s.svc.Get(ctx, m.Key, PlatformAppsVaultAppID)
		if errors.Is(err, vaultcore.ErrSecretNotFound) {
			continue // deleted since the listing
		}
		if err != nil {
			return nil, fmt.Errorf("read repository app %s: %w", m.Key, err)
		}
		var v storedPlatformApp
		if err := json.Unmarshal(sec.Value, &v); err != nil {
			// Never wrap the JSON error: it can quote the bytes it choked on.
			return nil, fmt.Errorf("repository app %s is unreadable", m.Key)
		}
		out[m.Key] = PlatformApp(v)
	}
	return out, nil
}

// Put implements PlatformAppStore.
func (s *VaultPlatformApps) Put(ctx context.Context, key string, a PlatformApp) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("a repository app needs a key")
	}
	a.UpdatedAt = a.UpdatedAt.UTC()
	raw, err := json.Marshal(storedPlatformApp(a)) // #nosec G117 -- the app is the secret, bound for Vault, never a response.
	if err != nil {
		return fmt.Errorf("encode repository app: %w", err)
	}
	if _, err := s.svc.Set(platformLevel(ctx), key, raw, PlatformAppsVaultAppID); err != nil {
		return fmt.Errorf("save repository app %s: %w", key, err)
	}
	return nil
}

// Delete implements PlatformAppStore.
func (s *VaultPlatformApps) Delete(ctx context.Context, key string) error {
	err := s.svc.Delete(platformLevel(ctx), key, PlatformAppsVaultAppID)
	if err != nil && !errors.Is(err, vaultcore.ErrSecretNotFound) {
		return fmt.Errorf("delete repository app %s: %w", key, err)
	}
	return nil
}
