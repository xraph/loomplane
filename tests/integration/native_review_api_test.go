//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/recovery"
	lpclient "github.com/xraph/loomplane/pkg/client"
)

func checkNativeCopyReviewAPI(t *testing.T, c *client, lc loomctl, dsn string, source recovery.NativeSource, point, google string, mode func(string, bool)) {
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
	reviewFile := filepath.Join(t.TempDir(), "native-review.json")
	lc.must("backups", "native-copies", "review", source.ID, point, "--cloud", cloud.ID, "--target", target.ID, "--request-id", reviewInput.RequestID, "--out", reviewFile)
	savedReview, err := os.ReadFile(reviewFile)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(savedReview, &review); err != nil {
		t.Fatal(err)
	}
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
	if decodeErr := json.Unmarshal([]byte(lc.must("backups", "native-copies", "create", "--review-file", reviewFile, "--acknowledge-charges", "--json")), &restored); decodeErr != nil {
		t.Fatal(decodeErr)
	}
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
	sdk := lpclient.New(lc.url, lc.key)
	retained, readErr := sdk.WaitNativeRecoveryCopy(t.Context(), restored.ID)
	if readErr != nil || retained.Status != "failed" || retained.VerifiedAt != nil {
		t.Fatalf("SDK lost failed status: %+v %v", retained, readErr)
	}
	var replay recovery.NativeRestore
	if decodeErr := json.Unmarshal([]byte(lc.must("backups", "native-copies", "create", "--review-file", reviewFile, "--acknowledge-charges", "--json")), &replay); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if replay.ID != restored.ID || !replay.ExpiresAt.Equal(restored.ExpiresAt) {
		t.Fatal("CLI replay changed copy or expiry")
	}
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
	lc.must("backups", "native-copies", "cleanup", restored.ID, "--yes", "--json")
	wait("deleted")
	if secretExists() {
		t.Fatal("confirmed absence retained native copy credential")
	}
	copies, listErr := sdk.NativeRecoveryCopies(t.Context(), source.ProjectID, lpclient.NativeCopyPage{EnvironmentID: source.EnvironmentID, Limit: 1})
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(copies.Copies) != 1 || copies.Copies[0].ID != restored.ID || copies.Copies[0].Status != "deleted" {
		t.Fatal("public history lost cleanup result")
	}

	var filteredSources recovery.NativeSourcesPage
	sourceListPath := "/api/v1/projects/" + source.ProjectID + "/native-recovery-sources?environment_id=" + url.QueryEscape(source.EnvironmentID) + "&resource="
	c.must(http.StatusOK, http.MethodGet, sourceListPath+"missing-resource", nil, &filteredSources)
	if len(filteredSources.Sources) != 0 {
		t.Fatal("HTTP source list ignored resource scope")
	}
	var filteredCopies recovery.NativeCopiesPage
	copyListPath := "/api/v1/projects/" + source.ProjectID + "/native-recovery-copies?environment_id=" + url.QueryEscape(source.EnvironmentID) + "&resource="
	c.must(http.StatusOK, http.MethodGet, copyListPath+"missing-resource", nil, &filteredCopies)
	if len(filteredCopies.Copies) != 0 {
		t.Fatal("HTTP copy list ignored resource scope")
	}
	c.must(http.StatusOK, http.MethodGet, sourceListPath+url.QueryEscape(source.Name), nil, &filteredSources)
	foundSource := false
	for _, retained := range filteredSources.Sources {
		foundSource = foundSource || retained.ID == source.ID
	}
	if !foundSource {
		t.Fatal("HTTP resource scope hid its retained source")
	}
	c.must(http.StatusOK, http.MethodGet, copyListPath+url.QueryEscape(source.Name), nil, &filteredCopies)
	if len(filteredCopies.Copies) != 1 || filteredCopies.Copies[0].ID != restored.ID {
		t.Fatal("HTTP resource scope hid its retained copy")
	}
	c.must(http.StatusNotFound, http.MethodGet, "/api/v1/native-recovery-sources/"+source.ID+"?resource=missing-resource", nil, nil)

	scopedSources, scopeErr := sdk.NativeRecoverySourcesPage(t.Context(), source.ProjectID, lpclient.NativeRecoveryPage{EnvironmentID: source.EnvironmentID, Resource: source.Name, Limit: 1})
	if scopeErr != nil || len(scopedSources.Sources) != 1 || scopedSources.Sources[0].Name != source.Name {
		t.Fatalf("Go client lost resource source scope: %+v %v", scopedSources, scopeErr)
	}
	scopedCopies, scopeErr := sdk.NativeRecoveryCopies(t.Context(), source.ProjectID, lpclient.NativeCopyPage{EnvironmentID: source.EnvironmentID, Resource: source.Name, Limit: 1})
	if scopeErr != nil || len(scopedCopies.Copies) != 1 || scopedCopies.Copies[0].ID != restored.ID {
		t.Fatalf("Go client lost resource copy scope: %+v %v", scopedCopies, scopeErr)
	}
	if _, scopeErr = sdk.NativeRecoveryInventoryPage(t.Context(), source.ID, lpclient.NativeRecoveryPage{Resource: "missing-resource"}); !errors.Is(scopeErr, lpclient.ErrNotFound) {
		t.Fatal("Go client ignored inventory resource assertion", scopeErr)
	}
	var cliSources recovery.NativeSourcesPage
	if decodeErr := json.Unmarshal([]byte(lc.must("backups", "native-sources", source.ProjectID, "--resource", source.Name, "--environment-id", source.EnvironmentID, "--json")), &cliSources); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(cliSources.Sources) != 1 || cliSources.Sources[0].Name != source.Name {
		t.Fatal("actual CLI lost scoped retained sources")
	}
	var cliCopies recovery.NativeCopiesPage
	if decodeErr := json.Unmarshal([]byte(lc.must("backups", "native-copies", source.ProjectID, "--resource", "missing-resource", "--environment-id", source.EnvironmentID, "--json")), &cliCopies); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(cliCopies.Copies) != 0 {
		t.Fatal("actual CLI ignored copy resource scope")
	}
	var contract json.RawMessage
	c.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &contract)
	var schema struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name     string `json:"name"`
				In       string `json:"in"`
				Required bool   `json:"required"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(contract, &schema); err != nil {
		t.Fatal(err)
	}
	query := map[string]bool{}
	for _, parameter := range schema.Paths["/api/v1/projects/{id}/native-recovery-copies"]["get"].Parameters {
		if parameter.In == "query" {
			if parameter.Required {
				t.Fatalf("native copy query %s must be optional", parameter.Name)
			}
			query[parameter.Name] = true
		}
	}
	for _, name := range []string{"environment_id", "before", "limit", "resource"} {
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
