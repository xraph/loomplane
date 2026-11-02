package deployments

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xraph/vault/core"
	"github.com/xraph/vault/secret"
)

// ResourceVaultAppID is the Vault app resource credentials are kept
// under, such as a database's password and address.
const ResourceVaultAppID = "loomplane-resource-credentials"

// ResourceVaultKeyLabel names the key, derived from secret_key, that Vault
// encrypts resource credentials with.
const ResourceVaultKeyLabel = "loomplane/vault resource credentials v1"

// ErrNoCredential is a resource credential that isn't stored.
var ErrNoCredential = errors.New("no credential stored for that resource")

// ResourceSecrets keeps what a provisioned resource's users need to reach
// it. *VaultResourceSecrets is the real one.
type ResourceSecrets interface {
	Put(ctx context.Context, key string, value []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

// VersionedResourceSecrets keeps an immutable credential handle in a durable
// operation checkpoint. Later writers cannot change the referenced version.
type VersionedResourceSecrets interface {
	ResourceSecrets
	PutVersion(context.Context, string, []byte) (int64, error)
	GetVersion(context.Context, string, int64) ([]byte, error)
}

// ResourceKey scopes a resource's credential to its organization,
// environment and name, all from Loomplane's records: where one field
// kept for an action, such as a database's "url" or a composed
// resource's "secret:<name>", lives in ResourceSecrets.
func ResourceKey(orgID, environmentID, resource, field string) string {
	return orgID + "/" + environmentID + "/" + resource + "/" + field
}

// environmentKeys is what every credential an environment keeps starts
// with, or an error when either ID would let it reach past that
// environment's own keys.
func environmentKeys(orgID, environmentID string) (string, error) {
	for _, id := range []string{orgID, environmentID} {
		if id == "" || strings.Contains(id, "/") {
			return "", fmt.Errorf("%q can't scope an environment's credentials", id)
		}
	}
	return orgID + "/" + environmentID + "/", nil
}

func resourceKey(orgID, environmentID, resource, field string) string {
	return ResourceKey(orgID, environmentID, resource, field)
}

// ResourceURLKey is the key a resource's address is kept under, so code
// outside this package reads it without copying the key format.
func ResourceURLKey(orgID, environmentID, resource string) string {
	return resourceKey(orgID, environmentID, resource, ResourceURL)
}

// VaultResourceSecrets keeps resource credentials in Vault, encrypted and
// versioned.
type VaultResourceSecrets struct{ svc *secret.Service }

// NewVaultResourceSecrets returns VaultResourceSecrets over a Vault secret
// service.
func NewVaultResourceSecrets(svc *secret.Service) *VaultResourceSecrets {
	return &VaultResourceSecrets{svc: svc}
}

var _ ResourceSecrets = (*VaultResourceSecrets)(nil)

func (v *VaultResourceSecrets) SnapshotMetadata(ctx context.Context, key string) (identity string, version int64, err error) {
	meta, err := v.svc.GetMeta(ctx, key, ResourceVaultAppID)
	if errors.Is(err, core.ErrSecretNotFound) {
		return "", 0, ErrNoCredential
	}
	if err != nil {
		return "", 0, err
	}
	return meta.ID.String(), meta.Version, nil
}

// Put implements ResourceSecrets.
func (v *VaultResourceSecrets) Put(ctx context.Context, key string, value []byte) error {
	_, err := v.PutVersion(ctx, key, value)
	return err
}

func (v *VaultResourceSecrets) PutVersion(ctx context.Context, key string, value []byte) (int64, error) {
	return v.putVersion(ctx, key, value)
}

func (v *VaultResourceSecrets) putVersion(ctx context.Context, key string, value []byte, options ...secret.SetOption) (int64, error) {
	meta, err := v.svc.Set(ctx, key, value, ResourceVaultAppID, options...)
	if err != nil {
		return 0, fmt.Errorf("store resource credential: %w", err)
	}
	return meta.Version, nil
}

func (v *VaultResourceSecrets) GetVersion(ctx context.Context, key string, version int64) ([]byte, error) {
	if version <= 0 {
		return nil, ErrNoCredential
	}
	s, err := v.svc.GetVersion(ctx, key, ResourceVaultAppID, version)
	if errors.Is(err, core.ErrSecretNotFound) {
		return nil, ErrNoCredential
	}
	if err != nil {
		return nil, fmt.Errorf("read resource credential version: %w", err)
	}
	return s.Value, nil
}

// Get implements ResourceSecrets.
func (v *VaultResourceSecrets) Get(ctx context.Context, key string) ([]byte, error) {
	s, err := v.svc.Get(ctx, key, ResourceVaultAppID)
	if errors.Is(err, core.ErrSecretNotFound) {
		return nil, ErrNoCredential
	}
	if err != nil {
		return nil, fmt.Errorf("read resource credential: %w", err)
	}
	return s.Value, nil
}

// Delete implements ResourceSecrets.
func (v *VaultResourceSecrets) Delete(ctx context.Context, key string) error {
	err := v.svc.Delete(ctx, key, ResourceVaultAppID)
	if err != nil && !errors.Is(err, core.ErrSecretNotFound) {
		return fmt.Errorf("delete resource credential: %w", err)
	}
	return nil
}

// Version says which version of a resource credential is stored without
// decrypting it or counting as an access: its id, version number and update
// time, which change whenever it is written again, deleted and written back
// included. A missing one is ErrNoCredential.
func (v *VaultResourceSecrets) Version(ctx context.Context, key string) (string, error) {
	m, err := v.svc.GetMeta(ctx, key, ResourceVaultAppID)
	if errors.Is(err, core.ErrSecretNotFound) {
		return "", ErrNoCredential
	}
	if err != nil {
		return "", fmt.Errorf("read resource credential metadata: %w", err)
	}
	return fmt.Sprintf("%s/%d/%d", m.ID, m.Version, m.UpdatedAt.UnixNano()), nil
}

// ResourceSecretKeys is a ResourceSecrets that can list the keys it keeps.
// *VaultResourceSecrets is one.
type ResourceSecretKeys interface {
	Keys(ctx context.Context, prefix string) ([]string, error)
}

var _ ResourceSecretKeys = (*VaultResourceSecrets)(nil)

// keysPage is how many keys Keys reads from Vault at a time.
const keysPage = 500

// Keys returns every key kept that starts with prefix. Vault can't filter
// by prefix, so this reads the metadata of every resource credential, a
// page at a time and never a value: fine when an environment is deleted,
// too slow for a request that runs often.
func (v *VaultResourceSecrets) Keys(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	for offset := 0; ; offset += keysPage {
		page, err := v.svc.List(ctx, ResourceVaultAppID, secret.ListOpts{Limit: keysPage, Offset: offset})
		if err != nil {
			return nil, fmt.Errorf("list resource credentials: %w", err)
		}
		for _, m := range page {
			if strings.HasPrefix(m.Key, prefix) {
				keys = append(keys, m.Key)
			}
		}
		if len(page) < keysPage {
			return keys, nil
		}
	}
}
