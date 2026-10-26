//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/recovery"
)

func TestNativeCloudSQLRecoveryNetworkPublishedContract(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	app := start(t, cfg)
	defer app.stop()
	c := &client{t: t, base: app.srv.URL, key: cfg.Identity.PublishableKey}
	var raw json.RawMessage
	c.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &raw)
	var contract struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]map[string]any `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	schemas := contract.Components.Schemas
	if schemas["NativeRestoreTarget"].Properties["cloud_sql_network"]["$ref"] != "#/components/schemas/CloudSQLRecoveryNetwork" || schemas["CloudSQLRecoveryNetwork"].Properties["consumer_project"]["type"] != "string" || schemas["CloudSQLRecoveryNetwork"].Properties["network"]["type"] != "string" {
		t.Fatal("published native recovery contract lost its PSC network", schemas["CloudSQLRecoveryNetwork"], schemas["NativeRestoreTarget"])
	}
	if output := os.Getenv("LOOMPLANE_TEST_OPENAPI_OUTPUT"); output != "" {
		if err := os.WriteFile(output, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNativeCloudSQLTargetPinsRecoveryConsumerNetwork(t *testing.T) {
	f, store, request, _ := nativeAdmissionFixture(t)
	setRoute := func(route string) {
		t.Helper()
		if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET details=jsonb_set(details,'{cloud_sql_recovery_network}',to_jsonb($1::text)) WHERE id='native-target-account'`, route); err != nil {
			t.Fatal(err)
		}
	}
	setRoute("projects/recovery-consumer/global/networks/worker")
	target, err := store.ReviewNativeTarget(t.Context(), "org", "project", "native-cloud", "native-target")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	var network map[string]string
	if err = json.Unmarshal(fields["cloud_sql_network"], &network); err != nil || network["consumer_project"] != "recovery-consumer" || network["network"] != "projects/recovery-consumer/global/networks/worker" {
		t.Fatal("target omitted the reviewed recovery consumer network", string(raw), err)
	}
	request.Target = target
	if err = store.CheckNativeTarget(t.Context(), request); err != nil {
		t.Fatal("unchanged route refused", err)
	}
	setRoute("projects/another-consumer/global/networks/worker")
	if err = store.CheckNativeTarget(t.Context(), request); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("changed recovery route retained mutation authority", err)
	}
	setRoute("default")
	if _, err = store.ReviewNativeTarget(t.Context(), "org", "project", "native-cloud", "native-target"); !errors.Is(err, recovery.ErrInvalid) {
		t.Fatal("malformed retained route was treated as a valid target", err)
	}
	setRoute("")
	legacy, err := store.ReviewNativeTarget(t.Context(), "org", "project", "native-cloud", "native-target")
	if err != nil {
		t.Fatal("optional PSC route disabled existing snapshot targets", err)
	}
	legacyJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	fields = nil
	if err = json.Unmarshal(legacyJSON, &fields); err != nil || fields["cloud_sql_network"] != nil {
		t.Fatal("absent PSC route changed legacy target serialization", string(legacyJSON), err)
	}
}
