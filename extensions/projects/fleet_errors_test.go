package projects

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
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

func TestHeldFrontendsConflict(t *testing.T) {
	var response interface{ StatusCode() int }
	err := fail(fmt.Errorf("%w: website in production (prj_7): remove it: boom", projects.ErrFrontendsHeld))
	if !errors.As(err, &response) || response.StatusCode() != http.StatusConflict || !strings.Contains(err.Error(), "prj_7") {
		t.Fatalf("a delete that a held frontend blocks must return conflict naming it, got %v", err)
	}
}
