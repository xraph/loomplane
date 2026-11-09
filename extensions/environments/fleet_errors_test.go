package environments

import (
	"errors"
	"net/http"
	"testing"

	"github.com/xraph/loomplane/internal/projects"
)

func TestFleetManagedConflict(t *testing.T) {
	var response interface{ StatusCode() int }
	err := fail(projects.ErrFleetManaged)
	if !errors.As(err, &response) || response.StatusCode() != http.StatusConflict {
		t.Fatalf("managed instance write must return conflict, got %v", err)
	}
}
