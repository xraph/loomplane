package fleets

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/cloudinfra"
	domain "github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type actionRouteFixture struct {
	routeFixture
	input  fleet.ActionInput
	called bool
	err    error
}

func (s *actionRouteFixture) RequestAction(_ context.Context, p security.Principal, id string, in fleet.ActionInput) (*fleet.Operation, error) {
	s.called = true
	s.input = in
	if p.OrgID != "org" || id != "instance" {
		return nil, domain.ErrNotFound
	}
	return &fleet.Operation{ID: "operation", InstanceID: id, Kind: in.Kind, Status: "waiting_approval", Generation: in.ExpectedGeneration + 1}, s.err
}
func (s *actionRouteFixture) GetActiveOperation(_ context.Context, p security.Principal, id string) (*fleet.Operation, error) {
	s.called = true
	if p.OrgID != "org" || id != "instance" {
		return nil, domain.ErrNotFound
	}
	return &fleet.Operation{ID: "operation", InstanceID: id, Kind: "create", Status: "waiting_approval", Generation: 1}, s.err
}
func TestFleetActionRoute(t *testing.T) {
	const valid = `{"kind":"suspend","idempotency_key":"stable-request","expected_revision":7,"expected_generation":3,"suspend":{}}`
	for _, tt := range []struct {
		name, body string
		fixture    actionRouteFixture
		status     int
		called     bool
	}{
		{name: "accepted intent", body: valid, status: 202, called: true},
		{name: "review blocker", body: valid, fixture: actionRouteFixture{err: domain.ErrApprovalRequired}, status: 422, called: true},
		{name: "conflict", body: valid, fixture: actionRouteFixture{err: domain.ErrConflict}, status: 409, called: true},
		{name: "denied", body: valid, fixture: actionRouteFixture{err: security.ErrForbidden}, status: 403, called: true},
		{name: "limit", body: valid, fixture: actionRouteFixture{err: domain.ErrCapacity}, status: 429, called: true},
		{name: "unknown error", body: valid, fixture: actionRouteFixture{err: errors.New("private-provider-token")}, status: 500, called: true},
		{name: "sibling scope", body: valid, fixture: actionRouteFixture{routeFixture: routeFixture{instanceFleet: "sibling"}}, status: 404},
		{name: "no revision", body: `{"kind":"suspend","idempotency_key":"stable-request","expected_generation":3,"suspend":{}}`, status: 422},
		{name: "multiple payloads", body: `{"kind":"suspend","idempotency_key":"stable-request","expected_revision":7,"expected_generation":3,"suspend":{},"resume":{}}`, status: 422},
		{name: "unknown authority", body: `{"org_id":"other"}`, status: 422},
		{name: "trailing document", body: valid + ` {}`, status: 422},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := forge.NewRouter()
			if err := (&Extension{svc: &tt.fixture}).routes(r, nil); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/projects/project/fleet/instances/instance/operations", strings.NewReader(tt.body))
			req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "operator"}))
			out := httptest.NewRecorder()
			r.ServeHTTP(out, req)
			if out.Code != tt.status || tt.fixture.called != tt.called {
				t.Fatalf("status=%d called=%v body=%s", out.Code, tt.fixture.called, out.Body.String())
			}
			if strings.Contains(out.Body.String(), "private-provider-token") {
				t.Fatal("internal failure disclosed")
			}
			if tt.status == 202 && (!strings.Contains(out.Body.String(), `"status":"waiting_approval"`) || tt.fixture.input.IdempotencyKey != "stable-request" || tt.fixture.input.ExpectedRevision != 7 || tt.fixture.input.ExpectedGeneration != 3) {
				t.Fatal("request pins or acceptance state changed")
			}
		})
	}
}
func TestFleetActiveOperationRoute(t *testing.T) {
	for _, tt := range []struct {
		name    string
		fixture actionRouteFixture
		status  int
		called  bool
	}{
		{name: "active", status: 200, called: true},
		{name: "none", fixture: actionRouteFixture{err: domain.ErrNotFound}, status: 404, called: true},
		{name: "denied", fixture: actionRouteFixture{err: security.ErrForbidden}, status: 403, called: true},
		{name: "sibling", fixture: actionRouteFixture{routeFixture: routeFixture{instanceFleet: "other"}}, status: 404},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := forge.NewRouter()
			if err := (&Extension{svc: &tt.fixture}).routes(r, nil); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/projects/project/fleet/instances/instance/active-operation", nil)
			req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "operator"}))
			out := httptest.NewRecorder()
			r.ServeHTTP(out, req)
			if out.Code != tt.status || tt.fixture.called != tt.called {
				t.Fatalf("status=%d called=%v body=%s", out.Code, tt.fixture.called, out.Body.String())
			}
		})
	}
}

func TestFleetActionSchemasKeepOrdinaryMove(t *testing.T) {
	r := forge.NewRouter(forge.WithOpenAPI(forge.OpenAPIConfig{Title: "Actions", Version: "1", SpecEnabled: true}))
	if err := r.GET("/ordinary-move", func(c forge.Context) error { return c.JSON(http.StatusOK, cloudinfra.MoveInput{}) }, forge.WithResponseSchema(http.StatusOK, "Ordinary move", cloudinfra.MoveInput{})); err != nil {
		t.Fatal(err)
	}
	if err := (&Extension{svc: &actionRouteFixture{}}).routes(r, nil); err != nil {
		t.Fatal(err)
	}
	schemas := r.OpenAPISpec().Components.Schemas
	for _, path := range []string{
		"/api/v1/projects/{project_id}/fleet/instances/{id}/operations",
		"/api/v1/projects/{project_id}/fleet/operations/{id}/cancel",
	} {
		body := r.OpenAPISpec().Paths[path].Post.RequestBody.Content["application/json"].Schema
		if body.Ref == "" {
			t.Fatalf("%s request body needs a named schema for generated field codecs", path)
		}
	}
	if s := schemas["MoveInput"]; s == nil || s.Properties["destination_server_id"] == nil {
		t.Error("ordinary MoveInput contract changed")
	}
	if s := schemas["ActionInputBody"]; s == nil || s.Properties["move"] == nil || s.Properties["move"].Properties["target_cloud_id"] == nil {
		t.Error("fleet action body must retain its move fields")
	}
	if s := schemas["FleetCancelOperationInputBody"]; s == nil || s.Properties["expected_instance_revision"] == nil {
		t.Error("cancellation body must retain its expected revision")
	}
}
