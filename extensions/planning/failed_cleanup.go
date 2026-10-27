package planning

import (
	"context"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/security"
)

// FailedCleanupInput approves the exact deployment evidence shown in the review.
type FailedCleanupInput struct {
	Revision string `json:"revision"`
}

func (e *Extension) previewFailedCleanup(c forge.Context) error {
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		return e.failedCleanups.Preview(ctx, p, c.Param("id"))
	})
}
func (e *Extension) applyFailedCleanup(c forge.Context) error {
	var in FailedCleanupInput
	if err := c.Bind(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "revision is required")
	}
	return cloudCall(c, http.StatusAccepted, func(ctx context.Context, p security.Principal) (any, error) {
		return e.failedCleanups.Apply(ctx, p, c.Param("id"), in.Revision)
	})
}
func (e *Extension) getFailedCleanup(c forge.Context) error {
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		return e.failedCleanups.Get(ctx, p, c.Param("id"))
	})
}
func (e *Extension) retryFailedCleanup(c forge.Context) error {
	return cloudCall(c, http.StatusAccepted, func(ctx context.Context, p security.Principal) (any, error) {
		return e.failedCleanups.Retry(ctx, p, c.Param("id"))
	})
}
