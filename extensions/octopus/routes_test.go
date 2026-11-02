package octopus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/octopusloom"
	"github.com/xraph/loomplane/internal/security"
)

// fakeService records what each route asks of the Octopus service and
// answers err when it's set.
type fakeService struct {
	calls []string
	err   error
}

func (f *fakeService) record(op string, p security.Principal, ref octopusloom.GatewayRef, extra ...any) error {
	f.calls = append(f.calls, strings.TrimSpace(fmt.Sprintf("%s %s %s %s/%s/%s %s", op, p.OrgID, p.UserID, ref.ProjectID, ref.EnvironmentID, ref.Resource, fmt.Sprint(extra...))))
	return f.err
}

func (f *fakeService) Board(_ context.Context, p security.Principal, ref octopusloom.GatewayRef) (*octopusloom.Board, error) {
	if err := f.record("board", p, ref); err != nil {
		return nil, err
	}
	return &octopusloom.Board{Hosts: []octopusloom.HostCard{}, Live: true}, nil
}

func (f *fakeService) Status(_ context.Context, p security.Principal, ref octopusloom.GatewayRef) (*octopusloom.OctopusStatus, error) {
	if err := f.record("status", p, ref); err != nil {
		return nil, err
	}
	return &octopusloom.OctopusStatus{Resource: ref.Resource}, nil
}

func (f *fakeService) Discovery(_ context.Context, p security.Principal, ref octopusloom.GatewayRef) (*octopusloom.DiscoverySummary, error) {
	if err := f.record("discovery", p, ref); err != nil {
		return nil, err
	}
	return &octopusloom.DiscoverySummary{Mode: "push", Services: []octopusloom.DiscoveredService{}}, nil
}

func (f *fakeService) CreateRoute(_ context.Context, p security.Principal, ref octopusloom.GatewayRef, in octopusloom.RouteInput) (*octopusloom.RouteResult, error) {
	if err := f.record("create", p, ref, in.Path); err != nil {
		return nil, err
	}
	return &octopusloom.RouteResult{Route: &octopusloom.Route{ID: "octoroute_1", Path: in.Path}, Sync: octopusloom.SyncResult{State: "pending", Message: "later"}}, nil
}

func (f *fakeService) UpdateRoute(_ context.Context, p security.Principal, ref octopusloom.GatewayRef, id string, in octopusloom.RouteInput) (*octopusloom.RouteResult, error) {
	if err := f.record("update", p, ref, id, " ", in.Revision); err != nil {
		return nil, err
	}
	return &octopusloom.RouteResult{Route: &octopusloom.Route{ID: id, Path: in.Path}, Sync: octopusloom.SyncResult{State: "applied"}}, nil
}

func (f *fakeService) RemoveRoute(_ context.Context, p security.Principal, ref octopusloom.GatewayRef, id string, revision int64) (*octopusloom.SyncResult, error) {
	if err := f.record("remove", p, ref, id, " ", revision); err != nil {
		return nil, err
	}
	return &octopusloom.SyncResult{State: "applied"}, nil
}

func (f *fakeService) SetFarpHostnames(_ context.Context, p security.Principal, ref octopusloom.GatewayRef, hostnames []string, revision int64) (*octopusloom.OctopusStatus, error) {
	if err := f.record("farp", p, ref, strings.Join(hostnames, ","), " ", revision); err != nil {
		return nil, err
	}
	return &octopusloom.OctopusStatus{FarpHostnames: hostnames}, nil
}

func (f *fakeService) Sync(_ context.Context, p security.Principal, ref octopusloom.GatewayRef) (*octopusloom.SyncResult, error) {
	if err := f.record("sync", p, ref); err != nil {
		return nil, err
	}
	return &octopusloom.SyncResult{State: "applied"}, nil
}

const base = "/api/v1/projects/proj_1/environments/env_1/octopus/edge"

type call struct{ method, path, body string }

var calls = []call{
	{http.MethodGet, base + "/board", ""},
	{http.MethodGet, base + "/status", ""},
	{http.MethodGet, base + "/discovery", ""},
	{http.MethodPost, base + "/routes", `{"path":"/auth/*","service":"auth","port":9000}`},
	{http.MethodPost, base + "/routes/octoroute_1/update", `{"path":"/auth/*","service":"auth","port":9000,"revision":2}`},
	{http.MethodPost, base + "/routes/octoroute_1/remove", `{"revision":3}`},
	{http.MethodPost, base + "/farp-hostnames", `{"hostnames":["api.acme.dev"],"revision":4}`},
	{http.MethodPost, base + "/sync", ""},
}

func router(t *testing.T, guard identity.Guard, svc service) forge.Router {
	t.Helper()
	r := forge.NewRouter()
	if err := mount(r, guard, svc); err != nil {
		t.Fatal(err)
	}
	return r
}

// do makes a request as user, or with no principal when user is "".
func do(t *testing.T, r forge.Router, c call, user string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), c.method, c.path, strings.NewReader(c.body))
	if c.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org_1", UserID: user}))
	}
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	return out
}

func TestOctopusRoutesAreRegistered(t *testing.T) {
	guarded := 0
	guard := identity.Guard{func(forge.Handler) forge.Handler {
		return func(forge.Context) error {
			guarded++
			return forge.NewHTTPError(http.StatusTeapot, "the guard")
		}
	}}
	svc := &fakeService{}
	r := router(t, guard, svc)
	const p = "/api/v1/projects/:projectId/environments/:environmentId/octopus/:resource"
	want := map[string]string{
		"octopus.board":            "GET " + p + "/board",
		"octopus.status":           "GET " + p + "/status",
		"octopus.discovery":        "GET " + p + "/discovery",
		"octopus.createRoute":      "POST " + p + "/routes",
		"octopus.updateRoute":      "POST " + p + "/routes/:routeId/update",
		"octopus.removeRoute":      "POST " + p + "/routes/:routeId/remove",
		"octopus.setFarpHostnames": "POST " + p + "/farp-hostnames",
		"octopus.sync":             "POST " + p + "/sync",
	}
	got := map[string]string{}
	for _, info := range r.Routes() {
		got[info.Name] = info.Method + " " + info.Path
		if !slices.Contains(info.Tags, "Octopus") {
			t.Fatalf("%s has tags %v", info.Name, info.Tags)
		}
	}
	for name, route := range want {
		if got[name] != route {
			t.Fatalf("%s is %q, want %q (routes %v)", name, got[name], route, got)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("routes %v", got)
	}
	for _, c := range calls {
		if out := do(t, r, c, "usr_1"); out.Code != http.StatusTeapot {
			t.Fatalf("%s %s: %d past the guard", c.method, c.path, out.Code)
		}
	}
	if guarded != len(calls) || len(svc.calls) != 0 {
		t.Fatalf("guarded %d, service calls %v", guarded, svc.calls)
	}
}

func TestOctopusRoutesCallTheService(t *testing.T) {
	svc := &fakeService{}
	r := router(t, nil, svc)
	wantCodes := []int{200, 200, 200, http.StatusCreated, 200, 200, 200, 200}
	for i, c := range calls {
		out := do(t, r, c, "usr_1")
		if out.Code != wantCodes[i] {
			t.Fatalf("%s %s: %d %s", c.method, c.path, out.Code, out.Body.String())
		}
		if !json.Valid(out.Body.Bytes()) {
			t.Fatalf("%s %s answered %q", c.method, c.path, out.Body.String())
		}
	}
	want := []string{
		"board org_1 usr_1 proj_1/env_1/edge",
		"status org_1 usr_1 proj_1/env_1/edge",
		"discovery org_1 usr_1 proj_1/env_1/edge",
		"create org_1 usr_1 proj_1/env_1/edge /auth/*",
		"update org_1 usr_1 proj_1/env_1/edge octoroute_1 2",
		"remove org_1 usr_1 proj_1/env_1/edge octoroute_1 3",
		"farp org_1 usr_1 proj_1/env_1/edge api.acme.dev 4",
		"sync org_1 usr_1 proj_1/env_1/edge",
	}
	if !slices.Equal(svc.calls, want) {
		t.Fatalf("calls\n%v\nwant\n%v", strings.Join(svc.calls, "\n"), strings.Join(want, "\n"))
	}
	var created struct {
		Route struct {
			ID string `json:"id"`
		} `json:"route"`
		Sync struct {
			State   string `json:"state"`
			Message string `json:"message"`
		} `json:"sync"`
	}
	out := do(t, r, calls[3], "usr_1")
	if err := json.Unmarshal(out.Body.Bytes(), &created); err != nil || created.Route.ID != "octoroute_1" || created.Sync.State != "pending" || created.Sync.Message != "later" {
		t.Fatalf("created %s: %v", out.Body.String(), err)
	}
}

func TestOctopusRouteErrors(t *testing.T) {
	svc := &fakeService{}
	r := router(t, nil, svc)
	for _, c := range calls {
		if out := do(t, r, c, ""); out.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s with no principal: %d", c.method, c.path, out.Code)
		}
	}
	if len(svc.calls) != 0 {
		t.Fatalf("calls %v", svc.calls)
	}
	for _, c := range []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w (infrastructure:modify)", security.ErrForbidden), http.StatusForbidden},
		{octopusloom.ErrNotFound, http.StatusNotFound},
		{octopusloom.ErrConflict, http.StatusConflict},
		{fmt.Errorf("update route: %w", octopusloom.ErrStale), http.StatusConflict},
		{octopusloom.ErrInvalid, http.StatusUnprocessableEntity},
	} {
		svc.err = c.err
		for _, call := range calls {
			if out := do(t, r, call, "usr_1"); out.Code != c.want {
				t.Fatalf("%s %s on %v: %d, want %d", call.method, call.path, c.err, out.Code, c.want)
			}
		}
	}
}

func TestOctopusRoutesSayWhatWasInvalid(t *testing.T) {
	svc := &fakeService{}
	r := router(t, nil, svc)
	if _, err := octopusloom.NormalizeRouteInput(octopusloom.RouteInput{Path: "auth", Service: "auth", Port: 9000}); err == nil {
		t.Fatal("NormalizeRouteInput accepted auth")
	} else {
		svc.err = err
	}
	out := do(t, r, calls[3], "usr_1")
	if out.Code != http.StatusUnprocessableEntity || !strings.Contains(out.Body.String(), "A path starts with /") {
		t.Fatalf("%d %s", out.Code, out.Body.String())
	}
	svc.err, svc.calls = nil, nil
	for _, c := range []call{calls[3], calls[4], calls[5], calls[6]} {
		c.body = "{"
		if out := do(t, r, c, "usr_1"); out.Code != http.StatusBadRequest {
			t.Fatalf("%s with a bad body: %d", c.path, out.Code)
		}
	}
	if len(svc.calls) != 0 {
		t.Fatalf("calls %v", svc.calls)
	}
	// An error the service can't explain stays a 500, without its text.
	svc.err = errors.New("the database answered: password=hunter2")
	if out := do(t, r, calls[0], "usr_1"); out.Code != http.StatusInternalServerError || strings.Contains(out.Body.String(), "hunter2") {
		t.Fatalf("%d %s", out.Code, out.Body.String())
	}
}
