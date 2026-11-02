// Package setuptoken derives the one-time token that gates first-run setup on
// a production instance.
//
// The token is computed from the instance's secret key, so the operator can
// recompute it (for example from the CLI) without any stored state. It only
// matters until the first administrator exists. Once setup is done the token
// is never consulted again.
package setuptoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"strings"
)

const (
	secretKeyBytes = 32
	message        = "loomplane setup token v1"
	tokenBytes     = 20
)

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Derive returns the setup token for a base64-encoded 32-byte secret key.
func Derive(secretKey string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(secretKey)
	if err != nil {
		return "", errors.New("setuptoken: secret key is not valid base64")
	}
	if len(key) != secretKeyBytes {
		return "", errors.New("setuptoken: secret key must be 32 bytes")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	sum := mac.Sum(nil)[:tokenBytes]
	return strings.ToLower(encoding.EncodeToString(sum)), nil
}

// Matches reports whether presented is the token for secretKey. It ignores
// surrounding whitespace and letter case, and is false on any derive error or
// empty input.
func Matches(secretKey, presented string) bool {
	presented = strings.ToLower(strings.TrimSpace(presented))
	if presented == "" {
		return false
	}
	want, err := Derive(secretKey)
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(want), []byte(presented))
}

// Link returns the first-run setup URL for publicURL and token.
func Link(publicURL, token string) string {
	return strings.TrimRight(publicURL, "/") + "/setup?token=" + token
}
