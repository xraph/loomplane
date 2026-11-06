package projects

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// envStore is a projects.Store holding one project's environments. What
// the delete route doesn't reach panics through the nil interface.
type envStore struct {
	projects.Store
	envs []*projects.Environment
}

func (s *envStore) GetEnvironment(_ context.Context, _, _, id string) (*projects.Environment, error) {
	for _, e := range s.envs {
		if e.ID == id {
			return e, nil
		}
	}
	return nil, projects.ErrEnvironmentNotFound
}

func (s *envStore) DeleteEnvironment(_ context.Context, _, _, id string) error {
	s.envs = slices.DeleteFunc(s.envs, func(e *projects.Environment) bool { return e.ID == id })
	return nil
}

type allowAll struct{}

func (allowAll) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

// keepRecorder records what each environment deletion asked to keep.
type keepRecorder struct{ keep [][]string }

func (r *keepRecorder) Reap(context.Context, string, string, string) func(context.Context) {
	return nil
}

func (r *keepRecorder) ReapWithOptions(_ context.Context, _, _, _ string, opts projects.DeleteEnvironmentOptions) func(context.Context) {
	r.keep = append(r.keep, opts.Keep)
	return nil
}

func TestDeleteEnvironmentPassesKeepOn(t *testing.T) {
	store := &envStore{envs: []*projects.Environment{
		{ID: "env_1", ProjectID: "proj_1", Name: "production"},
		{ID: "env_2", ProjectID: "proj_1", Name: "staging"},
		{ID: "env_3", ProjectID: "proj_1", Name: "qa"},
	}}
	svc := projects.NewService(store, allowAll{}, nil, nil)
	reaper := &keepRecorder{}
	svc.AddReaper(reaper)
	e := &Extension{svc: svc}
	r := forge.NewRouter()
	if err := e.routes(r, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/v1/projects/proj_1/environments/env_2?keep=bnd_db&keep=bnd_cache",
		"/api/v1/projects/proj_1/environments/env_3",
	} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, path, nil)
		req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{UserID: "usr_1", OrgID: "org_1"}))
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		if out.Code != http.StatusNoContent {
			t.Fatalf("DELETE %s: %d %s", path, out.Code, out.Body.String())
		}
	}
	if len(reaper.keep) != 2 || !slices.Equal(reaper.keep[0], []string{"bnd_db", "bnd_cache"}) || len(reaper.keep[1]) != 0 {
		t.Fatalf("the deletions asked to keep %v", reaper.keep)
	}
}
