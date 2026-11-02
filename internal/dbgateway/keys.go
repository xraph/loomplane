package dbgateway

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/dbgateway/plane"
	"github.com/xraph/loomplane/internal/secureconfig"
)

// ErrSigningKey is every failure to load the snapshot signing keys. It never
// says what was wrong with the file, so it can't leak the file either.
var ErrSigningKey = errors.New("invalid database gateway signing key configuration")

const (
	// maxSigningKeyFile bounds the key file, which holds a few small JSON
	// objects.
	maxSigningKeyFile = 16 << 10
	// maxSigningKeys bounds how many keys one file holds. A rotation needs
	// two at a time.
	maxSigningKeys = 8
)

// SigningKey is one key the control plane signs snapshots with, and the
// TrustKey gateways are given for it.
type SigningKey struct {
	Private ed25519.PrivateKey
	Trust   plane.TrustKey
}

type signingKeyEntry struct {
	PrivateKey string    `json:"private_key"`
	NotBefore  time.Time `json:"not_before"`
	NotAfter   time.Time `json:"not_after"`
}

// signingKeyFile is either version: 1 holds one key in its own fields, 2
// holds a list of them in keys.
type signingKeyFile struct {
	Version int `json:"version"`
	signingKeyEntry
	Keys []signingKeyEntry `json:"keys"`
}

// LoadSigningKeys reads the control plane's snapshot signing keys from a
// private, bounded regular file. Version 2 holds up to eight keys, so a new
// one can sit beside the old while gateways learn to trust it:
//
//	{"version":2,"keys":[{"private_key":"<base64 Ed25519 seed>","not_before":"<RFC 3339>","not_after":"<RFC 3339>"}, ...]}
//
// Version 1 holds one, in the fields of the object itself:
//
//	{"version":1,"private_key":"<base64 Ed25519 seed>","not_before":"<RFC 3339>","not_after":"<RFC 3339>"}
//
// It refuses any other version, a seed that isn't 32 bytes and a window that
// closes before it opens. The same seed may appear more than once, each time
// with another window, which is how a key is renewed. Windows that aren't
// open now are the caller's to handle: the Service signs with the newest
// key whose window is open and with none outside every window, so an
// expired key stops the gateway endpoints and not Loomplane. Reloading
// takes a restart.
func LoadSigningKeys(path string) ([]SigningKey, error) {
	var f signingKeyFile
	if secureconfig.ReadJSON(path, maxSigningKeyFile, true, &f) != nil {
		return nil, ErrSigningKey
	}
	var entries []signingKeyEntry
	switch {
	case f.Version == 1 && f.Keys == nil:
		entries = []signingKeyEntry{f.signingKeyEntry}
	case f.Version == 2 && f.signingKeyEntry == (signingKeyEntry{}) && len(f.Keys) > 0 && len(f.Keys) <= maxSigningKeys:
		entries = f.Keys
	default:
		return nil, ErrSigningKey
	}
	keys := make([]SigningKey, 0, len(entries))
	for _, e := range entries {
		k, err := e.load()
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

func (e signingKeyEntry) load() (SigningKey, error) {
	seed, err := base64.StdEncoding.Strict().DecodeString(e.PrivateKey)
	defer clear(seed)
	if err != nil || len(seed) != ed25519.SeedSize || !e.NotBefore.Before(e.NotAfter) {
		return SigningKey{}, ErrSigningKey
	}
	private := ed25519.NewKeyFromSeed(seed)
	return SigningKey{
		Private: private,
		Trust: plane.TrustKey{
			PublicKey: bytes.Clone(private[ed25519.SeedSize:]),
			NotBefore: e.NotBefore,
			NotAfter:  e.NotAfter,
		},
	}, nil
}
