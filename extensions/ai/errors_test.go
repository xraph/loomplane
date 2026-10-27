package ai

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/models/hfhub"
	"github.com/xraph/loomplane/internal/security"
)

func TestFailMapsServiceErrors(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w: a name is lower case", aisettings.ErrInvalid), http.StatusUnprocessableEntity},
		{aisettings.ErrInUse, http.StatusUnprocessableEntity},
		{fmt.Errorf("%w by $10.00", aisettings.ErrOverBudget), http.StatusUnprocessableEntity},
		{aisettings.ErrUnderAllocated, http.StatusUnprocessableEntity},
		{aisettings.ErrStale, http.StatusConflict},
		{aisettings.ErrNotFound, http.StatusNotFound},
		{fmt.Errorf("check the token: %w", hfhub.ErrUnavailable), http.StatusServiceUnavailable},
		{fmt.Errorf("%w (ai:manage)", security.ErrForbidden), http.StatusForbidden},
	} {
		var httpErr interface {
			error
			StatusCode() int
		}
		got := fail(c.err)
		if !errors.As(got, &httpErr) || httpErr.StatusCode() != c.want {
			t.Errorf("%v: %v, want %d", c.err, got, c.want)
		}
	}
	plain := errors.New("database is down")
	if got := fail(plain); !errors.Is(got, plain) || got.Error() != plain.Error() {
		t.Errorf("unknown error became %v", got)
	}
}
