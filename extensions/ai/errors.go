package ai

import (
	"errors"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/models/hfhub"
	"github.com/xraph/loomplane/internal/security"
)

// fail maps what the AI services return to an HTTP error with the
// service's sentence, for the AI routes. Anything it doesn't know stays a
// 500.
func fail(err error) error {
	switch {
	case errors.Is(err, aisettings.ErrInvalid), errors.Is(err, aisettings.ErrInUse),
		errors.Is(err, aisettings.ErrOverBudget), errors.Is(err, aisettings.ErrUnderAllocated):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, aisettings.ErrStale):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, aisettings.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, hfhub.ErrUnavailable):
		return forge.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}
	if status := security.StatusFor(err); status != http.StatusInternalServerError {
		return forge.NewHTTPError(status, err.Error())
	}
	return err
}
