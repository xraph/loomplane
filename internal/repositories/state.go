package repositories

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// StateKeyLabel names the key, derived from secret_key, that signs
// install states.
const StateKeyLabel = "loomplane/github install state v1"

// stateLifetime is how long someone has to finish installing the App.
const stateLifetime = 15 * time.Minute

// ErrBadState means an install link wasn't made here, for this person and
// organization, or has expired.
var ErrBadState = errors.New("that GitHub connection wasn't started here, or took too long; connect again")

// InstallState says who started a GitHub App installation and for which
// organization. It travels through GitHub as the install link's state.
type InstallState struct {
	OrgID   string `json:"o"`
	UserID  string `json:"u"`
	Expires int64  `json:"e"`
	Nonce   string `json:"n"`
}

// SignState mints the state for an install link.
func SignState(key []byte, orgID, userID string, now time.Time) (string, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("install state nonce: %w", err)
	}
	payload, err := json.Marshal(InstallState{
		OrgID: orgID, UserID: userID, Expires: now.Add(stateLifetime).Unix(),
		Nonce: base64.RawURLEncoding.EncodeToString(nonce),
	})
	if err != nil {
		return "", fmt.Errorf("encode install state: %w", err)
	}
	p := base64.RawURLEncoding.EncodeToString(payload)
	return p + "." + sign(key, p), nil
}

// VerifyState checks a state's signature and age, and returns it.
func VerifyState(key []byte, token string, now time.Time) (InstallState, error) {
	p, sig, ok := strings.Cut(token, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(sign(key, p))) {
		return InstallState{}, ErrBadState
	}
	raw, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return InstallState{}, ErrBadState
	}
	var s InstallState
	if err := json.Unmarshal(raw, &s); err != nil || now.Unix() > s.Expires {
		return InstallState{}, ErrBadState
	}
	return s, nil
}

func sign(key []byte, payload string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload)) // a hash's Write never fails
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
