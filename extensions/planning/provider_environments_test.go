package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// fakeProviderEnvironments holds provider environment records the way the
// store does: ResetFallbacks deletes the retryable fallbacks and counts the
// permanent ones.
type fakeProviderEnvironments struct {
	rows []*deployments.ProviderEnvironment
}

func (f *fakeProviderEnvironments) ProviderEnvironments(_ context.Context, orgID, projectID string) ([]*deployments.ProviderEnvironment, error) {
	var out []*deployments.ProviderEnvironment
	for _, pe := range f.rows {
		if pe.OrgID == orgID && pe.ProjectID == projectID {
			out = append(out, pe)
		}
	}
	return out, nil
}

func (f *fakeProviderEnvironments) ResetFallbacks(_ context.Context, orgID, environmentID string) (reset, permanent int, err error) {
	f.rows = slices.DeleteFunc(f.rows, func(pe *deployments.ProviderEnvironment) bool {
		mine := pe.OrgID == orgID && pe.EnvironmentID == environmentID && pe.Mode == deployments.ModeFallback
		if mine && pe.Permanent {
			permanent++
		}
		if mine && !pe.Permanent {
			reset++
		}
		return mine && !pe.Permanent
	})
	return reset, permanent, nil
}

// fakeEnvironments is one project's environments.
type fakeEnvironments map[string]*projects.Environment

func (f fakeEnvironments) GetEnvironment(_ context.Context, orgID, projectID, id string) (*projects.Environment, error) {
	e, ok := f[id]
	if !ok || e.OrgID != orgID || e.ProjectID != projectID {
		return nil, projects.ErrEnvironmentNotFound
	}
	return e, nil
}

// fakeAuthz allows what allowed holds, on any project.
type fakeAuthz struct {
	allowed []security.Permission
	asked   []string
}

func (f *fakeAuthz) Require(_ context.Context, _ security.Principal, perm security.Permission, resourceID string) error {
	f.asked = append(f.asked, perm.Name()+" "+resourceID)
	if slices.Contains(f.allowed, perm) {
		return nil
	}
	return fmt.Errorf("%w: %s", security.ErrForbidden, perm.Name())
}

type providerEnvironmentFixture struct {
	store *fakeProviderEnvironments
	authz *fakeAuthz
	plans *fakeReleases
	r     forge.Router
}

func providerEnvironmentRouter(t *testing.T, rows ...*deployments.ProviderEnvironment) *providerEnvironmentFixture {
	t.Helper()
	f := &providerEnvironmentFixture{
		store: &fakeProviderEnvironments{rows: rows},
		authz: &fakeAuthz{allowed: []security.Permission{security.EnvironmentRead, security.DeploymentCreate}},
		plans: &fakeReleases{},
		r:     forge.NewRouter(),
	}
	envs := fakeEnvironments{"env_staging": {ID: "env_staging", OrgID: "org", ProjectID: "p", Name: "staging", Kind: "staging"}}
	if err := providerEnvironmentRoutes(f.r, nil, providerEnvironmentDeps{Store: f.store, Environments: envs, Authz: f.authz, Plans: f.plans}); err != nil {
		t.Fatal(err)
	}
	return f
}

func stagingFallback(permanent bool) *deployments.ProviderEnvironment {
	return &deployments.ProviderEnvironment{
		OrgID: "org", ProjectID: "p", EnvironmentID: "env_staging", Provider: "vercel", ConnectionID: "conn", Scope: "web",
		Mode: deployments.ModeFallback, Permanent: permanent, Reason: "custom environments need a Pro plan",
		ProjectRef: "prj_secret", CreatedAt: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC),
	}
}

const retryPath = "/api/v1/projects/p/environments/env_staging/provider-environments/retry"

func TestRetryResetsAndPlans(t *testing.T) {
	f := providerEnvironmentRouter(t, stagingFallback(false))

	out := send(t, f.r, http.MethodPost, retryPath, "", true)
	if out.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", out.Code, out.Body.String())
	}
	if len(f.store.rows) != 0 {
		t.Fatalf("rows left %+v", f.store.rows)
	}
	if want := []planning.CreatePlanInput{{Environment: "staging"}}; !reflect.DeepEqual(f.plans.created, want) || !reflect.DeepEqual(f.plans.projects, []string{"p"}) {
		t.Fatalf("Create got %+v for %v", f.plans.created, f.plans.projects)
	}
	var plan planning.Plan
	if err := json.Unmarshal(out.Body.Bytes(), &plan); err != nil || plan.ID != "plan_1" || plan.Environment != "staging" {
		t.Fatalf("plan %+v: %v", plan, err)
	}
	if !slices.Contains(f.authz.asked, security.DeploymentCreate.Name()+" p") {
		t.Fatalf("asked %v", f.authz.asked)
	}
}

func TestRetryRefusesPermanentFallback(t *testing.T) {
	f := providerEnvironmentRouter(t, stagingFallback(true))

	out := send(t, f.r, http.MethodPost, retryPath, "", true)
	if out.Code != http.StatusConflict || !strings.Contains(out.Body.String(), "This environment can't use the provider's own environments, so there's nothing to try again.") {
		t.Fatalf("retry: %d %s", out.Code, out.Body.String())
	}
	if len(f.plans.created) != 0 || len(f.store.rows) != 1 {
		t.Fatalf("created %+v, rows %+v", f.plans.created, f.store.rows)
	}
}

func TestRetryWithNothingToTry(t *testing.T) {
	f := providerEnvironmentRouter(t)
	if out := send(t, f.r, http.MethodPost, retryPath, "", true); out.Code != http.StatusNotFound || len(f.plans.created) != 0 {
		t.Fatalf("no fallbacks: %d %s, created %+v", out.Code, out.Body.String(), f.plans.created)
	}
	// An environment of another project isn't reset through this one.
	other := providerEnvironmentRouter(t, stagingFallback(false))
	if out := send(t, other.r, http.MethodPost, "/api/v1/projects/q/environments/env_staging/provider-environments/retry", "", true); out.Code != http.StatusNotFound || len(other.store.rows) != 1 {
		t.Fatalf("another project: %d %s, rows %+v", out.Code, out.Body.String(), other.store.rows)
	}
}

func TestRetryNeedsDeploymentCreate(t *testing.T) {
	f := providerEnvironmentRouter(t, stagingFallback(false))
	f.authz.allowed = []security.Permission{security.EnvironmentRead}
	if out := send(t, f.r, http.MethodPost, retryPath, "", true); out.Code != http.StatusForbidden || len(f.store.rows) != 1 || len(f.plans.created) != 0 {
		t.Fatalf("forbidden: %d %s, rows %+v", out.Code, out.Body.String(), f.store.rows)
	}
	if out := send(t, f.r, http.MethodPost, retryPath, "", false); out.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d %s", out.Code, out.Body.String())
	}
}

func TestListProviderEnvironments(t *testing.T) {
	f := providerEnvironmentRouter(t, stagingFallback(false))

	out := send(t, f.r, http.MethodGet, "/api/v1/projects/p/provider-environments", "", true)
	if out.Code != http.StatusOK {
		t.Fatalf("list: %d %s", out.Code, out.Body.String())
	}
	if strings.Contains(out.Body.String(), "prj_secret") || strings.Contains(out.Body.String(), "conn") {
		t.Fatalf("the list names provider refs: %s", out.Body.String())
	}
	var list ProviderEnvironmentList
	if err := json.Unmarshal(out.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	want := ProviderEnvironmentList{ProviderEnvironments: []ProviderEnvironment{{
		EnvironmentID: "env_staging", Provider: "vercel", Scope: "web", Mode: "fallback",
		Reason: "custom environments need a Pro plan", CreatedAt: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC),
	}}}
	if !reflect.DeepEqual(list, want) {
		t.Fatalf("list %+v, want %+v", list, want)
	}
	if !slices.Equal(f.authz.asked, []string{security.EnvironmentRead.Name() + " p"}) {
		t.Fatalf("asked %v", f.authz.asked)
	}
	f.authz.allowed = nil
	if out = send(t, f.r, http.MethodGet, "/api/v1/projects/p/provider-environments", "", true); out.Code != http.StatusForbidden {
		t.Fatalf("forbidden: %d %s", out.Code, out.Body.String())
	}
}
