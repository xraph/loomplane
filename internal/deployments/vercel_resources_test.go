package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

type vercelResourceFixture struct {
	creates, connects, deletes, tokens, revokes int
	connected, connectFailure                   bool
	owner, nativeType                           string
	outputKey                                   string
	checkpoint                                  []byte
	kept                                        map[string]string
}

func newVercelResourceJob(t *testing.T, kind string) (*vercelResourceFixture, *Vercel, *Job) {
	t.Helper()
	f := &vercelResourceFixture{owner: "team_a", nativeType: "blob", outputKey: "DATABASE_URL", kept: map[string]string{}}
	if kind == "vercel-integration" {
		f.nativeType = "integration"
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer admin-token" {
			t.Fatal("resource lost connection authorization")
		}
		if r.URL.Path != "/v2/teams/team_a" && r.URL.Query().Get("teamId") != "team_a" {
			t.Fatal("resource lost team scope")
		}
		resource := func() map[string]any {
			store := map[string]any{"id": "store_native", "name": "assets-production-0123456789", "type": f.nativeType, "ownerId": f.owner, "access": "private", "region": "iad1"}
			if f.connected {
				key := "BLOB_READ_WRITE_TOKEN"
				if f.nativeType == "integration" {
					key = f.outputKey
				}
				store["projectsMetadata"] = []any{map[string]any{"id": "spc_owned", "projectId": "prj_bridge", "environments": []string{"production"}, "environmentVariables": []string{key}}}
			}
			if f.nativeType == "integration" {
				store["product"] = map[string]any{"id": "prod_neon", "slug": "neon-postgres", "integrationConfigurationId": "icfg_neon", "integration": map[string]any{"id": "int_neon", "slug": "neon"}}
			}
			return store
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v2/teams/team_a":
			fmt.Fprint(w, `{"id":"team_a","slug":"team"}`)
		case "GET /v9/projects/prj_bridge":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "prj_bridge", "accountId": f.owner})
		case "POST /v1/storage/stores/blob", "POST /v1/storage/stores/integration/direct":
			f.creates++
			_ = json.NewEncoder(w).Encode(map[string]any{"store": resource()})
		case "GET /v1/storage/stores/store_native":
			_ = json.NewEncoder(w).Encode(map[string]any{"store": resource()})
		case "POST /v1/storage/stores/store_native/connections":
			if !strings.Contains(string(f.checkpoint), "store_native") {
				t.Fatal("connection started before native identity checkpoint")
			}
			f.connects++
			if f.connectFailure {
				f.connectFailure = false
				w.WriteHeader(503)
				fmt.Fprint(w, `{"error":{"message":"connect failed"}}`)
				return
			}
			f.connected = true
			fmt.Fprint(w, `{}`)
		case "GET /v10/projects/prj_bridge/env":
			if f.nativeType == "integration" {
				value := "postgres://app:application-password@neon.example/db"
				if f.outputKey == "API_KEY" {
					value = "provider-application-key"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"envs": []any{map[string]any{"id": "env_app", "key": f.outputKey, "value": value, "target": []string{"production"}}}})
			} else {
				fmt.Fprint(w, `{"envs":[{"id":"env_token","key":"BLOB_READ_WRITE_TOKEN","value":"vercel_blob_rw_native_application-secret","target":["production"]},{"id":"unrelated","key":"ADMIN_SECRET","value":"foreign-secret"}]}`)
			}
		case "POST /v1/global-config":
			f.creates++
			fmt.Fprint(w, `{"id":"ecfg_owned","slug":"assets_production_0123456789","ownerId":"team_a"}`)
		case "GET /v1/global-config/ecfg_owned":
			fmt.Fprint(w, `{"id":"ecfg_owned","slug":"assets_production_0123456789","ownerId":"team_a"}`)
		case "POST /v1/global-config/ecfg_owned/token":
			if !strings.Contains(string(f.checkpoint), "ecfg_owned") {
				t.Fatal("token created before native identity checkpoint")
			}
			f.tokens++
			fmt.Fprintf(w, `{"id":"read_%d","token":"scoped-read-%d"}`, f.tokens, f.tokens)
		case "DELETE /v1/global-config/ecfg_owned/tokens":
			f.revokes++
			fmt.Fprint(w, `{}`)
		case "DELETE /v1/storage/stores/store_native/connections/spc_owned":
			f.connected = false
			fmt.Fprint(w, `{}`)
		case "DELETE /v1/storage/stores/blob/store_native", "DELETE /v1/storage/stores/integration/store_native", "DELETE /v1/global-config/ecfg_owned":
			f.deletes++
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	conn := &connections.Connection{ID: "conn_vercel", OrgID: "org", Cloud: connections.Vercel, Status: connections.StatusReady, Details: map[string]string{"team_id": "team_a"}}
	cfg := &planning.ResourceConfig{Provider: "vercel", Kind: kind, Region: "iad1", Settings: map[string]string{"project_id": "prj_bridge", "access": "private", "integration_configuration_id": "icfg_neon", "integration_product_id": "prod_neon"}, AccountIdentity: connections.AccountIdentity(conn.Cloud, conn.Account, conn.Details)}
	job := &Job{Deployment: &Deployment{OrgID: "org", ProjectID: "proj_0123456789", EnvironmentID: "env", Environment: "production"}, Plan: &planning.Plan{ID: "plan"}, Action: planning.Action{ID: "resource:assets", Subject: planning.ResourceSubject, Name: "assets", Provider: "vercel", Resource: cfg, ConnectionID: conn.ID}, Connection: conn, Credential: []byte("admin-token"), Checkpoint: func(b []byte) error { f.checkpoint = append([]byte(nil), b...); return nil }, Progress: func(string, string) {}, Keep: func(_ context.Context, k string, b []byte) error { f.kept[k] = string(b); return nil }, Recall: func(_ context.Context, k string) ([]byte, error) {
		s, ok := f.kept[k]
		if !ok {
			return nil, ErrNoCredential
		}
		return []byte(s), nil
	}}
	return f, &Vercel{Client: vercel.New(api.URL)}, job
}

func TestVercelManagedBlobCreatesScopedOutputsAndRetriesNativeID(t *testing.T) {
	f, v, job := newVercelResourceJob(t, "vercel-blob")
	f.connectFailure = true
	if _, err := v.Apply(t.Context(), job); err == nil {
		t.Fatal("connection failure was swallowed")
	}
	job.Resume = f.checkpoint
	out, err := v.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 || f.connects != 2 || out.ExternalID != "store_native" || out.PhysicalID != "store_native" || out.OwnedResources == "" {
		t.Fatalf("native resource outcome: %+v creates=%d connections=%d", out, f.creates, f.connects)
	}
	if f.kept["output:BLOB_READ_WRITE_TOKEN"] != "vercel_blob_rw_native_application-secret" || f.kept[ResourceURL] != "vercel_blob_rw_native_application-secret" {
		t.Fatal("application credentials were not kept")
	}
	for _, s := range f.kept {
		if strings.Contains(s, "admin-token") || strings.Contains(s, "foreign-secret") {
			t.Fatal("unrelated access material was stored")
		}
	}
	if strings.Contains(string(f.checkpoint), "vercel_blob_rw_native_application-secret") {
		t.Fatal("application credential leaked into checkpoint")
	}
}

func TestVercelManagedGlobalConfigReusesItsScopedReadToken(t *testing.T) {
	f, v, job := newVercelResourceJob(t, "vercel-global-config")
	out, err := v.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	job.Resume = f.checkpoint
	if _, err = v.Apply(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 || f.tokens != 1 || out.ExternalID != "ecfg_owned" || !strings.Contains(f.kept["output:GLOBAL_CONFIG"], "scoped-read-1") {
		t.Fatal("global config retry issued duplicate resources or read tokens")
	}
}

func TestVercelManagedCreateRefusesUncertainRetry(t *testing.T) {
	f, v, job := newVercelResourceJob(t, "vercel-blob")
	job.Checkpoint = func(b []byte) error {
		f.checkpoint = append([]byte(nil), b...)
		if strings.Contains(string(b), "store_native") {
			return errors.New("checkpoint unavailable")
		}
		return nil
	}
	if _, err := v.Apply(t.Context(), job); err == nil {
		t.Fatal("failed native checkpoint was accepted")
	}
	var state map[string]any
	_ = json.Unmarshal(f.checkpoint, &state)
	// The persisted checkpoint is the intent before the request, with no receipt.
	delete(state, "receipt")
	job.Resume, _ = json.Marshal(state)
	job.Checkpoint = func(b []byte) error { f.checkpoint = append([]byte(nil), b...); return nil }
	if _, err := v.Apply(t.Context(), job); err == nil || f.creates != 1 {
		t.Fatal("uncertain create was repeated")
	}
}

func TestVercelManagedRemoveRequiresOriginalOwnershipAndAccount(t *testing.T) {
	f, v, job := newVercelResourceJob(t, "vercel-blob")
	out, err := v.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(job.Action.Resource)
	b := &planning.Binding{OrgID: "org", ProjectID: job.Deployment.ProjectID, EnvironmentID: "env", Subject: planning.ResourceSubject, Name: "assets", Provider: "vercel", ConnectionID: job.Connection.ID, ExternalID: out.ExternalID, PhysicalID: out.PhysicalID, Config: raw}
	if err = v.Remove(t.Context(), job, b); err == nil || f.deletes != 0 {
		t.Fatal("unowned resource could be removed")
	}
	_ = json.Unmarshal([]byte(out.OwnedResources), &b.OwnedResources)
	f.owner = "team_other"
	if err = v.Remove(t.Context(), job, b); err == nil || f.deletes != 0 {
		t.Fatal("another account's resource could be removed")
	}
	f.owner = "team_a"
	if err = v.Remove(t.Context(), job, b); err != nil {
		t.Fatal(err)
	}
	if f.deletes != 1 || f.connected {
		t.Fatal("owned store and connection were not removed")
	}
}

func TestVercelRuntimeResourcesAreExcludedFromProjectAndBuildVariables(t *testing.T) {
	job := &Job{Action: planning.Action{Service: &planning.ServiceConfig{Env: []planning.EnvLink{{Key: "DATABASE_URL", Subject: planning.ResourceSubject, From: "db"}}}}, Env: map[string]string{"DATABASE_URL": "private-url", "NPM_TOKEN": "build-token"}, Secrets: map[string]bool{"DATABASE_URL": true, "NPM_TOKEN": true}}
	runtime, build, secret := vercelDeploymentEnvironments(job)
	if runtime["DATABASE_URL"] != "private-url" || build["DATABASE_URL"] != "" || build["NPM_TOKEN"] != "build-token" || secret["DATABASE_URL"] || !secret["NPM_TOKEN"] {
		t.Fatal("resource credential crossed the project or build boundary")
	}
}

func TestVercelManagedIntegrationConfirmsSelectedProductBeforeCredentials(t *testing.T) {
	f, v, job := newVercelResourceJob(t, "vercel-integration")
	job.Action.Resource.Settings["integration_product_id"] = "prod_other"
	if _, err := v.Apply(t.Context(), job); err == nil || f.connects != 0 {
		t.Fatal("unselected integration product received application credentials")
	}
}

func TestVercelServiceRedactsResourceValuesWithoutSecretFlags(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error":{"message":"private-resource-value"}}`)
	}))
	defer s.Close()
	job := &Job{Deployment: &Deployment{ProjectID: "proj_0123456789", Environment: "production"}, Action: planning.Action{Name: "web", Service: &planning.ServiceConfig{Env: []planning.EnvLink{{Key: "DATABASE_URL", Subject: planning.ResourceSubject}}}}, Env: map[string]string{"DATABASE_URL": "private-resource-value"}, Progress: func(string, string) {}, Checkpoint: func([]byte) error { return nil }}
	_, err := (&Vercel{Client: vercel.New(s.URL)}).Apply(t.Context(), job)
	if err == nil || strings.Contains(err.Error(), "private-resource-value") {
		t.Fatal("resource environment value leaked through provider errors")
	}
}

func TestVercelManagedOutputStorageDoesNotExposeNewCredentials(t *testing.T) {
	_, v, job := newVercelResourceJob(t, "vercel-global-config")
	job.Keep = func(_ context.Context, _ string, value []byte) error { return fmt.Errorf("could not store %s", value) }
	_, err := v.Apply(t.Context(), job)
	if err == nil || strings.Contains(err.Error(), "scoped-read-") {
		t.Fatal("new application credential leaked through the private storage error")
	}
}

func TestVercelManagedPendingCleanupUsesCheckpointedOwnership(t *testing.T) {
	f, v, job := newVercelResourceJob(t, "vercel-blob")
	f.connectFailure = true
	if _, err := v.Apply(t.Context(), job); err == nil {
		t.Fatal("expected credential bridge failure")
	}
	job.Resume = f.checkpoint
	identity, err := v.ResolvePendingCloudResource(t.Context(), job)
	if err != nil || identity == nil || identity.ExternalID != "store_native" || identity.PhysicalID != "store_native" {
		t.Fatalf("pending identity: %+v %v", identity, err)
	}
	if err = v.RemovePendingCloudResource(t.Context(), job, identity); err != nil {
		t.Fatal(err)
	}
	if f.deletes != 1 {
		t.Fatal("pending native resource was not removed")
	}
	identity.PhysicalID = "store_foreign"
	if err = v.RemovePendingCloudResource(t.Context(), job, identity); err == nil || f.deletes != 1 {
		t.Fatal("changed pending ownership could be removed")
	}
}

func TestVercelPendingResourceCleanupRefusesServiceCheckpoints(t *testing.T) {
	_, v, job := newVercelResourceJob(t, "vercel-blob")
	job.Action.Resource = nil
	job.Action.Service = &planning.ServiceConfig{}
	job.Resume = []byte(`{"project_id":"prj_existing","deployment_id":"dpl_service"}`)
	if _, err := v.ResolvePendingCloudResource(t.Context(), job); err == nil {
		t.Fatal("service checkpoint established unproven project ownership")
	}
}

func TestVercelServiceRetryPinsOriginalProjectID(t *testing.T) {
	var touched bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":"prj_other","name":"web-production-0123456789"}`)
	}))
	defer s.Close()
	job := &Job{Deployment: &Deployment{ProjectID: "proj_0123456789", Environment: "production"}, Plan: &planning.Plan{ID: "plan"}, Action: planning.Action{Name: "web", Service: &planning.ServiceConfig{}}, Resume: []byte(`{"project_id":"prj_original"}`), Progress: func(string, string) {}, Checkpoint: func([]byte) error { return nil }, Source: func(context.Context) (io.ReadCloser, int64, error) {
		touched = true
		return nil, 0, errors.New("source requested")
	}}
	_, err := (&Vercel{Client: vercel.New(s.URL)}).Apply(t.Context(), job)
	if err == nil || touched {
		t.Fatal("service retry followed a different project ID")
	}
}

func TestVercelImportedBridgeInPlanCannotBeApplicationProject(t *testing.T) {
	job := &Job{Action: planning.Action{Service: &planning.ServiceConfig{Env: []planning.EnvLink{{Key: "APP_CONNECTION", Subject: planning.ResourceSubject, From: "db"}}}}, Plan: &planning.Plan{Actions: []planning.Action{{Name: "db", Resource: &planning.ResourceConfig{Provider: "vercel", Kind: "vercel-integration", ExistingResource: &planning.ExistingResourceConfig{ResourceID: "imported"}, Settings: map[string]string{"project_id": "prj_app"}}}}}}
	if err := vercelCredentialBridgeCollision(job, "prj_app"); err == nil {
		t.Fatal("imported bridge leaked its original credential variables to the application build")
	}
	if err := vercelCredentialBridgeCollision(job, "prj_separate"); err != nil {
		t.Fatal(err)
	}
}

func TestVercelManagedIntegrationRequiresDeclaredApplicationOutputs(t *testing.T) {
	for _, requested := range []string{"DATABASE_URL", "KV_REST_API_URL"} {
		t.Run(requested, func(t *testing.T) {
			f, v, job := newVercelResourceJob(t, "vercel-integration")
			job.Action.Resource.Settings["output_keys"] = requested
			out, err := v.Apply(t.Context(), job)
			if requested == "DATABASE_URL" {
				if err != nil || out == nil || !strings.HasPrefix(f.kept["output:DATABASE_URL"], "postgres://") {
					t.Fatalf("integration outputs: %v", err)
				}
			} else if err == nil || len(f.kept) > 0 {
				t.Fatal("missing reviewed application outputs were accepted")
			}
		})
	}
}

func TestVercelManagedIntegrationPrivateFallbackPinsCredentialBridge(t *testing.T) {
	f, v, job := newVercelResourceJob(t, "vercel-integration")
	f.outputKey = "API_KEY"
	job.Action.Resource.Settings["output_keys"] = "API_KEY"
	out, err := v.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	access, err := catalogconnect.ParseAPIAccess(f.kept[ResourceURL])
	if err != nil || access.ProjectID != "prj_bridge" || access.Environment != "production" || access.Values["API_KEY"] != "provider-application-key" {
		t.Fatalf("private integration bridge contract was not retained: %v", err)
	}
	if out.URL != "" || strings.Contains(out.OwnedResources, "provider-application-key") || strings.Contains(string(f.checkpoint), "provider-application-key") {
		t.Fatal("application output leaked into public resource state")
	}
}

func TestVercelProvisioningOptionsMergeReviewedMetadataWithoutCredentials(t *testing.T) {
	_, _, job := newVercelResourceJob(t, "vercel-integration")
	job.Action.Resource.Settings["metadata"] = `{"region":"aws-us-east-1","replicas":2}`
	job.Action.Resource.Settings["metadata.enabled"] = "true"
	opts, err := vercelResourceOptions(job)
	if err != nil || opts.Metadata["region"] != "aws-us-east-1" || opts.Metadata["replicas"] != json.Number("2") || opts.Metadata["enabled"] != true {
		t.Fatalf("reviewed provisioning metadata was lost: %v", err)
	}
	job.Action.Resource.Settings["metadata.region"] = "other"
	if _, err = vercelResourceOptions(job); err == nil {
		t.Fatal("aggregate and legacy metadata ambiguously overwrote each other")
	}
	delete(job.Action.Resource.Settings, "metadata.region")
	job.Action.Resource.Settings["metadata.api_key"] = "private"
	if _, err = vercelResourceOptions(job); err == nil {
		t.Fatal("legacy metadata bypassed the non-secret contract")
	}
}
