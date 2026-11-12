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

type cancellationFixture struct {
	routeFixture
	operationErr error
	cancelErr    error
	cancelCalls  int
	revision     int64
}

func (s *cancellationFixture) GetFleetOperation(_ context.Context, p security.Principal, fleetID, operationID string) (*fleet.Operation, error) {
	if s.operationErr != nil {
		return nil, s.operationErr
	}
	if p.OrgID != "org" || fleetID != "fleet" || operationID != "operation" {
		return nil, domain.ErrNotFound
	}
	return &fleet.Operation{ID: operationID, InstanceID: "instance", Kind: "create", Status: "waiting_approval", Generation: 1}, nil
}

func (s *cancellationFixture) CancelOperation(_ context.Context, p security.Principal, id string, revision int64) (*fleet.Operation, error) {
	s.cancelCalls++
	s.revision = revision
	if p.OrgID != "org" || id != "operation" {
		return nil, domain.ErrNotFound
	}
	if s.cancelErr != nil {
		return nil, s.cancelErr
	}
	return &fleet.Operation{ID: id, InstanceID: "instance", Kind: "create", Status: "cancelled", Generation: 1}, nil
}

func TestFleetCancelRouteScopeRevisionAndErrors(t *testing.T) {
	for _, tt := range []struct {
		name         string
		body         string
		operationErr error
		cancelErr    error
		status       int
		calls        int
	}{
		{name: "cancelled", body: `{"expected_instance_revision":7}`, status: 200, calls: 1},
		{name: "stale revision or admitted", body: `{"expected_instance_revision":7}`, cancelErr: domain.ErrConflict, status: 409, calls: 1},
		{name: "denied", body: `{"expected_instance_revision":7}`, cancelErr: security.ErrForbidden, status: 403, calls: 1},
		{name: "hidden operation", body: `{"expected_instance_revision":7}`, operationErr: domain.ErrNotFound, status: 404},
		{name: "redacted failure", body: `{"expected_instance_revision":7}`, cancelErr: errors.New("private-provider-token"), status: 500, calls: 1},
		{name: "missing revision", body: `{}`, status: 422},
		{name: "zero revision", body: `{"expected_instance_revision":0}`, status: 422},
		{name: "negative revision", body: `{"expected_instance_revision":-1}`, status: 422},
		{name: "unknown authority field", body: `{"expected_instance_revision":7,"org_id":"other"}`, status: 422},
		{name: "multiple documents", body: `{"expected_instance_revision":7} {}`, status: 422},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := &cancellationFixture{operationErr: tt.operationErr, cancelErr: tt.cancelErr}
			router := forge.NewRouter()
			if err := (&Extension{svc: svc}).routes(router, nil); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/projects/project/fleet/operations/operation/cancel", strings.NewReader(tt.body))
			req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "operator"}))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tt.status || svc.cancelCalls != tt.calls {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, svc.cancelCalls, rec.Body.String())
			}
			if tt.calls > 0 && svc.revision != 7 {
				t.Fatalf("revision=%d", svc.revision)
			}
			if strings.Contains(rec.Body.String(), "private-provider-token") {
				t.Fatal("internal failure disclosed")
			}
			if tt.status == 200 && !strings.Contains(rec.Body.String(), `"status":"cancelled"`) {
				t.Fatalf("missing cancellation receipt: %s", rec.Body.String())
			}
		})
	}
}

func TestFleetCancelHidesSiblingInstance(t *testing.T) {
	svc := &cancellationFixture{routeFixture: routeFixture{instanceFleet: "other"}}
	r := forge.NewRouter()
	if err := (&Extension{svc: svc}).routes(r, nil); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/projects/project/fleet/operations/operation/cancel", strings.NewReader(`{"expected_instance_revision":7}`))
	req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "operator"}))
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	if out.Code != 404 || svc.cancelCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", out.Code, svc.cancelCalls, out.Body.String())
	}
}
