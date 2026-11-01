//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/config"
	apiclient "github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/provider"
)

// TestManagedLifecyclePublicAPI keeps rollout off while exercising persisted public views.
func TestManagedLifecyclePublicAPI(t *testing.T) {
	hub := httptest.NewServer(http.NotFoundHandler())
	defer hub.Close()
	a := capacityAPISetup(t, func(c *config.Config) {
		c.Push.Enabled = false
		c.Clouds.ManagedClusters.Enabled = false
		c.AI.Enabled = true
		c.AI.Models.HuggingFaceURL = hub.URL
	})
	manualCluster := lifecycleManualFixture(t, a)
	accessAttachment := lifecycleAccessFixture(t, a)
	cleanupDeployment := lifecycleCleanupFixture(t, a, accessAttachment)
	now := time.Now().UTC()
	id := uuid.NewString()
	r := &cloudinfra.ManagedResource{ID: id, OrgID: a.org, CloudID: a.cloud, Kind: "cluster", Provider: "digitalocean", AccountID: "fixture-account", ConnectionID: "capacity-do", ExternalID: "fixture-cluster", Incarnation: "fixture-incarnation", Ownership: "managed", Revision: 1, SchemaVersion: 1, CreatedAt: now, UpdatedAt: now, Status: "running", Observation: &provider.ClusterObservation{Ref: provider.ClusterRef{ID: "fixture-cluster", AccountID: "fixture-account", Incarnation: "fixture-incarnation"}, State: "running", ObservedAt: now}}
	if _, err := a.store.ClaimManagedResource(t.Context(), r, 0); err != nil {
		t.Fatal(err)
	}
	op := &cloudinfra.ClusterOperation{ID: uuid.NewString(), OrgID: a.org, CloudID: a.cloud, ResourceID: id, Kind: "node-pool-update", SchemaVersion: 1, Revision: 1, Status: "preview", Digest: "fixture-original-review", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour), MoneyEstimate: &cloudinfra.MoneyEstimate{HourlyMicroUSD: 9007199254740993, HorizonMicroUSD: 9007199254740993, Known: false, Components: []cloudinfra.MoneyComponent{{Name: "network", Category: "network_egress", Identity: "private-capture", Source: "provider", Quantity: 1}}}}
	if err := a.store.CreateClusterPreview(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	var got apiclient.CloudClusterOperation
	a.c.must(http.StatusOK, http.MethodGet, a.path("/cluster-operations/"+op.ID), nil, &got)
	if got.Digest != op.Digest || got.MoneyEstimate.HourlyMicroUSD != "9007199254740993" {
		t.Fatalf("lost exact review: %+v", got)
	}
	var invitation struct {
		AcceptPath string `json:"accept_path"`
	}
	a.c.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations", map[string]string{"email": "lifecycle-viewer@example.test", "role": "viewer"}, &invitation)
	accept, err := url.Parse(invitation.AcceptPath)
	if err != nil {
		t.Fatal(err)
	}
	viewer := &client{t: t, base: a.c.base, key: a.c.key}
	account(t, viewer, "lifecycle-viewer@example.test")
	code := viewer.do(http.MethodPost, "/authsome/v1/orgs/invitations/accept", map[string]string{"token": accept.Query().Get("token")}, nil)
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("accept: %d", code)
	}
	viewer.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": a.org}, nil)
	viewer.must(http.StatusForbidden, http.MethodGet, a.path("/clusters/"+id+"/capacity"), nil, nil)
	viewer.must(http.StatusForbidden, http.MethodPost, a.path("/clusters/"+id+"/capacity/refresh"), nil, nil)
	viewer.must(http.StatusForbidden, http.MethodPost, a.path("/clusters/"+id+"/retirement/preview"), nil, nil)
	if cleanupDeployment != "" {
		viewer.must(http.StatusForbidden, http.MethodPost, "/api/v1/deployments/"+cleanupDeployment+"/cleanup/preview", nil, nil)
		if os.Getenv("LOOMPLANE_CLEANUP_KEEP_UNRESOLVED") != "1" {
			var cleanup apiclient.FailedCleanup
			a.c.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+cleanupDeployment+"/cleanup", nil, &cleanup)
			viewer.must(http.StatusForbidden, http.MethodPost, "/api/v1/deployments/"+cleanupDeployment+"/cleanup/cancel", map[string]string{"expected_review_id": cleanup.ReviewID}, nil)
		}
	}
	viewer.must(http.StatusForbidden, http.MethodPost, a.path("/clusters/preview"), map[string]string{"kind": "runtime-access-upgrade"}, nil)

	// Reload reads the same saved review, never a replacement execution payload.
	a.c.must(http.StatusOK, http.MethodGet, a.path("/cluster-operations/"+op.ID), nil, &got)
	if got.Digest != op.Digest {
		t.Fatal("reload changed review")
	}
	if state := os.Getenv("LOOMPLANE_LIFECYCLE_BROWSER_STATE"); state != "" {
		if err = os.MkdirAll(filepath.Dir(state), 0700); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]string{"api_url": a.r.srv.URL, "manual_cluster_id": manualCluster, "access_attachment_id": accessAttachment, "cleanup_deployment_id": cleanupDeployment, "org_id": a.org, "cloud_id": a.cloud, "project_id": a.project, "cluster_id": id, "operation_id": op.ID, "owner_email": "clouds@example.test", "viewer_email": "lifecycle-viewer@example.test"})
		if err = os.WriteFile(state, raw, 0600); err != nil {
			t.Fatal(err)
		}
		browser := startInfrastructureBrowser(t, a.r.srv.URL, state)
		browser.checkpoint(t, "lifecycle", map[string]string{"org_id": a.org, "cloud_id": a.cloud, "project_id": a.project, "cluster_id": id, "operation_id": op.ID})
	}
}
