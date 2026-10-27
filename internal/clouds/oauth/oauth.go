// Package oauth describes signing in to a cloud: the app Loomplane is
// registered as with it, and the grant it hands back. Each cloud's package
// does its own authorize, exchange and refresh calls and returns these.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// App is Loomplane's registration with a cloud.
type App struct {
	ClientID     string
	ClientSecret string
	// Slug is the app's name in its cloud's own address. Only Vercel's
	// has one.
	Slug string
}

// AuthorizeRequest is what the page that signs someone in needs: where to
// send them back, the state that comes back with them, the PKCE challenge
// for a cloud that takes one, and the choices made before signing in.
type AuthorizeRequest struct {
	Redirect, State, Challenge string
	Settings                   map[string]string
}

// CodeRequest trades a code for a grant: the redirect the sign-in used,
// the code, the PKCE verifier whose challenge it sent, and its settings.
type CodeRequest struct {
	Redirect, Code, Verifier string
	Settings                 map[string]string
}

// Grant is what a sign-in gives Loomplane: a token to call the cloud with,
// the token that renews it, and when it stops working. Account says what the
// cloud says the grant is for, such as a team's UUID and name.
type Grant struct {
	AccessToken  string            `json:"access_token"`
	RefreshToken string            `json:"refresh_token"`
	Expiry       time.Time         `json:"expiry"`
	Account      map[string]string `json:"account,omitempty"`
}

// ErrRefused is a cloud saying no to a sign-in or a renewal: the code was
// spent, or the grant was revoked or expired.
var ErrRefused = errors.New("the cloud refused the sign-in")

// ErrAppRejected is a cloud saying no to Loomplane's sign-in app itself,
// such as a client secret rotated in the cloud but not in Loomplane.
// Signing in again with the same app won't help.
var ErrAppRejected = errors.New("the cloud refused Loomplane's sign-in app")

// Due reports whether the grant expires within the given time of now. A
// grant without an expiry, such as Vercel's, is never due.
func (g Grant) Due(now time.Time, within time.Duration) bool {
	if g.Expiry.IsZero() {
		return false
	}
	return !g.Expiry.After(now.Add(within))
}

// NewPKCE returns a PKCE verifier, kept until the code comes back, and
// its S256 challenge, sent with the sign-in.
func NewPKCE() (verifier, challenge string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate a PKCE verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}
