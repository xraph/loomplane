package connections

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/xraph/loomplane/internal/connections"
)

func TestFailMapsSignInErrors(t *testing.T) {
	for err, want := range map[error]int{
		connections.ErrAppLocked: http.StatusConflict,
		connections.ErrNoApp:     http.StatusUnprocessableEntity,
		fmt.Errorf("%w: no callback URL", connections.ErrUnavailable): http.StatusServiceUnavailable,
	} {
		var httpErr interface {
			error
			StatusCode() int
		}
		if !errors.As(fail(err), &httpErr) || httpErr.StatusCode() != want {
			t.Errorf("%v: got %v, want %d", err, httpErr, want)
		}
	}
}

func TestCallbackURLIsUnderThePublicURL(t *testing.T) {
	for _, public := range []string{"https://lp.example", "https://lp.example/"} {
		if got := callbackURL(public); got != "https://lp.example/integrations/oauth/callback" {
			t.Errorf("%q: got %q", public, got)
		}
	}
}
