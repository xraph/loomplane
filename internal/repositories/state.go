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

// BitbucketStateKeyLabel names the key, derived from secret_key, that signs
// Bitbucket connect states. It isn't GitHub's, so neither flow takes the
// other's state.
const BitbucketStateKeyLabel = "loomplane/bitbucket connect state v1"

// stateLifetime is how long someone has to finish installing the App.
const stateLifetime = 15 * time.Minute

// ErrBadState means an install or connect link wasn't made here, for this
// person and organization, or has expired. It names no host, since GitHub
// and Bitbucket both use it.
var ErrBadState = errors.New("that connection wasn't started here, or took too long; connect again")

// InstallState says who started a GitHub App installation or a Bitbucket
// connection, and for which organization. It travels through the host as
// the link's state.
type InstallState struct {
	OrgID   string `json:"o"`
	UserID  string `json:"u"`
	Expires int64  `json:"e"`
	Nonce   string `json:"n"`
	// Workspace is the Bitbucket workspace slug being connected. GitHub's
	// states have none.
	Workspace string `json:"w,omitempty"`
}

// SignState mints the state for an install link.
func SignState(key []byte, orgID, userID string, now time.Time) (string, error) {
	return signState(key, InstallState{OrgID: orgID, UserID: userID}, now)
}

// SignWorkspaceState mints the state for a link that connects a Bitbucket
// workspace, so the callback knows which workspace was asked for.
func SignWorkspaceState(key []byte, orgID, userID, workspace string, now time.Time) (string, error) {
	return signState(key, InstallState{OrgID: orgID, UserID: userID, Workspace: workspace}, now)
}

// signState fills in st's expiry and nonce, and signs it.
func signState(key []byte, st InstallState, now time.Time) (string, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("install state nonce: %w", err)
	}
	st.Expires = now.Add(stateLifetime).Unix()
	st.Nonce = base64.RawURLEncoding.EncodeToString(nonce)
	payload, err := json.Marshal(st)
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
