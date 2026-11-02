package planning

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/security"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
)

func TestCleanupReviewPublicProjection(t *testing.T) {
	v := &deployments.CleanupReview{Variant: "managed_workload", Revision: "original", Targets: []planning.ManagedCleanupTarget{{AttachmentID: "private-attachment", Unresolved: []string{"private-write"}}}}
	raw, err := json.Marshal(publicCleanupReview(v))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-") || !strings.Contains(string(raw), `"unresolved_writes":1`) {
		t.Fatalf("unsafe review %s", raw)
	}
	if v.Targets[0].AttachmentID != "private-attachment" {
		t.Fatal("mutated authority")
	}
	progress := publicCleanupProgress(&deployments.FailedCleanup{Variant: "managed_workload", Error: "private-internal-error"})
	if strings.Contains(progress.Error, "private-") {
		t.Fatal("exposed execution error")
	}
}

type cancelCleanupService struct {
	failedCleanupService
	review string
	calls  int
}

func (s *cancelCleanupService) Cancel(ctx context.Context, p security.Principal, deployment, review string) (*deployments.FailedCleanup, error) {
	s.calls++
	caller, ok := security.PrincipalFrom(ctx)
	if !ok || caller != p {
		return nil, security.ErrForbidden
	}
	if p.UserID != "ada" || deployment != "deployment" {
		return nil, security.ErrForbidden
	}
	if review != s.review {
		return nil, planning.ErrManagedFailedCleanupConflict
	}
	return &deployments.FailedCleanup{Variant: "managed_workload", ReviewID: review, Status: "cancelled"}, nil
}
func TestManagedCleanupCancellationUsesSelectedReview(t *testing.T) {
	svc := &cancelCleanupService{review: "review-b"}
	e := &Extension{failedCleanups: svc}
	r := forge.NewRouter()
	if err := r.POST("/deployments/:id/cleanup/cancel", e.cancelFailedCleanup); err != nil {
		t.Fatal(err)
	}
	missing := send(t, r, http.MethodPost, "/deployments/deployment/cleanup/cancel", `{}`, true)
	if missing.Code != 400 || svc.calls != 0 {
		t.Fatal("missing expected review reached cancellation")
	}
	stale := send(t, r, http.MethodPost, "/deployments/deployment/cleanup/cancel", `{"expected_review_id":"review-a"}`, true)
	if stale.Code != 409 || !strings.Contains(stale.Body.String(), "stale_cleanup_review") {
		t.Fatalf("stale review: %d %s", stale.Code, stale.Body)
	}
	denied := send(t, r, http.MethodPost, "/deployments/deployment/cleanup/cancel", `{"expected_review_id":"review-b"}`, false)
	if denied.Code != 401 {
		t.Fatalf("unsigned request: %d", denied.Code)
	}
	current := send(t, r, http.MethodPost, "/deployments/deployment/cleanup/cancel", `{"expected_review_id":"review-b"}`, true)
	if current.Code != 200 || !strings.Contains(current.Body.String(), "review-b") {
		t.Fatalf("current review: %d %s", current.Code, current.Body)
	}
}

func TestStalePlanAndCloudReviewsHaveTypedBlocker(t *testing.T) {
	for _, reason := range []error{planning.ErrStale, cloudprofiles.ErrConflict} {
		r := forge.NewRouter()
		if err := r.POST("/review", func(c forge.Context) error { return cloudResult(c, http.StatusOK, nil, reason) }); err != nil {
			t.Fatal(err)
		}
		out := send(t, r, http.MethodPost, "/review", `{}`, true)
		if out.Code != http.StatusConflict || !strings.Contains(out.Body.String(), `"code":"stale_review"`) {
			t.Fatalf("stale response: %d %s", out.Code, out.Body)
		}
	}
}

func TestCleanupProgressRequiresAccessUpgrade(t *testing.T) {
	progress := publicCleanupProgress(&deployments.FailedCleanup{Variant: "managed_workload", Error: "private-attachment: cleanup_permissions_unavailable"})
	if progress.ErrorCode != "runtime_access_upgrade_required" || strings.Contains(progress.Error, "private-") {
		t.Fatalf("unsafe or unactionable progress: %+v", progress)
	}
}
