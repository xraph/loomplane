// Package oauth describes signing in to a cloud: the app Loomplane is
// registered as with it, and the grant it hands back. Each cloud's package
// does its own authorize, exchange and refresh calls and returns these.
package oauth

import (
	"errors"
	"time"
)

// App is Loomplane's registration with a cloud.
type App struct {
	ClientID     string
	ClientSecret string
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
// spent, the grant was revoked, or the app isn't known to it.
var ErrRefused = errors.New("the cloud refused the sign-in")

// Due reports whether the grant expires within the given time of now.
func (g Grant) Due(now time.Time, within time.Duration) bool {
	return !g.Expiry.After(now.Add(within))
}
