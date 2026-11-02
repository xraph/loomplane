package planning

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/publicplan"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/client"
)

type failedCleanupService interface {
	Preview(context.Context, security.Principal, string) (*deployments.CleanupReview, error)
	Apply(context.Context, security.Principal, string, string) (*deployments.FailedCleanup, error)
	Get(context.Context, security.Principal, string) (*deployments.FailedCleanup, error)
	Retry(context.Context, security.Principal, string) (*deployments.FailedCleanup, error)
	Cancel(context.Context, security.Principal, string, string) (*deployments.FailedCleanup, error)
}

// FailedCleanupInput approves the exact deployment evidence shown in the review.
type FailedCleanupInput struct {
	Revision string `json:"revision"`
}

func (e *Extension) previewFailedCleanup(c forge.Context) error {
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		v, err := e.failedCleanups.Preview(ctx, p, c.Param("id"))
		if err != nil {
			return nil, err
		}
		return publicCleanupReview(v), nil
	})
}
func (e *Extension) applyFailedCleanup(c forge.Context) error {
	var in FailedCleanupInput
	if err := c.Bind(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "revision is required")
	}
	return cloudCall(c, http.StatusAccepted, func(ctx context.Context, p security.Principal) (any, error) {
		v, err := e.failedCleanups.Apply(ctx, p, c.Param("id"), in.Revision)
		return publicCleanupProgress(v), err
	})
}
func (e *Extension) getFailedCleanup(c forge.Context) error {
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		v, err := e.failedCleanups.Get(ctx, p, c.Param("id"))
		return publicCleanupProgress(v), err
	})
}
func (e *Extension) retryFailedCleanup(c forge.Context) error {
	return cloudCall(c, http.StatusAccepted, func(ctx context.Context, p security.Principal) (any, error) {
		v, err := e.failedCleanups.Retry(ctx, p, c.Param("id"))
		return publicCleanupProgress(v), err
	})
}

// CleanupReview is the reviewable effect, without private execution targets.
type CleanupReview struct {
	Blockers         []string                   `json:"blockers,omitempty"`
	PreviousReviewID string                     `json:"previous_review_id,omitempty"`
	Variant          string                     `json:"variant,omitempty"`
	ReviewSequence   int64                      `json:"review_sequence,omitempty"`
	Targets          []publicplan.CleanupTarget `json:"targets,omitempty"`
	DeploymentID     string                     `json:"deployment_id"`
	Revision         string                     `json:"revision"`
	Resource         string                     `json:"resource"`
	Provider         string                     `json:"provider"`
	Region           string                     `json:"region"`
	ConnectionID     string                     `json:"connection_id"`
	Effect           string                     `json:"effect"`
}

func publicCleanupReview(v *deployments.CleanupReview) *CleanupReview {
	if v == nil {
		return nil
	}
	return &CleanupReview{Blockers: v.Blockers, PreviousReviewID: v.PreviousReviewID, Variant: v.Variant, ReviewSequence: v.ReviewSequence, Targets: publicplan.CleanupTargets(v.Targets), DeploymentID: v.DeploymentID, Revision: v.Revision, Resource: v.Resource, Provider: v.Provider, Region: v.Region, ConnectionID: v.ConnectionID, Effect: v.Effect}
}

// FailedCleanupCancelInput fences only the accepted review you selected.
type FailedCleanupCancelInput struct {
	ExpectedReviewID string `json:"expected_review_id"`
}

func (e *Extension) cancelFailedCleanup(c forge.Context) error {
	var in FailedCleanupCancelInput
	if err := c.Bind(&in); err != nil || in.ExpectedReviewID == "" {
		return forge.NewHTTPError(http.StatusBadRequest, "expected_review_id is required")
	}
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		v, err := e.failedCleanups.Cancel(ctx, p, c.Param("id"), in.ExpectedReviewID)
		return publicCleanupProgress(v), err
	})
}

func publicCleanupProgress(v *deployments.FailedCleanup) *client.FailedCleanup {
	if v == nil {
		return nil
	}
	out := client.FailedCleanup{Variant: v.Variant, ReviewID: v.ReviewID, ReviewSequence: v.ReviewSequence, DeploymentID: v.DeploymentID, Revision: v.Revision, Status: v.Status, Attempt: v.Attempt, Error: v.Error}
	if out.Variant == "managed_workload" && out.Error != "" {
		out.ErrorCode = "managed_cleanup_incomplete"
		out.Error = "Cleanup has not completed. Refresh the review and reconcile unresolved writes before retrying."
		if strings.Contains(v.Error, "cleanup_permissions_unavailable") {
			out.ErrorCode = "runtime_access_upgrade_required"
			out.Error = "Review a runtime access upgrade with a current cluster management grant before retrying cleanup."
		}
	}
	return &out
}

type CleanupProblem struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func cleanupProblem(c forge.Context, err error) (bool, error) {
	var p CleanupProblem
	status := http.StatusConflict
	switch {
	case errors.Is(err, planning.ErrHistoricalFailedCleanup):
		p = CleanupProblem{Code: "historical_cleanup_review_required", Message: "Reconcile historical cleanup effects, cancel the previous accepted review and request a fresh review."}
	case errors.Is(err, planning.ErrManagedFailedCleanupConflict):
		p = CleanupProblem{Code: "stale_cleanup_review", Message: "The cleanup review changed. Reload and review the current target before continuing.", Retryable: true}
	case errors.Is(err, planning.ErrStale), errors.Is(err, cloudprofiles.ErrConflict):
		p = CleanupProblem{Code: "stale_review", Message: "The reviewed state changed. Reload and create a fresh review.", Retryable: true}
	case errors.Is(err, planning.ErrManagedFailedCleanupUnavailable):
		status = http.StatusServiceUnavailable
		p = CleanupProblem{Code: "managed_cleanup_unavailable", Message: "Managed cleanup is unavailable. Existing resource holds remain in place."}
	default:
		return false, nil
	}
	return true, c.JSON(status, p)
}
