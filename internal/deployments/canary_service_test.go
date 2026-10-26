package deployments

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/security"
)

// projectAuthz lets a principal read only the projects it names.
type projectAuthz struct{ read map[string]bool }

func (a projectAuthz) Require(_ context.Context, _ security.Principal, perm security.Permission, project string) error {
	if perm == security.DeploymentRead && a.read[project] {
		return nil
	}
	return security.ErrForbidden
}

func recordVerdicts(t *testing.T, m *memStore, dep string, retry int, steps ...int) {
	t.Helper()
	for _, step := range steps {
		v := &CanaryVerdict{OrgID: "org", DeploymentID: dep, Retry: retry, ActionID: "service:web", Service: "web", Step: step, Weight: 25, Verdict: releasehealth.Pass, Reason: fmt.Sprintf("retry %d step %d", retry, step)}
		if err := m.RecordVerdict(t.Context(), v); err != nil {
			t.Fatal(err)
		}
	}
}

func TestServiceCanaryNeedsDeploymentRead(t *testing.T) {
	m := &memStore{d: map[string]*Deployment{"dep": {
		ID: "dep", OrgID: "org", ProjectID: "proj", Status: StatusRunning,
		Canary: &Canary{Status: CanaryRamping, Steps: []int{5, 25}, Step: 1, Weight: 25},
	}}}
	recordVerdicts(t, m, "dep", 0, 1, 0)
	p := security.Principal{OrgID: "org", UserID: "user"}
	if _, err := NewService(m, projectAuthz{read: map[string]bool{"other": true}}, nil, nil).Canary(t.Context(), p, "dep"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a viewer of another project: %v", err)
	}
	report, err := NewService(m, projectAuthz{read: map[string]bool{"proj": true}}, nil, nil).Canary(t.Context(), p, "dep")
	if err != nil || report.Canary == nil || report.Canary.Status != CanaryRamping || len(report.Verdicts) != 2 || report.Verdicts[0].Step != 0 || report.Verdicts[1].Step != 1 {
		t.Fatalf("report: %+v %v", report, err)
	}
	if _, err = NewService(m, projectAuthz{read: map[string]bool{"proj": true}}, nil, nil).Canary(t.Context(), p, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing deployment: %v", err)
	}
}

func TestServiceCanaryOfAPlainDeployment(t *testing.T) {
	m := &memStore{d: map[string]*Deployment{"dep": {ID: "dep", OrgID: "org", ProjectID: "proj", Status: StatusSucceeded}}}
	report, err := NewService(m, projectAuthz{read: map[string]bool{"proj": true}}, nil, nil).Canary(t.Context(), security.Principal{OrgID: "org"}, "dep")
	if err != nil || report.Canary != nil || report.Verdicts == nil || len(report.Verdicts) != 0 {
		t.Fatalf("report: %+v %v", report, err)
	}
}

// A retried deployment ramps again and judges afresh, so the report shows
// the current retry's verdicts only.
func TestServiceCanaryShowsTheCurrentRetryOnly(t *testing.T) {
	m := &memStore{d: map[string]*Deployment{"dep": {
		ID: "dep", OrgID: "org", ProjectID: "proj", Status: StatusRunning, Retries: 1,
		Canary: &Canary{Status: CanaryRamping, Steps: []int{5, 25}, Step: 0, Weight: 5},
	}}}
	recordVerdicts(t, m, "dep", 0, 0, 1)
	recordVerdicts(t, m, "dep", 1, 0)
	svc := NewService(m, projectAuthz{read: map[string]bool{"proj": true}}, nil, nil)
	report, err := svc.Canary(t.Context(), security.Principal{OrgID: "org"}, "dep")
	if err != nil || len(report.Verdicts) != 1 || report.Verdicts[0].Retry != 1 || report.Verdicts[0].Reason != "retry 1 step 0" {
		t.Fatalf("report: %+v %v", report, err)
	}
}

func TestRecentCanariesNewestFirst(t *testing.T) {
	now := time.Now().UTC()
	canary := func() *Canary { return &Canary{Status: CanaryPromoted, Steps: []int{100}, Step: 0, Weight: 100} }
	dep := func(id string, age time.Duration, c *Canary, retries int) *Deployment {
		return &Deployment{ID: id, OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Environment: "production", PlanID: "plan_" + id, Status: StatusSucceeded, Canary: c, Retries: retries, StartedAt: now.Add(-age)}
	}
	m := &memStore{d: map[string]*Deployment{
		"old": dep("old", 3*time.Hour, canary(), 0), "mid": dep("mid", 2*time.Hour, canary(), 1),
		"new": dep("new", time.Hour, canary(), 0), "plain": dep("plain", 30*time.Minute, nil, 0),
	}}
	recordVerdicts(t, m, "mid", 0, 0)
	recordVerdicts(t, m, "mid", 1, 0, 1)
	recordVerdicts(t, m, "new", 0, 0)
	svc := NewService(m, projectAuthz{read: map[string]bool{"proj": true}}, nil, nil)
	p := security.Principal{OrgID: "org"}
	runs, err := svc.RecentCanaries(t.Context(), p, "proj", "env", 2)
	if err != nil || len(runs) != 2 || runs[0].DeploymentID != "new" || runs[1].DeploymentID != "mid" {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	if runs[0].PlanID != "plan_new" || runs[0].Environment != "production" || runs[0].Canary == nil || len(runs[0].Verdicts) != 1 || len(runs[1].Verdicts) != 2 {
		t.Fatalf("run contents: %+v", runs)
	}
	for _, tc := range []struct{ limit, want int }{{0, 1}, {-3, 1}, {500, 3}} {
		runs, err = svc.RecentCanaries(t.Context(), p, "proj", "env", tc.limit)
		if err != nil || len(runs) != tc.want {
			t.Fatalf("limit %d: %d runs, %v", tc.limit, len(runs), err)
		}
	}
	if _, err = NewService(m, projectAuthz{}, nil, nil).RecentCanaries(t.Context(), p, "proj", "env", 2); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("without deployment:read: %v", err)
	}
}
