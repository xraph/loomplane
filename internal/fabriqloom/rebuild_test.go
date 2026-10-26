package fabriqloom_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/fabriqloom"
	"github.com/xraph/loomplane/internal/security"
)

// memRebuilds is a RebuildStore in memory, with the table's rules: one
// request id per organization, and one queued or running rebuild per
// resource and projection.
type memRebuilds struct {
	mu   sync.Mutex
	rows []fabriqloom.Rebuild
}

func (s *memRebuilds) Create(_ context.Context, r *fabriqloom.Rebuild) (*fabriqloom.Rebuild, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.rows {
		if x.OrgID == r.OrgID && x.RequestID == r.RequestID {
			if x.EnvironmentID != r.EnvironmentID || x.Resource != r.Resource || x.Projection != r.Projection {
				return nil, fabriqloom.ErrRequestConflict
			}
			return &x, nil
		}
	}
	for _, x := range s.rows {
		if x.OrgID == r.OrgID && x.EnvironmentID == r.EnvironmentID && x.Resource == r.Resource && x.Projection == r.Projection && (x.Status == "queued" || x.Status == "running") {
			return &x, nil
		}
	}
	row := *r
	row.Status = "queued"
	s.rows = append(s.rows, row)
	return &row, nil
}

func (s *memRebuilds) List(_ context.Context, orgID string, ref deployments.ResourceRef) ([]fabriqloom.Rebuild, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []fabriqloom.Rebuild{}
	for i := len(s.rows) - 1; i >= 0; i-- {
		x := s.rows[i]
		if x.OrgID == orgID && x.ProjectID == ref.ProjectID && x.EnvironmentID == ref.EnvironmentID && x.Resource == ref.Resource {
			out = append(out, x)
		}
	}
	return out, nil
}

func (s *memRebuilds) Claim(_ context.Context, orgID, id, attempt string) (*fabriqloom.Rebuild, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range s.rows {
		if x.OrgID == orgID && x.ID == id && x.Status == "queued" {
			s.rows[i].Status, s.rows[i].Attempt = "running", attempt
			r := s.rows[i]
			return &r, nil
		}
	}
	return nil, fabriqloom.ErrRebuildClaimed
}

func (s *memRebuilds) Finish(_ context.Context, orgID, id, attempt, status, output string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range s.rows {
		if x.OrgID == orgID && x.ID == id && x.Attempt == attempt && x.Status == "running" {
			s.rows[i].Status, s.rows[i].Output, s.rows[i].FinishedAt = status, output, &at
			return nil
		}
	}
	return fabriqloom.ErrRebuildClaimed
}

func (s *memRebuilds) Unfinished(context.Context) ([]fabriqloom.Rebuild, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []fabriqloom.Rebuild
	for _, x := range s.rows {
		if x.Status == "queued" || x.Status == "running" {
			out = append(out, x)
		}
	}
	return out, nil
}

func (s *memRebuilds) get(id string) fabriqloom.Rebuild {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.rows {
		if x.ID == id {
			return x
		}
	}
	return fabriqloom.Rebuild{}
}

func jobName(id, attempt string) string {
	sum := sha256.Sum256([]byte("rebuild/" + id + "/" + attempt))
	return "lp-job-" + hex.EncodeToString(sum[:])[:12]
}

// listed is the rebuild as Rebuilds lists it.
func listed(t *testing.T, h *serviceHarness, id string) fabriqloom.Rebuild {
	t.Helper()
	list, err := h.svc.Rebuilds(t.Context(), as("viewer"), fabriqRef)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("rebuild %s isn't listed", id)
	return fabriqloom.Rebuild{}
}

func TestRebuildRunsTheJobOnce(t *testing.T) {
	h := newServiceHarness(nil, "acme")
	ctx := t.Context()
	first, err := h.svc.RequestRebuild(ctx, as("developer"), fabriqRef, "graph", "request-0001")
	if err != nil || first.Status != "queued" || first.Projection != "graph" || first.CreatedBy != "developer" {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := h.svc.RequestRebuild(ctx, as("developer"), fabriqRef, "graph", "request-0002")
	if err != nil || second.ID != first.ID {
		t.Fatalf("a second request while one is queued: %+v %v", second, err)
	}
	if list, _ := h.svc.Rebuilds(ctx, as("viewer"), fabriqRef); len(list) != 1 {
		t.Fatalf("records: %+v", list)
	}
	if _, err = h.svc.RequestRebuild(ctx, as("viewer"), fabriqRef, "graph", "request-0003"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("viewer: %v", err)
	}

	h.catalog.result = owned.JobResult{ExitCode: 1, Output: "tenant acme: rebuilt\ntenant globex: falkordb: connection reset"}
	if err = h.svc.RunRebuild(ctx, "org", first.ID); err != nil {
		t.Fatal(err)
	}
	jobs := h.catalog.ran()
	attempt := h.rebuilds.get(first.ID).Attempt
	if len(jobs) != 1 || jobs[0].Component != "rebuild-graph" || jobs[0].Ref != fabriqRef || attempt == "" || jobs[0].Name != jobName(first.ID, attempt) {
		t.Fatalf("jobs: %+v (attempt %q)", jobs, attempt)
	}
	got := listed(t, h, first.ID)
	if got.Status != "failed" || !got.Retryable || !strings.Contains(got.Output, "falkordb: connection reset") || got.FinishedAt == nil {
		t.Fatalf("exit 1: %+v", got)
	}
	// A claimed rebuild doesn't run again.
	if err = h.svc.RunRebuild(ctx, "org", first.ID); err != nil || len(h.catalog.ran()) != 1 {
		t.Fatalf("second run: %v, %d jobs", err, len(h.catalog.ran()))
	}

	// Once it's finished, a new request queues a new rebuild.
	next, err := h.svc.RequestRebuild(ctx, as("developer"), fabriqRef, "graph", "request-0004")
	if err != nil || next.ID == first.ID {
		t.Fatalf("after a failure: %+v %v", next, err)
	}
	h.catalog.result = owned.JobResult{ExitCode: 2, Output: "unknown flag: --drop-old"}
	if err = h.svc.RunRebuild(ctx, "org", next.ID); err != nil {
		t.Fatal(err)
	}
	got = listed(t, h, next.ID)
	const code2 = "rebuild exited with code 2: Loomplane's Fabriq recipe passed bad arguments; retrying won't help."
	if got.Status != "failed" || got.Retryable || !strings.HasPrefix(got.Output, code2) || !strings.Contains(got.Output, "unknown flag") {
		t.Fatalf("exit 2: %+v", got)
	}

	ok, err := h.svc.RequestRebuild(ctx, as("developer"), fabriqRef, "graph", "request-0005")
	if err != nil {
		t.Fatal(err)
	}
	h.catalog.result = owned.JobResult{ExitCode: 0, Output: "tenant acme: rebuilt"}
	if err = h.svc.RunRebuild(ctx, "org", ok.ID); err != nil {
		t.Fatal(err)
	}
	if got = listed(t, h, ok.ID); got.Status != "succeeded" || got.Retryable || got.Output != "tenant acme: rebuilt" {
		t.Fatalf("exit 0: %+v", got)
	}
	if !slices.Equal(h.enqueued, []string{"org/" + first.ID, "org/" + first.ID, "org/" + next.ID, "org/" + ok.ID}) {
		t.Fatalf("enqueued: %v", h.enqueued)
	}

	// Elasticsearch is off for this Fabriq; FalkorDB is turned off too.
	if _, err = h.svc.RequestRebuild(ctx, as("developer"), fabriqRef, "search", "request-0006"); !errors.Is(err, fabriqloom.ErrInvalid) || err.Error() != "Elasticsearch is off for this Fabriq" {
		t.Fatalf("search: %v", err)
	}
	h.catalog.on["falkordb"] = false
	if _, err = h.svc.RequestRebuild(ctx, as("developer"), fabriqRef, "graph", "request-0007"); !errors.Is(err, fabriqloom.ErrInvalid) || err.Error() != "FalkorDB is off for this Fabriq" {
		t.Fatalf("falkordb off: %v", err)
	}
	if _, err = h.svc.RequestRebuild(ctx, as("developer"), fabriqRef, "vectors", "request-0008"); !errors.Is(err, fabriqloom.ErrInvalid) {
		t.Fatalf("an unknown projection: %v", err)
	}
	if _, err = h.svc.RequestRebuild(ctx, as("developer"), fabriqRef, "graph", "short"); !errors.Is(err, fabriqloom.ErrInvalid) {
		t.Fatalf("a short request id: %v", err)
	}
}

func TestRebuildRunRechecksPermission(t *testing.T) {
	h := newServiceHarness(nil, "acme")
	r, err := h.svc.RequestRebuild(t.Context(), as("developer"), fabriqRef, "graph", "request-0001")
	if err != nil {
		t.Fatal(err)
	}
	// The developer lost the role before the worker ran.
	h.catalog.mu.Lock()
	h.catalog.roles = map[string][]security.Permission{"developer": {security.DeploymentRead}}
	h.catalog.mu.Unlock()
	if err = h.svc.RunRebuild(t.Context(), "org", r.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(h.catalog.ran()); n != 0 {
		t.Fatalf("RunJob ran %d times", n)
	}
	got := h.rebuilds.get(r.ID)
	if got.Status != "failed" || got.Output != security.ErrForbidden.Error() {
		t.Fatalf("rebuild: %+v", got)
	}
}

func TestRebuildReconcileRequeuesAndFailsStuckRuns(t *testing.T) {
	h := newServiceHarness(nil, "acme")
	h.catalog.on["elasticsearch"] = true
	queued, err := h.svc.RequestRebuild(t.Context(), as("developer"), fabriqRef, "graph", "request-0001")
	if err != nil {
		t.Fatal(err)
	}
	stuck, err := h.svc.RequestRebuild(t.Context(), as("developer"), fabriqRef, "search", "request-0002")
	if err != nil {
		t.Fatal(err)
	}
	// A worker claimed the search rebuild 80 minutes ago and never came back.
	claimed := harnessNow.Add(-80 * time.Minute)
	if _, err = h.rebuilds.Claim(t.Context(), "org", stuck.ID, fabriqloom.RebuildAttempt(claimed)); err != nil {
		t.Fatal(err)
	}
	h.enqueued = nil
	if err = h.svc.ReconcileRebuilds(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.enqueued, []string{"org/" + queued.ID}) {
		t.Fatalf("enqueued: %v", h.enqueued)
	}
	got := listed(t, h, stuck.ID)
	if got.Status != "failed" || !got.Retryable || got.Output == "" {
		t.Fatalf("stuck: %+v", got)
	}
	// One claimed a minute ago is still running.
	fresh, err := h.svc.RequestRebuild(t.Context(), as("developer"), fabriqRef, "search", "request-0003")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.rebuilds.Claim(t.Context(), "org", fresh.ID, fabriqloom.RebuildAttempt(harnessNow.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err = h.svc.ReconcileRebuilds(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got = listed(t, h, fresh.ID); got.Status != "running" {
		t.Fatalf("fresh: %+v", got)
	}
}
