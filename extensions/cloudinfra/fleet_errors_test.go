package cloudinfra

import (
	"errors"
	"net/http"
	"testing"

	"github.com/xraph/loomplane/internal/projects"
)

func TestFleetManagedConflict(t *testing.T) {
	var response interface{ StatusCode() int }
	err := result(nil, 0, nil, projects.ErrFleetManaged)
	if !errors.As(err, &response) || response.StatusCode() != http.StatusConflict {
		t.Fatalf("fleet route conflict: %v", err)
	}
}
