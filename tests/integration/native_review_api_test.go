//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/recovery"
)

func checkNativeCopyReviewAPI(t *testing.T, c *client, dsn string, source recovery.NativeSource, point, google string, mode func(string, bool)) {
	t.Helper()
	mode("copy-review", false)
	defer mode("original", false)
	var targetAccount connections.Connection
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]string{"cloud": "gcp", "name": "Recovery project", "credential": string(nativeSQLAccountKey(t, "recovery-project", google+"/token"))}, &targetAccount)
	var cloud cloudprofiles.Profile
	cloudBase := "/api/v1/projects/" + source.ProjectID + "/clouds"
	c.must(http.StatusCreated, http.MethodPost, cloudBase, map[string]string{"name": "Native recovery"}, &cloud)
	var target cloudprofiles.Target
	c.must(http.StatusCreated, http.MethodPost, cloudBase+"/"+cloud.ID+"/targets", cloudprofiles.CreateTargetInput{Name: "recovery", Runtime: "managed", Provider: "cloud-sql", ConnectionID: targetAccount.ID, Region: "europe-west1"}, &target)
	var inventory recovery.NativeInventory
	c.must(http.StatusOK, http.MethodPost, "/api/v1/native-recovery-sources/"+source.ID+"/refresh", nil, &inventory)
	var review recovery.NativeCopyReview
	path := "/api/v1/native-recovery-sources/" + source.ID + "/points/" + point + "/copy-review"
	reviewInput := recovery.NativeCopyReviewInput{RequestID: "public-native-copy", CloudID: cloud.ID, TargetID: target.ID, TTLHours: 1}
	c.must(http.StatusOK, http.MethodPost, path, reviewInput, &review)
	if review.Target.NativeProject != "recovery-project" || review.Capacity.StorageGB != 10 || review.Target.GrantRevision < 1 {
		t.Fatal("public review lost granted scope and bounded capacity", review)
	}
	input := recovery.NativeCopyCreateInput{RequestID: "public-native-copy", ReviewToken: review.ReviewToken}
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/native-recovery-copies", input, nil)
	input.AcknowledgeCharges = true
	var readKey keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "Native copy reader", "access": "read"}, &readKey)
	reader := &client{t: t, base: c.base, key: c.key, token: readKey.Secret}
	reader.must(http.StatusForbidden, http.MethodPost, path, reviewInput, nil)
	reader.must(http.StatusForbidden, http.MethodPost, "/api/v1/native-recovery-copies", input, nil)
	var restored recovery.NativeRestore
	c.must(http.StatusAccepted, http.MethodPost, "/api/v1/native-recovery-copies", input, &restored)
	copyPath := "/api/v1/native-recovery-copies/" + restored.ID
	wait := func(status string) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			c.must(http.StatusOK, http.MethodGet, copyPath, nil, &restored)
			if restored.Status == status {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("public copy never reached %s: %s", status, restored.Status)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	wait("failed")
	reader.must(http.StatusOK, http.MethodGet, copyPath, nil, nil)
	reader.must(http.StatusForbidden, http.MethodDelete, copyPath, nil, nil)
	var public map[string]any
	c.must(http.StatusOK, http.MethodGet, copyPath, nil, &public)
	for _, private := range []string{"source", "point", "checkpoint", "token", "request_id"} {
		if _, exists := public[private]; exists {
			t.Fatal("public status exposed internal worker input", private)
		}
	}
	if restored.VerifiedAt != nil || restored.PhysicalID != "" || restored.ExpiresAt.Sub(restored.CreatedAt) != time.Hour {
		t.Fatal("denied native target was called verified or changed lifetime")
	}
	db, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(t.Context())
	secretKey := deployments.ResourceKey(source.OrgID, source.EnvironmentID, "native-copy:"+restored.ID, "credentials")
	secretExists := func() bool {
		t.Helper()
		var exists bool
		if err := db.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM vault_secrets WHERE app_id='loomplane-resource-credentials' AND key=$1)`, secretKey).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		return exists
	}
	if !secretExists() {
		t.Fatal("production native worker did not retain its fenced Vault credential")
	}
	// Revocation blocks new placement, while current recovery authority still
	// permits cleanup of this retained destination.
	c.must(http.StatusOK, http.MethodDelete, "/api/v1/clouds/"+cloud.ID+"/projects/"+source.ProjectID, nil, nil)
	c.must(http.StatusAccepted, http.MethodDelete, copyPath, nil, &restored)
	wait("cleanup_failed")
	if !secretExists() {
		t.Fatal("failed absence observation discarded copy credentials")
	}
	mode("copy-review", true)
	c.must(http.StatusAccepted, http.MethodDelete, copyPath, nil, &restored)
	wait("deleted")
	if secretExists() {
		t.Fatal("confirmed absence retained native copy credential")
	}
	var copies recovery.NativeCopiesPage
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+source.ProjectID+"/native-recovery-copies?environment_id="+source.EnvironmentID+"&limit=1", nil, &copies)
	if len(copies.Copies) != 1 || copies.Copies[0].ID != restored.ID || copies.Copies[0].Status != "deleted" {
		t.Fatal("public history lost cleanup result")
	}
	var contract json.RawMessage
	c.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &contract)
	var schema struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name string `json:"name"`
				In   string `json:"in"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(contract, &schema); err != nil {
		t.Fatal(err)
	}
	query := map[string]bool{}
	for _, parameter := range schema.Paths["/api/v1/projects/{id}/native-recovery-copies"]["get"].Parameters {
		if parameter.In == "query" {
			query[parameter.Name] = true
		}
	}
	for _, name := range []string{"environment_id", "before", "limit"} {
		if !query[name] {
			t.Fatalf("native copy list contract omits query parameter %s", name)
		}
	}
	if output := os.Getenv("LOOMPLANE_TEST_OPENAPI_OUTPUT"); output != "" {
		if err := os.WriteFile(output, contract, 0600); err != nil {
			t.Fatal(err)
		}
	}
	mode("original", false)
	c.must(http.StatusOK, http.MethodPost, "/api/v1/native-recovery-sources/"+source.ID+"/refresh", nil, &inventory)
}
