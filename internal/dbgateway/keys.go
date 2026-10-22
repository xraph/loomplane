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

// ErrSigningKey is every failure to load the snapshot signing key. It never
// says what was wrong with the file, so it can't leak the file either.
var ErrSigningKey = errors.New("invalid database gateway signing key configuration")

// maxSigningKeyFile bounds the key file, which holds one small JSON object.
const maxSigningKeyFile = 16 << 10

type signingKeyFile struct {
	Version    int       `json:"version"`
	PrivateKey string    `json:"private_key"`
	NotBefore  time.Time `json:"not_before"`
	NotAfter   time.Time `json:"not_after"`
}

// LoadSigningKey reads the control plane's snapshot signing key from a
// private, bounded regular file:
//
//	{"version":1,"private_key":"<base64 Ed25519 seed>","not_before":"<RFC 3339>","not_after":"<RFC 3339>"}
//
// It returns the private key and the TrustKey gateways are given for it. It
// refuses any other version, a seed that isn't 32 bytes and a window that
// closes before it opens. A window that isn't open now is the caller's to
// handle: the Service holds such a key without signing with it, so an
// expired key stops the gateway endpoints and not Loomplane. Reloading
// takes a restart.
func LoadSigningKey(path string) (ed25519.PrivateKey, plane.TrustKey, error) {
	var f signingKeyFile
	if secureconfig.ReadJSON(path, maxSigningKeyFile, true, &f) != nil || f.Version != 1 {
		return nil, plane.TrustKey{}, ErrSigningKey
	}
	seed, err := base64.StdEncoding.Strict().DecodeString(f.PrivateKey)
	defer clear(seed)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, plane.TrustKey{}, ErrSigningKey
	}
	if !f.NotBefore.Before(f.NotAfter) {
		return nil, plane.TrustKey{}, ErrSigningKey
	}
	private := ed25519.NewKeyFromSeed(seed)
	trust := plane.TrustKey{
		PublicKey: bytes.Clone(private[ed25519.SeedSize:]),
		NotBefore: f.NotBefore,
		NotAfter:  f.NotAfter,
	}
	return private, trust, nil
}
