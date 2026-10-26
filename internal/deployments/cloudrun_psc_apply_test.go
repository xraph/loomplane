package deployments

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/ctrlplane/deploy"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/cloudrun"
	"github.com/xraph/loomplane/internal/planning"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/connections"
)

type pscApplicationDNS struct{}

func (pscApplicationDNS) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("10.20.0.8")}, nil
}

type pscApplicationInstances struct {
	fakeInstances
	providers   *cpprovider.Registry
	request     []byte
	afterUpdate func()
}

func (f *pscApplicationInstances) Create(ctx context.Context, req instance.CreateRequest) (*instance.Instance, error) {
	f.request, _ = json.Marshal(req)
	p, err := f.providers.Get(req.ProviderName)
	if err != nil {
		return nil, err
	}
	created := ctrlInstance(req.Name, req.ProviderName)
	_, err = p.Provision(ctx, cpprovider.ProvisionRequest{InstanceID: created.ID, Labels: req.Labels, Services: req.Services})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (f *pscApplicationInstances) Update(ctx context.Context, iid id.ID, req instance.UpdateRequest) (*instance.Instance, error) {
	current, err := f.Get(ctx, iid)
	if err != nil {
		return nil, err
	}
	current.Services, current.Labels = req.Services, req.Labels
	f.request, _ = json.Marshal(req)
	if f.afterUpdate != nil {
		f.afterUpdate()
	}
	return current, nil
}

type pscApplicationDeploys struct {
	deploy.Service
	instances *pscApplicationInstances
	calls     int
}

func (f *pscApplicationDeploys) Deploy(ctx context.Context, req deploy.DeployRequest) (*deploy.Deployment, error) {
	f.calls++
	current, err := f.instances.Get(ctx, req.InstanceID)
	if err != nil {
		return nil, err
	}
	p, err := f.instances.providers.Get(current.ProviderName)
	if err != nil {
		return nil, err
	}
	_, err = p.Deploy(ctx, cpprovider.DeployRequest{InstanceID: req.InstanceID, Services: req.Services})
	return &deploy.Deployment{}, err
}

func TestCloudRunPSCApplyDeliversSecretsThroughCheckedRoute(t *testing.T) {
	for _, scenario := range []string{"fresh", "resume", "build-retry", "build-retry-checkpoint-failed", "update", "after-update", "subnet-replaced", "after-build", "after-secrets", "checkpoint-failed", "malformed-checkpoint", "removed-psc"} {
		t.Run(scenario, func(t *testing.T) {
			job := cloudRunPSCJob(t)
			buildRetry := strings.HasPrefix(scenario, "build-retry")
			retryCheckpointRejected := false
			profile := job.Plan.Actions[0].Resource.Managed.Profile
			route := job.Action.Service.CloudSQLNetwork
			const attachment = "projects/sql-tenant/regions/us-central1/serviceAttachments/database"
			fields := map[string]any{"kind": "sql#instance", "name": "database", "project": "app-project", "region": "us-central1", "connectionName": "app-project:us-central1:database", "createTime": "2026-10-20T00:00:00Z", "databaseVersion": "POSTGRES_17", "state": "RUNNABLE", "pscServiceAttachmentLink": attachment, "dnsName": "database.sql.goog.", "settings": map[string]any{"settingsVersion": "42", "edition": "ENTERPRISE", "availabilityType": "ZONAL", "tier": profile.ComputeClass, "dataDiskSizeGb": profile.StorageGB, "dataDiskType": profile.StorageType, "storageAutoResize": false, "connectorEnforcement": "REQUIRED", "dataApiAccess": "DISALLOW_DATA_API", "backupConfiguration": map[string]any{"enabled": true, "pointInTimeRecoveryEnabled": true, "transactionLogRetentionDays": profile.PointInTimeRecoveryDays}, "ipConfiguration": map[string]any{"ipv4Enabled": false, "pscConfig": map[string]any{"pscEnabled": true, "allowedConsumerProjects": []string{"app-project"}}}}}
			// The native API encodes disk size as a JSON string.
			fields["settings"].(map[string]any)["dataDiskSizeGb"] = "10"
			raw, _ := json.Marshal(fields)
			var source gcp.SQLInstance
			if err := json.Unmarshal(raw, &source); err != nil {
				t.Fatal(err)
			}
			physical, identityErr := source.Incarnation("app-project", "us-central1", "database")
			if identityErr != nil {
				t.Fatal(identityErr)
			}
			job.Attach[0].PhysicalID = physical
			var mu sync.Mutex
			subnetID := "123"
			secretWrites := 0
			buildWrites := 0
			var credentials []string
			var rollout []byte
			var rolloutMethod string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/v1/projects/app-project/builds":
					buildWrites++
					_, _ = w.Write([]byte(`{"metadata":{"build":{"id":"fixture-build"}}}`))
				case r.URL.Path == "/v1/projects/app-project/builds/failed":
					_, _ = w.Write([]byte(`{"id":"failed","status":"FAILURE"}`))
				case r.URL.Path == "/v1/projects/app-project/builds/fixture-build":
					if scenario == "after-build" {
						subnetID = "789"
					}
					_, _ = w.Write([]byte(`{"status":"SUCCESS"}`))
				case r.URL.Path == "/storage/v1/b" || strings.HasPrefix(r.URL.Path, "/upload/storage/") || strings.Contains(r.URL.Path, "/repositories"):
					_, _ = w.Write([]byte(`{}`))
				case r.URL.Path == "/token":
					_, _ = w.Write([]byte(`{"access_token":"fixture","token_type":"Bearer","expires_in":3600}`))
				case r.URL.Path == "/compute/v1/"+route.Subnetwork:
					_ = json.NewEncoder(w).Encode(map[string]any{"kind": "compute#subnetwork", "id": subnetID, "name": "run", "selfLink": route.Subnetwork, "region": "projects/app-project/regions/us-central1", "network": route.Network, "ipCidrRange": "10.20.0.0/26", "purpose": "PRIVATE", "stackType": "IPV4_ONLY", "state": "READY"})
				case r.URL.Path == "/compute/v1/projects/app-project/regions/us-central1/forwardingRules/database":
					_ = json.NewEncoder(w).Encode(map[string]any{"kind": "compute#forwardingRule", "id": "456", "name": "database", "selfLink": "projects/app-project/regions/us-central1/forwardingRules/database", "region": "projects/app-project/regions/us-central1", "network": route.Network, "target": attachment, "IPAddress": "10.20.0.8", "pscConnectionStatus": "ACCEPTED"})
				case r.URL.Path == "/v1/projects/app-project/instances/database":
					_, _ = w.Write(raw)
				case r.URL.Path == "/v3/projects/app-project":
					_, _ = w.Write([]byte(`{"name":"projects/123456"}`))
				case strings.Contains(r.URL.Path, "/secrets"):
					secretWrites++
					if scenario == "after-secrets" {
						subnetID = "789"
					}
					if strings.HasSuffix(r.URL.Path, ":addVersion") {
						var payload struct {
							Payload struct {
								Data string `json:"data"`
							} `json:"payload"`
						}
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Error(err)
						}
						decoded, err := base64.StdEncoding.DecodeString(payload.Payload.Data)
						if err != nil {
							t.Error(err)
						}
						credentials = append(credentials, string(decoded))
						_ = json.NewEncoder(w).Encode(map[string]string{"name": strings.TrimSuffix(r.URL.Path, ":addVersion") + "/versions/1"})
					} else {
						_, _ = w.Write([]byte(`{}`))
					}
				case strings.HasPrefix(r.URL.Path, "/v2/projects/app-project/locations/us-central1/services"):
					if r.Method == http.MethodPatch || r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/services") {
						var body json.RawMessage
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						rollout = append([]byte(nil), body...)
						rolloutMethod = r.Method
						_, _ = w.Write([]byte(`{"done":true}`))
					} else if r.Method == http.MethodGet && len(rollout) == 0 && scenario != "update" {
						w.WriteHeader(404)
					} else {
						_, _ = w.Write([]byte(`{"uri":"https://fixture.run.app"}`))
					}
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer srv.Close()
			job.Credential = []byte(strings.ReplaceAll(string(serviceAccountKey(t, srv.URL+"/token")), "shop", "app-project"))
			google := gcp.New(srv.URL).WithDNSResolver(pscApplicationDNS{})
			key, err := gcp.ParseKey(job.Credential)
			if err != nil {
				t.Fatal(err)
			}
			session, err := google.Session(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := prepareCloudRunPSC(job)
			if err != nil {
				t.Fatal(err)
			}
			var state cloudRunState
			save := func() error { var encodeErr error; job.Resume, encodeErr = json.Marshal(state); return encodeErr }
			if checkErr := prepared.check(t.Context(), session, job, &state, save); checkErr != nil {
				t.Fatal(checkErr)
			}
			state.Image = "image:fixture"
			if buildRetry {
				state.Image, state.BuildID = "", "failed"
				job.Deployment.Retries = 1
			}
			if saveErr := save(); saveErr != nil {
				t.Fatal(saveErr)
			}
			job.Checkpoint = func(raw []byte) error {
				if scenario == "build-retry-checkpoint-failed" {
					var checkpoint cloudRunState
					if decodeErr := json.Unmarshal(raw, &checkpoint); decodeErr != nil {
						return decodeErr
					}
					if checkpoint.BuildID == "" && checkpoint.BuildRetry == 1 {
						retryCheckpointRejected = true
						return errors.New("lease revoked")
					}
				}
				if scenario == "checkpoint-failed" {
					return errors.New("lease revoked")
				}
				job.Resume = append([]byte(nil), raw...)
				return nil
			}
			job.Progress = func(string, string) {}
			if scenario == "fresh" || scenario == "after-build" || buildRetry {
				if !buildRetry {
					job.Resume = nil
				}
				job.SourceID = "0123456789abcdef"
				job.Action.Service.Image = "services/api/Dockerfile"
				source := monoTarball(t)
				job.Source = func(context.Context) (io.ReadCloser, int64, error) {
					return io.NopCloser(bytes.NewReader(source)), int64(len(source)), nil
				}
			}
			if scenario == "removed-psc" {
				job.Attach = nil
				job.Action.Service.CloudSQLNetwork = nil
			}
			if scenario == "subnet-replaced" {
				mu.Lock()
				subnetID = "789"
				mu.Unlock()
			}
			if scenario == "malformed-checkpoint" {
				job.Resume = []byte(`{"image":`)
			}
			registry := cpprovider.NewRegistry()
			instances := &pscApplicationInstances{providers: registry}
			deploys := &pscApplicationDeploys{instances: instances}
			if scenario == "update" || scenario == "after-update" {
				current := ctrlInstance("api", cloudrun.Key(job.Connection.ID, job.Action.Region))
				instances.byID = map[id.ID]*instance.Instance{current.ID: current}
				job.Binding = &planning.Binding{ExternalID: current.ID.String(), Provider: "cloud-run", Region: job.Action.Region, ConnectionID: job.Connection.ID}
				if scenario == "after-update" {
					instances.afterUpdate = func() { mu.Lock(); subnetID = "789"; mu.Unlock() }
				}
			}

			executor := &CloudRun{Google: google, Ctrl: CtrlPlane{Providers: registry, Instances: instances, Deploys: deploys}, Credentials: buildCredentialFunc(func(context.Context, string, string) (*connections.Connection, []byte, error) {
				return job.Connection, job.Credential, nil
			})}
			outcome, err := executor.Apply(t.Context(), job)
			mu.Lock()
			defer mu.Unlock()
			if scenario != "resume" && scenario != "fresh" && scenario != "build-retry" && scenario != "update" {
				if err == nil || len(rollout) > 0 {
					t.Fatal("unsafe rollout accepted", err)
				}
				if scenario == "build-retry-checkpoint-failed" && (!retryCheckpointRejected || buildWrites != 0) {
					t.Fatal("replacement build crossed rejected checkpoint", retryCheckpointRejected, buildWrites)
				}
				if scenario == "after-update" && deploys.calls != 0 {
					t.Fatal("replacement after metadata update reached deployment")
				}
				if scenario != "after-secrets" && scenario != "after-update" && secretWrites > 0 {
					t.Fatal("invalid route reached Secret Manager")
				}
				return
			}
			if err != nil || outcome == nil || len(rollout) == 0 {
				t.Fatal("checked application not deployed", err)
			}
			if (scenario == "fresh" || scenario == "build-retry") && buildWrites != 1 || scenario == "resume" && buildWrites != 0 {
				t.Fatal("wrong build count", buildWrites)
			}
			if scenario == "update" && deploys.calls != 1 {
				t.Fatal("existing service did not deploy")
			}
			wantMethod := http.MethodPost
			if scenario == "update" {
				wantMethod = http.MethodPatch
			}
			if rolloutMethod != wantMethod {
				t.Fatalf("rollout method = %s, want %s", rolloutMethod, wantMethod)
			}
			if len(credentials) != 2 {
				t.Fatal("alias credentials missing", len(credentials))
			}
			for _, credential := range credentials {
				if !strings.Contains(credential, "host=%2Fcloudsql-psc%2Fapp-project") {
					t.Fatal("wrong socket delivered")
				}
			}
			for _, document := range [][]byte{instances.request, rollout, job.Resume} {
				if strings.Contains(string(document), "secret + / @") || strings.Contains(string(document), "secret%20%2B") {
					t.Fatal("credential leaked outside Secret Manager")
				}
			}
			if !strings.Contains(string(rollout), "cloudsql-psc") || !strings.Contains(string(rollout), route.Subnetwork) {
				t.Fatal("reviewed route or proxy lost")
			}
		})
	}
}
