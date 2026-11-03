package connections

import (
	"context"
	"errors"
	"fmt"

	"github.com/xraph/vault/core"
	"github.com/xraph/vault/secret"
)

// VaultAppID is the Vault app cloud credentials are kept under, apart
// from variable secrets.
const VaultAppID = "loomplane-cloud-credentials"

// VaultKeyLabel names the key, derived from secret_key, that Vault
// encrypts cloud credentials with. It's not the variables' key, so the
// two share no key material.
const VaultKeyLabel = "loomplane/vault cloud credentials v1"

// VaultSecrets keeps credentials in Vault, encrypted with AES-256-GCM
// and versioned on every write.
//
// Loomplane never puts a Vault tenant on the context, so every secret is
// an app-level one and every key is built from the
// organization's and the connection's IDs, both from Loomplane's records
// and never from what a caller sent.
type VaultSecrets struct {
	svc   *secret.Service
	appID string
}

// NewVaultSecrets returns VaultSecrets over a Vault secret service.
func NewVaultSecrets(svc *secret.Service) *VaultSecrets { return NewVaultSecretsFor(svc, VaultAppID) }

// NewVaultSecretsFor returns VaultSecrets that keep their secrets under
// the given Vault app ID.
func NewVaultSecretsFor(svc *secret.Service, appID string) *VaultSecrets {
	return &VaultSecrets{svc: svc, appID: appID}
}

var _ Secrets = (*VaultSecrets)(nil)

func vaultKey(orgID, connectionID string) string { return orgID + "/" + connectionID }

// Put implements Secrets.
func (v *VaultSecrets) Put(ctx context.Context, orgID, connectionID string, credential []byte) error {
	if _, err := v.svc.Set(ctx, vaultKey(orgID, connectionID), credential, v.appID); err != nil {
		return fmt.Errorf("store credential: %w", err)
	}
	return nil
}

// Reveal implements Secrets.
func (v *VaultSecrets) Reveal(ctx context.Context, orgID, connectionID string) ([]byte, error) {
	sec, err := v.svc.Get(ctx, vaultKey(orgID, connectionID), v.appID)
	if errors.Is(err, core.ErrSecretNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read credential: %w", err)
	}
	return sec.Value, nil
}

// Delete implements Secrets. One that's already gone is not an error.
func (v *VaultSecrets) Delete(ctx context.Context, orgID, connectionID string) error {
	err := v.svc.Delete(ctx, vaultKey(orgID, connectionID), v.appID)
	if err != nil && !errors.Is(err, core.ErrSecretNotFound) {
		return fmt.Errorf("delete credential: %w", err)
	}
	return nil
}

// PlatformOAuthAppsVaultAppID is the Vault app the platform's sign-in app
// secrets are kept under, apart from credentials, with one key per cloud.
const PlatformOAuthAppsVaultAppID = "loomplane-platform-oauth-apps"

// VaultAppSecrets keeps the platform's sign-in app secrets in Vault. The
// svc it is given should be scoped to PlatformOAuthAppsVaultAppID.
type VaultAppSecrets struct{ svc *secret.Service }

// NewVaultAppSecrets returns VaultAppSecrets over a Vault secret service.
func NewVaultAppSecrets(svc *secret.Service) *VaultAppSecrets { return &VaultAppSecrets{svc: svc} }

var _ AppSecrets = (*VaultAppSecrets)(nil)

// Put implements AppSecrets.
func (v *VaultAppSecrets) Put(ctx context.Context, cloud string, value []byte) error {
	if _, err := v.svc.Set(ctx, cloud, value, PlatformOAuthAppsVaultAppID); err != nil {
		return fmt.Errorf("store sign-in app secret: %w", err)
	}
	return nil
}

// Reveal implements AppSecrets.
func (v *VaultAppSecrets) Reveal(ctx context.Context, cloud string) ([]byte, error) {
	sec, err := v.svc.Get(ctx, cloud, PlatformOAuthAppsVaultAppID)
	if errors.Is(err, core.ErrSecretNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read sign-in app secret: %w", err)
	}
	return sec.Value, nil
}

// Delete implements AppSecrets. One that's already gone is not an error.
func (v *VaultAppSecrets) Delete(ctx context.Context, cloud string) error {
	err := v.svc.Delete(ctx, cloud, PlatformOAuthAppsVaultAppID)
	if err != nil && !errors.Is(err, core.ErrSecretNotFound) {
		return fmt.Errorf("delete sign-in app secret: %w", err)
	}
	return nil
}
