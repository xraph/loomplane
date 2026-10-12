package security

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"github.com/xraph/authsome/bridge"
)

const (
	tokenKeyInfo  = "loomplane/authsome token encryption v1" //nolint:gosec // G101: an HKDF label, not a credential
	tokenKeyBytes = 32
)

// TokenEncryptor derives the key authsome encrypts tokens at rest with
// (third-party OAuth tokens and the like) from Loomplane's secret_key.
//
// HKDF keeps authsome's key material separate from anything else Loomplane
// derives from the same secret. The key ID names the key without revealing
// it, so a stored token finds its key again after a rotation adds new ones.
func TokenEncryptor(secretKey string) (bridge.Encryptor, error) {
	secret, err := base64.StdEncoding.DecodeString(secretKey)
	if err != nil {
		return nil, fmt.Errorf("secret_key isn't base64: %w", err)
	}
	key, err := hkdf.Key(sha256.New, secret, nil, tokenKeyInfo, tokenKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("derive authsome token key: %w", err)
	}
	sum := sha256.Sum256(key)
	kid := "lp" + hex.EncodeToString(sum[:5])
	ring, err := bridge.NewKeyring(kid, map[string][]byte{kid: key}, []string{kid})
	if err != nil {
		return nil, fmt.Errorf("authsome token keyring: %w", err)
	}
	return ring, nil
}
