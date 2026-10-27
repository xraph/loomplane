package aisettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xraph/vault/secret"

	"github.com/xraph/loomplane/internal/connections"
)

// VaultAppID is the Vault app provider key values are kept under, apart
// from variable secrets and cloud credentials.
const VaultAppID = "loomplane-ai-credentials"

// VaultKeyLabel names the key, derived from secret_key, that Vault encrypts
// provider key values with. No other Vault app shares it.
const VaultKeyLabel = "loomplane/vault ai credentials v1"

// Keys keeps provider key values: one JSON blob of secret fields per
// provider key.
type Keys interface {
	// Put replaces the key's secret fields.
	Put(ctx context.Context, orgID, keyID string, secrets map[string]string) error
	// Reveal returns the key's secret fields, or ErrNotFound.
	Reveal(ctx context.Context, orgID, keyID string) (map[string]string, error)
	// Delete removes the key's secret fields. One that's already gone is not
	// an error.
	Delete(ctx context.Context, orgID, keyID string) error
}

// VaultKeys keeps provider key values in Vault, encrypted with AES-256-GCM
// and versioned on every write. Every Vault key is
// "<org id>/provider-keys/<key id>", both IDs from Loomplane's records and
// never from what a caller sent.
type VaultKeys struct {
	secrets *connections.VaultSecrets
}

// NewVaultKeys returns VaultKeys over a Vault secret service built for
// VaultAppID.
func NewVaultKeys(svc *secret.Service) *VaultKeys {
	return &VaultKeys{secrets: connections.NewVaultSecretsFor(svc, VaultAppID)}
}

var _ Keys = (*VaultKeys)(nil)

func vaultName(keyID string) string { return "provider-keys/" + keyID }

// Put implements Keys.
func (v *VaultKeys) Put(ctx context.Context, orgID, keyID string, secrets map[string]string) error {
	if secrets == nil {
		secrets = map[string]string{}
	}
	blob, err := json.Marshal(secrets)
	if err != nil {
		// Never wrap: a marshal error can quote what it was given.
		return errors.New("encode provider key secrets")
	}
	if err := v.secrets.Put(ctx, orgID, vaultName(keyID), blob); err != nil {
		return fmt.Errorf("store provider key secrets: %w", err)
	}
	return nil
}

// Reveal implements Keys.
func (v *VaultKeys) Reveal(ctx context.Context, orgID, keyID string) (map[string]string, error) {
	blob, err := v.secrets.Reveal(ctx, orgID, vaultName(keyID))
	if errors.Is(err, connections.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read provider key secrets: %w", err)
	}
	out := map[string]string{}
	if err := json.Unmarshal(blob, &out); err != nil {
		// Never wrap: a decode error can quote the bytes it choked on.
		return nil, errors.New("decode provider key secrets")
	}
	return out, nil
}

// Delete implements Keys.
func (v *VaultKeys) Delete(ctx context.Context, orgID, keyID string) error {
	if err := v.secrets.Delete(ctx, orgID, vaultName(keyID)); err != nil {
		return fmt.Errorf("delete provider key secrets: %w", err)
	}
	return nil
}
