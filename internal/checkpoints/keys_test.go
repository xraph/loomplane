package checkpoints

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/chronicle/keys"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultmem "github.com/xraph/vault/store/memory"

	"github.com/xraph/loomplane/internal/security"
)

const testSecretKey = "bG9vbXBsYW5lLWRldmVsb3BtZW50LXNlY3JldC1rZXk="

// vaultService is a Vault secret service over store, encrypted the way the
// identity extension encrypts it.
func vaultService(t *testing.T, store secret.Store) *secret.Service {
	t.Helper()
	key, err := security.DeriveKey(testSecretKey, KeyLabel)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	return secret.NewService(store, enc, secret.WithAppID(VaultAppID))
}

func TestKeysRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := vaultmem.New()
	k := NewKeys(vaultService(t, store))

	if _, _, err := k.Current(ctx, keys.UseCheckpointSig); !errors.Is(err, keys.ErrNoActiveKey) {
		t.Fatalf("Current before Ensure = %v, want ErrNoActiveKey", err)
	}
	created, err := k.Ensure(ctx)
	if err != nil || !created {
		t.Fatalf("Ensure = %v, %v; want a new key", created, err)
	}
	priv, keyID, err := k.Current(ctx, keys.UseCheckpointSig)
	if err != nil {
		t.Fatal(err)
	}
	if len(priv) != keys.Ed25519KeySize || !strings.HasPrefix(keyID, "loomplane-ckpt-") {
		t.Fatalf("Current = %d bytes, %q", len(priv), keyID)
	}
	pub, _ := ed25519.PrivateKey(priv).Public().(ed25519.PublicKey)
	if KeyID(pub) != keyID {
		t.Fatalf("key ID %q isn't KeyID of the public key, %q", keyID, KeyID(pub))
	}
	byID, err := k.ByID(ctx, keyID)
	if err != nil || !ed25519.PrivateKey(byID).Equal(ed25519.PrivateKey(priv)) {
		t.Fatalf("ByID = %v; want the current key", err)
	}

	// A second process reads the same key with the same ID, and Ensure
	// doesn't replace it.
	again := NewKeys(vaultService(t, store))
	if created, err = again.Ensure(ctx); err != nil || created {
		t.Fatalf("second Ensure = %v, %v; want the stored key", created, err)
	}
	pub2, keyID2, err := again.PublicKey(ctx)
	if err != nil || keyID2 != keyID || !pub2.Equal(pub) {
		t.Fatalf("second process: %q, %v; want %q", keyID2, err, keyID)
	}

	if _, _, err = k.Current(ctx, keys.UseHMAC); !errors.Is(err, keys.ErrNoActiveKey) {
		t.Fatalf("Current(hmac) = %v, want ErrNoActiveKey", err)
	}
	if _, err = k.ByID(ctx, "loomplane-ckpt-nobody"); !errors.Is(err, keys.ErrKeyNotFound) {
		t.Fatalf("ByID(unknown) = %v, want ErrKeyNotFound", err)
	}
}

// A key replaced in Vault still verifies what it signed, through ByID.
func TestKeysByIDFindsAnOlderVersion(t *testing.T) {
	ctx := context.Background()
	store := vaultmem.New()
	svc := vaultService(t, store)
	k := NewKeys(svc)
	if _, err := k.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	_, oldID, err := k.PublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, next, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Set(ctx, vaultKey, next, VaultAppID); err != nil {
		t.Fatal(err)
	}

	restarted := NewKeys(svc)
	_, newID, err := restarted.PublicKey(ctx)
	if err != nil || newID == oldID {
		t.Fatalf("after a rotation the key ID is %q (%v), want a new one", newID, err)
	}
	if _, err = restarted.ByID(ctx, oldID); err != nil {
		t.Fatalf("ByID(old key) = %v", err)
	}
}

func TestKeysRefusesAMalformedKey(t *testing.T) {
	ctx := context.Background()
	svc := vaultService(t, vaultmem.New())
	if _, err := svc.Set(ctx, vaultKey, []byte("short"), VaultAppID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewKeys(svc).Current(ctx, keys.UseCheckpointSig); err == nil {
		t.Fatal("Current accepted a 5-byte key")
	}
	bad := make([]byte, keys.Ed25519KeySize)
	if _, err := svc.Set(ctx, vaultKey, bad, VaultAppID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewKeys(svc).Current(ctx, keys.UseCheckpointSig); err == nil {
		t.Fatal("Current accepted a key whose public half doesn't match its seed")
	}
}

// Replicas that start together run Ensure under the startup lock. With the
// lock (here a mutex standing in for it), they all end on one key.
func TestKeysEnsureUnderALockMakesOneKey(t *testing.T) {
	ctx := context.Background()
	store := vaultmem.New()
	var lock sync.Mutex
	const replicas = 8
	ids := make([]string, replicas)
	created := make([]bool, replicas)
	errs := make([]error, replicas)
	var wg sync.WaitGroup
	for i := range replicas {
		wg.Go(func() {
			k := NewKeys(vaultService(t, store))
			lock.Lock()
			created[i], errs[i] = k.Ensure(ctx)
			lock.Unlock()
			if errs[i] == nil {
				_, ids[i], errs[i] = k.PublicKey(ctx)
			}
		})
	}
	wg.Wait()
	made := 0
	for i := range replicas {
		if errs[i] != nil {
			t.Fatalf("replica %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("replica %d has key %q, replica 0 has %q", i, ids[i], ids[0])
		}
		if created[i] {
			made++
		}
	}
	if made != 1 {
		t.Fatalf("%d replicas created a key, want 1", made)
	}
	versions, err := vaultService(t, store).ListVersions(ctx, vaultKey, VaultAppID)
	if err != nil || len(versions) > 1 {
		t.Fatalf("Vault holds %d versions (%v), want 1", len(versions), err)
	}
}
