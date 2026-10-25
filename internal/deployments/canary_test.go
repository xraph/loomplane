package deployments

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/releasehealth"
)

func TestCanaryJSONShape(t *testing.T) {
	keys := func(v any) []string {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err = json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return slices.Sorted(maps.Keys(m))
	}
	if got := keys(Deployment{}); slices.Contains(got, "canary") {
		t.Fatalf("a deployment without a canary shows one: %v", got)
	}
	d := Deployment{Canary: &Canary{
		Status: CanaryRamping, Steps: []int{10, 50}, HoldSeconds: 300, Thresholds: releasehealth.DefaultThresholds(),
		Step: 0, Weight: 10, Services: []CanaryService{{ActionID: "service:api", Name: "api"}}, UpdatedAt: time.Now(),
	}}
	if got := keys(d); !slices.Contains(got, "canary") {
		t.Fatalf("a canary deployment leaves it out: %v", got)
	}
	want := []string{"hold_seconds", "services", "status", "step", "steps", "thresholds", "updated_at", "weight"}
	if got := keys(d.Canary); !slices.Equal(got, want) {
		t.Fatalf("canary keys %v, want %v", got, want)
	}
}

func TestMemStoreRecordVerdictIsIdempotent(t *testing.T) {
	ctx := t.Context()
	m := &memStore{d: map[string]*Deployment{"dep": {ID: "dep", OrgID: "org", ProjectID: "proj", Status: StatusRunning}}}
	verdict := func(action string, step, attempt int, reason string) *CanaryVerdict {
		return &CanaryVerdict{OrgID: "org", ProjectID: "forged", DeploymentID: "dep", ActionID: action,
			Step: step, Attempt: attempt, Verdict: releasehealth.Pass, Reason: reason}
	}
	retried := verdict("service:web", 1, 0, "retried")
	retried.ID = "another-id"
	for _, v := range []*CanaryVerdict{
		verdict("service:web", 1, 0, "first"),
		retried,
		verdict("service:api", 1, 0, "api"),
		verdict("service:api", 0, 1, "held"),
		verdict("service:api", 0, 0, "start"),
	} {
		if err := m.RecordVerdict(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.Verdicts(ctx, "org", "dep")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(got))
	for _, v := range got {
		ids = append(ids, v.ID+" "+v.Reason)
	}
	want := []string{"dep/service:api/0/0 start", "dep/service:api/0/1 held", "dep/service:api/1/0 api", "dep/service:web/1/0 first"}
	if !slices.Equal(ids, want) {
		t.Fatalf("verdicts %v, want %v", ids, want)
	}
	for _, v := range got {
		if v.ProjectID != "proj" {
			t.Fatalf("verdict %s scoped to project %q", v.ID, v.ProjectID)
		}
	}
	foreign := verdict("service:api", 2, 0, "foreign")
	foreign.OrgID = "other"
	if err := m.RecordVerdict(ctx, foreign); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a verdict for another org's deployment: %v", err)
	}
	if got, _ = m.Verdicts(ctx, "other", "dep"); len(got) != 0 {
		t.Fatalf("another org's verdicts: %v", got)
	}
}

func TestReopenResetsTheCanary(t *testing.T) {
	ctx := t.Context()
	at := time.Now()
	m := &memStore{d: map[string]*Deployment{"dep": {
		ID: "dep", OrgID: "org", Status: StatusFailed, Error: "rolled back", FinishedAt: &at,
		Steps: []Step{{ActionID: "service:api", Status: StepRamping}, {ActionID: "service:web", Status: StepSkipped}},
		Canary: &Canary{Status: CanaryRolledBack, Steps: []int{10, 50, 100}, Step: 2, Weight: 50, Reason: "error rate",
			Services: []CanaryService{{ActionID: "service:api", Baseline: "rev-1", Candidate: "x", Image: "img:2"}}},
	}}}
	if err := m.RecordVerdict(ctx, &CanaryVerdict{ID: VerdictID("dep", "service:api", 2, 0), OrgID: "org", DeploymentID: "dep", ActionID: "service:api", Step: 2, Verdict: releasehealth.Fail}); err != nil {
		t.Fatal(err)
	}
	ok, err := m.Reopen(ctx, "org", "dep")
	if err != nil || !ok {
		t.Fatalf("reopen: %v %v", ok, err)
	}
	d, err := m.GetDeployment(ctx, "org", "dep")
	if err != nil {
		t.Fatal(err)
	}
	c := d.Canary
	if c == nil || c.Status != CanaryPending || c.Step != -1 || c.Weight != 0 || c.Reason != "" || !slices.Equal(c.Steps, []int{10, 50, 100}) {
		t.Fatalf("canary after reopen: %+v", c)
	}
	if s := c.Services[0]; s.Candidate != "" || s.Image != "" || s.Baseline != "rev-1" {
		t.Fatalf("service after reopen: %+v", s)
	}
	for _, s := range d.Steps {
		if s.Status != StepPending {
			t.Fatalf("step %s after reopen: %s", s.ActionID, s.Status)
		}
	}
	if v, err := m.Verdicts(ctx, "org", "dep"); err != nil || len(v) != 1 {
		t.Fatalf("verdicts after reopen: %v %v", v, err)
	}
}

// Planning owns which canary statuses hold an environment; they must be
// this package's names for them.
func TestActiveCanaryStatusesAreDeploymentStatuses(t *testing.T) {
	want := []string{CanaryPending, CanaryRamping, CanaryPromoting, CanaryReverting}
	if !slices.Equal(canaryActive, want) {
		t.Fatalf("active canary statuses %v, want %v", canaryActive, want)
	}
}
