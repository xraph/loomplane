package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// fakeReleases records what the release routes ask of planning, and
// answers with err when it's set.
type fakeReleases struct {
	err       error
	created   []planning.CreatePlanInput
	targets   [][3]string // project, environment, deployment
	rollbacks []planning.RollbackInput
	projects  []string
}

func (f *fakeReleases) Create(_ context.Context, _ security.Principal, projectID string, in planning.CreatePlanInput) (*planning.Plan, error) {
	f.projects = append(f.projects, projectID)
	f.created = append(f.created, in)
	if f.err != nil {
		return nil, f.err
	}
	return &planning.Plan{ID: "plan_1", Environment: in.Environment, Kind: planning.KindPromotion, From: in.From}, nil
}

func (f *fakeReleases) RollbackTarget(_ context.Context, _ security.Principal, projectID, environment, deploymentID string) (*planning.RollbackTarget, error) {
	f.targets = append(f.targets, [3]string{projectID, environment, deploymentID})
	if f.err != nil {
		return nil, f.err
	}
	return &planning.RollbackTarget{
		Environment: environment,
		Target:      planning.ReleaseRef{DeploymentID: "dep_3", PlanID: "plan_3", Kind: planning.KindRelease, Status: "succeeded"},
		Restore:     planning.ReleaseRef{DeploymentID: "dep_1", PlanID: "plan_1", Kind: planning.KindRelease, Status: "succeeded"},
		Commits:     []planning.RollbackCommit{{Alias: "web", From: "bbbbbbb", To: "aaaaaaa"}},
	}, nil
}

func (f *fakeReleases) CreateRollback(_ context.Context, _ security.Principal, projectID string, in planning.RollbackInput) (*planning.Plan, error) {
	f.projects = append(f.projects, projectID)
	f.rollbacks = append(f.rollbacks, in)
	if f.err != nil {
		return nil, f.err
	}
	return &planning.Plan{ID: "plan_r", Environment: in.Environment, Kind: planning.KindRollback}, nil
}

func releaseRouter(t *testing.T, svc releaseService) forge.Router {
	t.Helper()
	r := forge.NewRouter()
	if err := releaseRoutes(r, nil, svc); err != nil {
		t.Fatal(err)
	}
	return r
}

func send(t *testing.T, r forge.Router, method, path, body string, signedIn bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if signedIn {
		req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "ada"}))
	}
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	return out
}

func TestCreatePlanRouteAcceptsFrom(t *testing.T) {
	svc := &fakeReleases{}
	r := releaseRouter(t, svc)
	const body = `{"environment":"production","from":"staging"}`

	out := send(t, r, http.MethodPost, "/api/v1/projects/p/plans", body, true)
	if out.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", out.Code, out.Body.String())
	}
	want := []planning.CreatePlanInput{{Environment: "production", From: "staging"}}
	if !reflect.DeepEqual(svc.created, want) || !reflect.DeepEqual(svc.projects, []string{"p"}) {
		t.Fatalf("Create got %+v for %v", svc.created, svc.projects)
	}
	var plan planning.Plan
	if err := json.Unmarshal(out.Body.Bytes(), &plan); err != nil || plan.ID != "plan_1" || plan.From != "staging" {
		t.Fatalf("plan %+v: %v", plan, err)
	}

	svc.err = fmt.Errorf("promote from staging: %w", planning.ErrNotRunning)
	if out = send(t, r, http.MethodPost, "/api/v1/projects/p/plans", body, true); out.Code != http.StatusConflict || !strings.Contains(out.Body.String(), "runs nothing yet") {
		t.Fatalf("an environment that runs nothing: %d %s", out.Code, out.Body.String())
	}
	if out = send(t, r, http.MethodPost, "/api/v1/projects/p/plans", body, false); out.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d %s", out.Code, out.Body.String())
	}
	if out = send(t, r, http.MethodPost, "/api/v1/projects/p/plans", `{"environment":`, true); out.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON: %d %s", out.Code, out.Body.String())
	}
}

func TestRollbackRoutes(t *testing.T) {
	svc := &fakeReleases{}
	r := releaseRouter(t, svc)

	out := send(t, r, http.MethodGet, "/api/v1/projects/p/environments/production/rollback-target", "", true)
	if out.Code != http.StatusOK {
		t.Fatalf("target: %d %s", out.Code, out.Body.String())
	}
	var target planning.RollbackTarget
	if err := json.Unmarshal(out.Body.Bytes(), &target); err != nil || target.Target.DeploymentID != "dep_3" || target.Restore.DeploymentID != "dep_1" || len(target.Commits) != 1 {
		t.Fatalf("target %+v: %v", target, err)
	}
	send(t, r, http.MethodGet, "/api/v1/projects/p/environments/production/rollback-target?deployment_id=dep_3", "", true)
	if want := [][3]string{{"p", "production", ""}, {"p", "production", "dep_3"}}; !reflect.DeepEqual(svc.targets, want) {
		t.Fatalf("RollbackTarget got %v", svc.targets)
	}

	out = send(t, r, http.MethodPost, "/api/v1/projects/p/rollbacks", `{"environment":"production","reason":"errors"}`, true)
	if out.Code != http.StatusCreated {
		t.Fatalf("rollback: %d %s", out.Code, out.Body.String())
	}
	var plan planning.Plan
	if err := json.Unmarshal(out.Body.Bytes(), &plan); err != nil || plan.ID != "plan_r" || plan.Kind != planning.KindRollback {
		t.Fatalf("plan %+v: %v", plan, err)
	}
	if want := []planning.RollbackInput{{Environment: "production", Reason: "errors"}}; !reflect.DeepEqual(svc.rollbacks, want) || svc.projects[0] != "p" {
		t.Fatalf("CreateRollback got %+v for %v", svc.rollbacks, svc.projects)
	}

	// Each refusal reaches the caller with its message.
	for _, tc := range []struct {
		err  error
		code int
	}{
		{planning.ErrNoPreviousRelease, http.StatusConflict},
		{planning.ErrReleaseRunning, http.StatusConflict},
		{fmt.Errorf("the canary already returned production to the baseline: %w", planning.ErrCanaryReverted), http.StatusConflict},
		{fmt.Errorf("%w: the current release is a rollback; deploy or promote to move forward", planning.ErrInvalid), http.StatusUnprocessableEntity},
		{fmt.Errorf("%w: the current release is a rollback that failed; retry its deployment dep_4 to finish it, or deploy or promote to move forward", planning.ErrInvalid), http.StatusUnprocessableEntity},
		{fmt.Errorf("%w: say why you're rolling back", planning.ErrInvalid), http.StatusUnprocessableEntity},
	} {
		svc.err = tc.err
		for _, req := range []struct{ method, path, body string }{
			{http.MethodGet, "/api/v1/projects/p/environments/production/rollback-target", ""},
			{http.MethodPost, "/api/v1/projects/p/rollbacks", `{"environment":"production","reason":"errors"}`},
		} {
			out = send(t, r, req.method, req.path, req.body, true)
			if out.Code != tc.code || !strings.Contains(out.Body.String(), tc.err.Error()) {
				t.Fatalf("%s %s with %v: %d %s", req.method, req.path, tc.err, out.Code, out.Body.String())
			}
		}
	}

	svc.err = nil
	if out = send(t, r, http.MethodPost, "/api/v1/projects/p/rollbacks", `{"environment":"production","reason":"errors"}`, false); out.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d %s", out.Code, out.Body.String())
	}
	if out = send(t, r, http.MethodGet, "/api/v1/projects/p/environments/production/rollback-target", "", false); out.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d %s", out.Code, out.Body.String())
	}
	if out = send(t, r, http.MethodPost, "/api/v1/projects/p/rollbacks", `nope`, true); out.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON: %d %s", out.Code, out.Body.String())
	}
}

// The real service sits behind the release routes.
var _ releaseService = (*planning.Service)(nil)
