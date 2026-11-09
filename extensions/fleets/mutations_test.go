package fleets

import (
	"context"
	"encoding/json"
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

type mutationFixture struct {
	routeFixture
	err   error
	calls int
	input fleet.CreateInstanceInput
}

func (s *mutationFixture) CreateInstance(_ context.Context, p security.Principal, id string, in fleet.CreateInstanceInput) (*fleet.OperationResult, error) {
	s.calls++
	if p.OrgID != "org" || id != "fleet" {
		return nil, domain.ErrNotFound
	}
	if s.err != nil {
		return nil, s.err
	}
	s.input = in
	return &fleet.OperationResult{Instance: fleet.Instance{ID: "instance", FleetID: id, Status: "waiting_approval"}, Operation: fleet.Operation{ID: "operation", InstanceID: "instance", Status: "waiting_approval"}}, nil
}
func TestFleetCreateAcceptanceAndErrors(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		err        error
		status     int
	}{
		{name: "accepted", body: `{"stage_id":"stage","release_id":"release","profile_id":"small","external_ref":"sample","idempotency_key":"request"}`, status: 202},
		{name: "conflict", body: `{"idempotency_key":"request"}`, err: domain.ErrConflict, status: 409},
		{name: "limit", body: `{"idempotency_key":"request"}`, err: domain.ErrCapacity, status: 429},
		{name: "blocked", body: `{"idempotency_key":"request"}`, err: domain.ErrInvalid, status: 422},
		{name: "denied", body: `{"idempotency_key":"request"}`, err: security.ErrForbidden, status: 403},
		{name: "internal", body: `{"idempotency_key":"request"}`, err: errors.New("provider-secret"), status: 500},
		{name: "invalid json", body: `{`, status: 422},
		{name: "unknown fields", body: `{"organization_id":"other"}`, status: 422},
		{name: "multiple bodies", body: `{} {}`, status: 422},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := &mutationFixture{err: tt.err}
			r := forge.NewRouter()
			if err := (&Extension{svc: svc}).routes(r, nil); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/projects/project/fleet/instances", strings.NewReader(tt.body))
			req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "operator"}))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "provider-secret") {
				t.Fatal("secret disclosed")
			}
			if tt.name == "accepted" {
				var got fleet.OperationResult
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.Operation.ID != "operation" || got.Instance.Status != "waiting_approval" || svc.input.IdempotencyKey != "request" {
					t.Fatalf("accepted=%+v input=%+v", got, svc.input)
				}
			}
			if tt.name == "unknown fields" || tt.name == "multiple bodies" || tt.name == "invalid json" {
				if svc.calls != 0 {
					t.Fatal("invalid body reached admission")
				}
			}
		})
	}
}
