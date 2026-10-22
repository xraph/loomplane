package environments

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xraph/vault/core"
	"github.com/xraph/vault/secret"
)

// VaultAppID is the Vault app Loomplane keeps variable secrets under, so
// listing it returns those and nothing else.
const VaultAppID = "loomplane-variables"

// VaultKeyLabel names the key, derived from secret_key, that Vault
// encrypts variable secrets with.
const VaultKeyLabel = "loomplane/vault variable secrets v1"

const vaultPage = 500

// VaultSecrets keeps secret values in Vault, encrypted with AES-256-GCM
// and versioned on every write.
//
// Vault scopes a secret by tenant only when the context carries one
// (its own scope.WithTenantID), and Loomplane never sets it, so every
// secret here is an app-level one. Every key is built from the
// organization and the variable's own ID, both from Loomplane's
// records and never from anything a caller sent, and one organization's
// key can't name another's secret.
type VaultSecrets struct {
	svc *secret.Service
}

// NewVaultSecrets returns VaultSecrets over a Vault secret service.
func NewVaultSecrets(svc *secret.Service) *VaultSecrets { return &VaultSecrets{svc: svc} }

var _ Secrets = (*VaultSecrets)(nil)

func vaultKey(orgID, variableID string) string { return orgID + "/" + variableID }

// Put stores value as the variable's secret, archiving any earlier one.
func (v *VaultSecrets) Put(ctx context.Context, orgID, variableID string, value []byte) error {
	if _, err := v.svc.Set(ctx, vaultKey(orgID, variableID), value, VaultAppID); err != nil {
		return fmt.Errorf("store secret: %w", err)
	}
	return nil
}

// Reveal returns the variable's secret.
func (v *VaultSecrets) Reveal(ctx context.Context, orgID, variableID string) ([]byte, error) {
	sec, err := v.svc.Get(ctx, vaultKey(orgID, variableID), VaultAppID)
	if errors.Is(err, core.ErrSecretNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read secret: %w", err)
	}
	return sec.Value, nil
}

// Delete removes the variable's secret and its earlier versions. One
// that's already gone is not an error.
func (v *VaultSecrets) Delete(ctx context.Context, orgID, variableID string) error {
	err := v.svc.Delete(ctx, vaultKey(orgID, variableID), VaultAppID)
	if err != nil && !errors.Is(err, core.ErrSecretNotFound) {
		return fmt.Errorf("delete secret: %w", err)
	}
	return nil
}

// IDs lists the variable IDs the organization has secrets stored for.
func (v *VaultSecrets) IDs(ctx context.Context, orgID string) ([]string, error) {
	prefix := orgID + "/"
	var ids []string
	for offset := 0; ; offset += vaultPage {
		page, err := v.svc.List(ctx, VaultAppID, secret.ListOpts{Limit: vaultPage, Offset: offset})
		if err != nil {
			return nil, fmt.Errorf("list secrets: %w", err)
		}
		for _, m := range page {
			if id, ok := strings.CutPrefix(m.Key, prefix); ok {
				ids = append(ids, id)
			}
		}
		if len(page) < vaultPage {
			return ids, nil
		}
	}
}
