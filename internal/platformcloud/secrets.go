package platformcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/registry"
)

// Secrets keeps the platform Cloud's credentials: each host's client key and
// the registry login. It is a platform-scoped Vault app in production.
type Secrets interface {
	Put(ctx context.Context, key string, value []byte) error
	Reveal(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

const (
	hostSecretPrefix = "platform-cloud/host/"
	registrySecret   = "platform-cloud/registry"
)

// WithSecrets sets where the service keeps credentials and returns it.
func (s *Service) WithSecrets(x Secrets) *Service {
	s.secrets = x
	return s
}

func (s *Service) secretStore() (Secrets, error) {
	if s.secrets == nil {
		return nil, errors.New("platformcloud: no secret store is configured")
	}
	return s.secrets, nil
}

type registryCredential struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Token    string `json:"token,omitempty"`
}

// errRegistryLogin refuses a platform registry credential that can't mint
// repository-scoped tokens. A direct token would reach every workspace's
// repository from any VM it was handed to.
var errRegistryLogin = errors.New("platformcloud: the platform registry needs a username and password its token service mints repository-scoped tokens for; a registry token alone is refused")

// SetRegistrySecret stores the credential platform builds use to push to the
// registry named in the instance settings. It replaces the previous one.
// Only a username and password are taken: the registry's token service mints
// each build and deploy a token for one workspace repository from them.
func (s *Service) SetRegistrySecret(ctx context.Context, _ /* actor */, username, password, token string) error {
	if username == "" || password == "" || token != "" {
		return errRegistryLogin
	}
	vault, err := s.secretStore()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(registryCredential{Username: username, Password: password, Token: token}) //nolint:gosec // G117: This credential is serialized only for Vault storage.
	if err != nil {
		return err
	}
	return vault.Put(ctx, registrySecret, raw)
}

// registryConfig is the registry a workspace's platform builds push to: the
// instance registry, in a repository of that workspace's own.
func (s *Service) registryConfig(ctx context.Context, orgID string) (*registry.Config, error) {
	if orgID == "" {
		return nil, errors.New("platformcloud: organization is required")
	}
	cfg, err := s.settings.PlatformCloud(ctx)
	if err != nil {
		return nil, err
	}
	reg := cfg.Registry
	if reg.Endpoint == "" || strings.Trim(reg.Repository, "/") == "" {
		return nil, errors.New("platformcloud: the platform registry is not configured")
	}
	vault, err := s.secretStore()
	if err != nil {
		return nil, err
	}
	raw, err := vault.Reveal(ctx, registrySecret)
	if err != nil {
		return nil, fmt.Errorf("platformcloud: read registry credential: %w", err)
	}
	var cred registryCredential
	if err = json.Unmarshal(raw, &cred); err != nil {
		return nil, errors.New("platformcloud: the stored registry credential is unreadable")
	}
	if cred.Username == "" || cred.Password == "" {
		return nil, errRegistryLogin
	}
	// No direct token, and ScopedOnly: every token a VM gets is minted by
	// the registry's token service for one repository under this one.
	return &registry.Config{
		Endpoint:     reg.Endpoint,
		Repository:   strings.TrimRight(reg.Repository, "/") + "/ws-" + orgID,
		AuthEndpoint: reg.AuthEndpoint,
		Username:     cred.Username,
		Password:     cred.Password,
		ScopedOnly:   true,
	}, nil
}

// errRegistryUnavailable stands in for anything that went wrong reading the
// platform registry: its settings, its credential or the secret store.
var errRegistryUnavailable = &workspaceError{msg: "Loomplane Cloud's image registry isn't ready right now."}

// Registry is the registry the organization's platform builds push to and
// its platform deploys pull from: the platform registry, in the
// organization's own repository. Tokens minted from it are scoped under that
// repository, so a workspace never reaches another's images. Its errors are
// safe to show the workspace.
func (s *Service) Registry(ctx context.Context, orgID string) (*registry.Config, error) {
	if err := checkTenant(ctx, orgID); err != nil {
		return nil, err
	}
	cfg, err := s.registryConfig(ctx, orgID)
	if err != nil {
		slog.ErrorContext(ctx, "platform cloud registry failed", "org", orgID, "error", err)
		return nil, errRegistryUnavailable
	}
	return cfg, nil
}
