package planning

import (
	"errors"
	"net/http"
	"testing"

	"github.com/xraph/loomplane/internal/recovery"
)

func TestRecoveryRefusalsAreUnprocessableWithTheirMessage(t *testing.T) {
	for _, err := range []error{recovery.ErrNothingToRestore, recovery.ErrRebuildable, recovery.ErrNotManaged} {
		var httpErr interface {
			error
			StatusCode() int
		}
		if !errors.As(fail(err), &httpErr) || httpErr.StatusCode() != http.StatusUnprocessableEntity || httpErr.Error() != err.Error() {
			t.Fatalf("%v: %+v", err, httpErr)
		}
	}
}
