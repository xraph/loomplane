package planning

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
)

func TestPromotionFromAnEnvironmentThatRunsNothingIsAConflict(t *testing.T) {
	for _, err := range []error{planning.ErrNotRunning, fmt.Errorf("promote from staging: %w", planning.ErrNotRunning)} {
		var httpErr interface {
			error
			StatusCode() int
		}
		if !errors.As(fail(err), &httpErr) || httpErr.StatusCode() != http.StatusConflict || httpErr.Error() != err.Error() {
			t.Fatalf("%v: %+v", err, httpErr)
		}
	}
}
