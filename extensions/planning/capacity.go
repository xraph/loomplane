package planning

import (
	"context"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/security"
)

// CapacityWaitInput names the wait revision the caller read. A wait that
// moved since then answers 409.
type CapacityWaitInput struct {
	ExpectedRevision int64 `json:"expected_revision"`
}

func (e *Extension) resumeCapacity(c forge.Context) error {
	return e.capacityWait(c, e.deploys.ResumeCapacity)
}

func (e *Extension) cancelCapacity(c forge.Context) error {
	return e.capacityWait(c, e.deploys.CancelCapacityWait)
}

// capacityWait only records the decision. The workflow picks a resume up from
// the durable capacity event, so the handler never signals it directly.
func (e *Extension) capacityWait(c forge.Context, decide func(context.Context, security.Principal, string, int64) (*deployments.Deployment, error)) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in CapacityWaitInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"expected_revision": 1}`)
	}
	d, err := decide(c.Request().Context(), p, c.Param("id"), in.ExpectedRevision)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, d)
}
