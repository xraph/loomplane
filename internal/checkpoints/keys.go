// Package checkpoints signs Chronicle checkpoints over Loomplane's audit
// trail.
//
// A checkpoint is Chronicle's signed statement of a stream's head: from
// which sequence to which, ending on which hash. Someone holding the
// public key can then tell whether events in that range were changed after
// it was signed. Hallproof checks exactly that, so you pin the key Loomplane
// logs at start in Hallproof.
//
// Keys keeps the Ed25519 signing key in Vault, encrypted under a key derived
// from secret_key. Scheduler takes the checkpoints. The identity extension
// wires both.
package checkpoints

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"

	"github.com/xraph/chronicle/keys"
	"github.com/xraph/vault/core"
	"github.com/xraph/vault/secret"
)

// VaultAppID is the Vault app the signing key is kept under, apart from
// every other secret.
const VaultAppID = "loomplane-chronicle-checkpoints"

// KeyLabel names the key, derived from secret_key, that Vault encrypts the
// signing key with.
const KeyLabel = "chronicle-checkpoint"

// vaultKey is the signing key's name in VaultAppID. A new key is a new
// Vault version of it, so the old ones stay readable for ByID.
const vaultKey = "ed25519"

// ErrKeyUndecryptable is a checkpoint key in Vault that the key derived
// from the current secret_key can't decrypt.
var ErrKeyUndecryptable = errors.New("checkpoint key can't be decrypted")

// keyIDPrefix starts every key ID, so a pinned ID says whose it is.
const keyIDPrefix = "loomplane-ckpt-"

// Keys is a chronicle keys.Provider over Vault. It only serves
// keys.UseCheckpointSig.
//
// The current key is read from Vault once and kept, so a key written to
// Vault by hand takes over on the next restart. Older versions stay
// verifiable through ByID.
type Keys struct {
	svc *secret.Service

	mu        sync.Mutex
	current   ed25519.PrivateKey
	currentID string
	byID      map[string]ed25519.PrivateKey
}

var _ keys.Provider = (*Keys)(nil)

// NewKeys returns Keys over a Vault secret service. Nothing is read until a
// key is asked for.
func NewKeys(svc *secret.Service) *Keys {
	return &Keys{svc: svc, byID: map[string]ed25519.PrivateKey{}}
}

// KeyID is the stable ID of the key whose public half is pub.
func KeyID(pub ed25519.PublicKey) string {
	return keyIDPrefix + base64.RawURLEncoding.EncodeToString(pub[:8])
}

// Ensure makes sure Vault holds a signing key, creating one when it has
// none, and reports whether it created it. Run it under the startup lock:
// two replicas that both found nothing would each write a key, and the
// later one would replace the earlier.
func (k *Keys) Ensure(ctx context.Context) (created bool, err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.current != nil {
		return false, nil
	}
	err = k.loadLocked(ctx)
	if !errors.Is(err, keys.ErrNoActiveKey) {
		return false, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return false, fmt.Errorf("generate the checkpoint key: %w", err)
	}
	if _, err = k.svc.Set(ctx, vaultKey, priv, VaultAppID); err != nil {
		return false, fmt.Errorf("store the checkpoint key: %w", err)
	}
	k.remember(priv, true)
	return true, nil
}

// Current implements keys.Provider: the signing key and its ID.
func (k *Keys) Current(ctx context.Context, use keys.Use) (key []byte, keyID string, err error) {
	if use != keys.UseCheckpointSig {
		return nil, "", fmt.Errorf("%w: %s", keys.ErrNoActiveKey, use)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.current == nil {
		if loadErr := k.loadLocked(ctx); loadErr != nil {
			return nil, "", loadErr
		}
	}
	return bytes.Clone(k.current), k.currentID, nil
}

// ByID implements keys.Provider: the key with keyID, current or an older
// Vault version.
func (k *Keys) ByID(ctx context.Context, keyID string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if key, ok := k.byID[keyID]; ok {
		return bytes.Clone(key), nil
	}
	if k.current == nil {
		if err := k.loadLocked(ctx); err != nil && !errors.Is(err, keys.ErrNoActiveKey) {
			return nil, err
		}
		if key, ok := k.byID[keyID]; ok {
			return bytes.Clone(key), nil
		}
	}
	versions, err := k.svc.ListVersions(ctx, vaultKey, VaultAppID)
	if err != nil {
		return nil, fmt.Errorf("list checkpoint key versions: %w", err)
	}
	for _, v := range versions {
		sec, err := k.svc.GetVersion(ctx, vaultKey, VaultAppID, v.Version)
		if err != nil {
			return nil, fmt.Errorf("read checkpoint key version %d: %w", v.Version, err)
		}
		priv, err := parse(sec.Value)
		if err != nil {
			return nil, fmt.Errorf("checkpoint key version %d: %w", v.Version, err)
		}
		k.remember(priv, false)
	}
	if key, ok := k.byID[keyID]; ok {
		return bytes.Clone(key), nil
	}
	return nil, fmt.Errorf("%w: %s", keys.ErrKeyNotFound, keyID)
}

// PublicKey is the current key's public half and ID, for an admin to pin.
func (k *Keys) PublicKey(ctx context.Context) (ed25519.PublicKey, string, error) {
	priv, keyID, err := k.Current(ctx, keys.UseCheckpointSig)
	if err != nil {
		return nil, "", err
	}
	return publicHalf(priv), keyID, nil
}

// loadLocked reads the current key from Vault. Vault without one is
// keys.ErrNoActiveKey. k.mu is held.
func (k *Keys) loadLocked(ctx context.Context) error {
	sec, err := k.svc.Get(ctx, vaultKey, VaultAppID)
	if errors.Is(err, core.ErrSecretNotFound) {
		return fmt.Errorf("%w: no checkpoint key in Vault yet", keys.ErrNoActiveKey)
	}
	if err != nil {
		if undecryptable(ctx, k.svc) {
			return fmt.Errorf("%w: the checkpoint key in Vault app %s can't be decrypted with the current secret_key, "+
				"which isn't the one it was stored under. Restore the old secret_key, or rotate the checkpoint key: "+
				"delete it from Vault (DELETE FROM vault_secret_versions WHERE app_id = '%[2]s'; "+
				"DELETE FROM vault_secrets WHERE app_id = '%[2]s'), start Loomplane so it creates a new one, "+
				"and re-pin the new public key it logs in Hallproof (%w)", ErrKeyUndecryptable, VaultAppID, err)
		}
		return fmt.Errorf("read the checkpoint key: %w", err)
	}
	priv, err := parse(sec.Value)
	if err != nil {
		return err
	}
	k.remember(priv, true)
	return nil
}

// undecryptable reports whether the checkpoint key is in Vault, encrypted,
// after a read of it failed: the read failed on the decrypt, not on the
// database, since the row's metadata reads without decrypting anything.
func undecryptable(ctx context.Context, svc *secret.Service) bool {
	meta, err := svc.GetMeta(ctx, vaultKey, VaultAppID)
	return err == nil && meta.EncryptionAlg != ""
}

// remember caches priv by ID, and as the current key when current. k.mu is
// held.
func (k *Keys) remember(priv ed25519.PrivateKey, current bool) {
	keyID := KeyID(publicHalf(priv))
	k.byID[keyID] = priv
	if current {
		k.current, k.currentID = priv, keyID
	}
}

// parse checks that raw is a whole Ed25519 private key whose public half
// matches its seed.
func parse(raw []byte) (ed25519.PrivateKey, error) {
	if len(raw) != keys.Ed25519KeySize {
		return nil, fmt.Errorf("checkpoint key is %d bytes, want %d", len(raw), keys.Ed25519KeySize)
	}
	priv := ed25519.NewKeyFromSeed(raw[:ed25519.SeedSize])
	if !bytes.Equal(priv, raw) {
		return nil, errors.New("checkpoint key's public half doesn't match its seed")
	}
	return priv, nil
}

// publicHalf is the public key an Ed25519 private key carries in its
// trailing 32 bytes.
func publicHalf(priv []byte) ed25519.PublicKey {
	return bytes.Clone(ed25519.PublicKey(priv[ed25519.SeedSize:]))
}
