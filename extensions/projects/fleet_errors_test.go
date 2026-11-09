package projects

import (
	"errors"
	"fmt"
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

func TestRetainedHistoryConflict(t *testing.T) {
	var response interface{ StatusCode() int }
	err := fail(fmt.Errorf("%w: %w", projects.ErrHistoryRetained, errors.New("foreign key")))
	if !errors.As(err, &response) || response.StatusCode() != http.StatusConflict {
		t.Fatalf("a delete that history blocks must return conflict, got %v", err)
	}
}
