package fleets

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/forge"

	domain "github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type routeFixture struct {
	err           error
	instanceFleet string
}

func (s routeFixture) GetProjectFleet(_ context.Context, p security.Principal, project string) (*fleet.Fleet, error) {
	if s.err != nil {
		return nil, s.err
	}
	if p.OrgID != "org" || project != "project" {
		return nil, domain.ErrNotFound
	}
	return &fleet.Fleet{ID: "fleet", ProjectID: project, OrgID: p.OrgID}, nil
}
func (s routeFixture) GetFleetInstance(context.Context, security.Principal, string, string) (*fleet.Instance, error) {
	id := s.instanceFleet
	if id == "" {
		id = "fleet"
	}
	return &fleet.Instance{ID: "instance", FleetID: id, Status: "waiting_approval"}, nil
}
func (s routeFixture) ListInstances(context.Context, security.Principal, string, fleet.ListInstancesInput) (*fleet.InstancePage, error) {
	return &fleet.InstancePage{Items: []fleet.Instance{}}, nil
}
func (s routeFixture) GetFleetOperation(context.Context, security.Principal, string, string) (*fleet.Operation, error) {
	return &fleet.Operation{ID: "operation", InstanceID: "instance", Status: "waiting_approval"}, nil
}
func (s routeFixture) GetRelease(context.Context, security.Principal, string, string) (*fleet.TemplateRelease, error) {
	return &fleet.TemplateRelease{ID: "release", FleetID: "fleet"}, nil
}

func TestFleetReadRoutesScopeAndErrors(t *testing.T) {
	for _, tt := range []struct {
		name, path string
		service    routeFixture
		anonymous  bool
		status     int
	}{
		{name: "instance", path: "/projects/project/fleet/instances/instance", status: 200},
		{name: "operation", path: "/projects/project/fleet/operations/operation", status: 200},
		{name: "list", path: "/projects/project/fleet/instances", status: 200},
		{name: "release", path: "/projects/project/fleet/releases/release", status: 200},
		{name: "missing principal", path: "/projects/project/fleet", anonymous: true, status: 401},
		{name: "foreign project", path: "/projects/other/fleet/instances/instance", status: 404},
		{name: "sibling instance", path: "/projects/project/fleet/instances/instance", service: routeFixture{instanceFleet: "other"}, status: 404},
		{name: "sibling operation", path: "/projects/project/fleet/operations/operation", service: routeFixture{instanceFleet: "other"}, status: 404},
		{name: "denied", path: "/projects/project/fleet", service: routeFixture{err: security.ErrForbidden}, status: 403},
		{name: "invalid page", path: "/projects/project/fleet/instances?limit=no", status: 422},
		{name: "redacted failure", path: "/projects/project/fleet", service: routeFixture{err: errors.New("private-provider-token")}, status: 500},
	} {
		t.Run(tt.name, func(t *testing.T) {
			router := forge.NewRouter()
			if err := (&Extension{svc: tt.service}).routes(router, nil); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1"+tt.path, nil)
			if !tt.anonymous {
				req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "operator"}))
			}
			result := httptest.NewRecorder()
			router.ServeHTTP(result, req)
			if result.Code != tt.status {
				t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
			}
			if strings.Contains(result.Body.String(), "private-provider-token") {
				t.Fatal("internal failure disclosed")
			}
		})
	}
}
