package cloudinfra

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/security"
)

// adoptionListStore holds one waiting and one flagged adoption into cloud,
// and one into another Cloud.
type adoptionListStore struct {
	infra.Store
	asked *[2]string
}

func (s adoptionListStore) ListNodeAdoptions(_ context.Context, org, cloud string) ([]*infra.NodeAdoption, error) {
	*s.asked = [2]string{org, cloud}
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	return []*infra.NodeAdoption{
		{NodeID: "node_2", NodeName: "web-2", OrgID: org, CloudID: cloud, ApprovedBy: "user_admin", ApprovedByKeyID: "key_secret", Status: infra.NodeAdoptionReview, Reason: "ports 80/443 are in use", CreatedAt: now, UpdatedAt: now},
		{NodeID: "node_1", NodeName: "web-1", OrgID: org, CloudID: cloud, ApprovedBy: "user_admin", Status: infra.NodeAdoptionApplied, ServerID: "server_1", OperationID: "infraop_1", CreatedAt: now, UpdatedAt: now},
	}, nil
}
func (adoptionListStore) CreateNodeAdoption(context.Context, *infra.NodeAdoption) error { return nil }
func (adoptionListStore) GetNodeAdoption(context.Context, string, string) (*infra.NodeAdoption, error) {
	return nil, infra.ErrNotFound
}
func (adoptionListStore) SaveNodeAdoption(context.Context, *infra.NodeAdoption) error { return nil }

func TestCloudNodeAdoptionsList(t *testing.T) {
	get := func(allow bool) (*httptest.ResponseRecorder, [2]string) {
		var asked [2]string
		svc := infra.NewService(infra.Deps{Authz: overviewAuthz{allow: allow}, Clouds: clusterCloudStore{}, Store: adoptionListStore{asked: &asked}})
		r := forge.NewRouter()
		if err := (&Extension{svc: svc}).routes(r, nil); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/clouds/cloud_1/node-adoptions", http.NoBody)
		req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "member"}))
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out, asked
	}
	out, asked := get(true)
	if out.Code != http.StatusOK {
		t.Fatalf("status %d: %s", out.Code, out.Body)
	}
	if asked != [2]string{"org", "cloud_1"} {
		t.Fatalf("listed %v, want the caller's org and the route's Cloud", asked)
	}
	if strings.Contains(out.Body.String(), "key_secret") || strings.Contains(out.Body.String(), "approved_by_key_id") {
		t.Fatalf("the approving key leaked: %s", out.Body)
	}
	var body struct {
		Adoptions []map[string]any `json:"adoptions"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Adoptions) != 2 {
		t.Fatalf("adoptions %v", body.Adoptions)
	}
	flagged := body.Adoptions[0]
	for key, want := range map[string]string{"node_id": "node_2", "node_name": "web-2", "status": "review", "reason": "ports 80/443 are in use", "approved_by": "user_admin"} {
		if flagged[key] != want {
			t.Errorf("%s = %v, want %q", key, flagged[key], want)
		}
	}
	for _, key := range []string{"server_id", "operation_id", "created_at", "updated_at"} {
		if _, ok := flagged[key]; !ok {
			t.Errorf("missing %s", key)
		}
	}
	if applied := body.Adoptions[1]; applied["server_id"] != "server_1" || applied["operation_id"] != "infraop_1" {
		t.Errorf("applied %v", applied)
	}
	// Reading them takes the same role as reading the Cloud's servers.
	if denied, _ := get(false); denied.Code != http.StatusForbidden {
		t.Fatalf("without the role: %d", denied.Code)
	}
}
