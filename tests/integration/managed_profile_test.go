//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
)

func TestManagedProfilePlanningGate(t *testing.T) {
	f, _, plan, result, _ := managedDemandFixture(t)
	svc := planning.NewService(planning.Deps{Store: f.plans, Capacity: managedDemandAuthority{disabled: true}})
	// Plan01 does not need a capacity target, scheduling profile or requests.
	result.Actions[0].Service.CloudPlacement = nil
	result.Actions[0].Service.Size = nil
	for _, mode := range []string{"", "guaranteed", "best_effort"} {
		result.Actions[0].Service.CapacityMode = mode
		err := svc.PrepareClusterWorkloadDemands(t.Context(), plan, result)
		if mode == "" {
			if err != nil || result.Actions[0].Service.ClusterDemand != nil || result.RequiredRuntimeProtocol != "" {
				t.Fatalf("legacy planning changed: %+v %v", result, err)
			}
		} else {
			var block *cloudinfra.CapacityAdmissionError
			if !errors.As(err, &block) || block.Block.Reason != "cluster_capacity_disabled" {
				t.Fatalf("explicit mode %s: %v", mode, err)
			}
		}
	}
	var count int
	if err := f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cluster_workload_holds`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy planning reserved capacity: %d %v", count, err)
	}
}

type managedProfileCredentials struct {
	connection *connections.Connection
	raw        []byte
}

func (c managedProfileCredentials) Credential(_ context.Context, org, connection string) (*connections.Connection, []byte, error) {
	if c.connection.OrgID != org || c.connection.ID != connection {
		return nil, nil, errors.New("wrong connection")
	}
	return c.connection, c.raw, nil
}

// This API fixture supplies Kubernetes readiness; all requests pass through the
// production session, client-go transport, runtime and PostgreSQL receipt store.
func managedProfileAPI(t *testing.T) ([]byte, map[string]string) {
	t.Helper()
	var mu sync.Mutex
	objects := map[string]map[string]any{}
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			_ = json.NewEncoder(w).Encode(map[string]any{"gitVersion": "v1.36.4"})
			return
		case "/api/v1/namespaces/apps":
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "Namespace", "apiVersion": "v1", "metadata": map[string]any{"name": "apps", "uid": "namespace-uid"}})
			return
		case "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
			var review authv1.SelfSubjectAccessReview
			if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			review.Status.Allowed = true
			review.Kind = "SelfSubjectAccessReview"
			review.APIVersion = "authorization.k8s.io/v1"
			_ = json.NewEncoder(w).Encode(review)
			return
		}
		switch r.Method {
		case http.MethodGet:
			if object, ok := objects[r.URL.Path]; ok {
				_ = json.NewEncoder(w).Encode(object)
				return
			}
		case http.MethodPost, http.MethodPut:
			var object map[string]any
			if err := json.NewDecoder(r.Body).Decode(&object); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			meta := object["metadata"].(map[string]any)
			name := meta["name"].(string)
			meta["uid"] = "uid-" + name
			meta["resourceVersion"] = "1"
			meta["generation"] = float64(1)
			path := r.URL.Path
			if r.Method == http.MethodPost {
				path += "/" + name
			}
			if object["kind"] == "Deployment" {
				object["status"] = map[string]any{"observedGeneration": 1, "updatedReplicas": 1, "availableReplicas": 1, "replicas": 1}
			}
			objects[path] = object
			_ = json.NewEncoder(w).Encode(object)
			return
		case http.MethodDelete:
			delete(objects, r.URL.Path)
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Success"})
			return
		}
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": "NotFound", "code": 404})
	}))
	t.Cleanup(api.Close)
	raw, err := clientcmd.Write(clientapi.Config{CurrentContext: "fixture", Clusters: map[string]*clientapi.Cluster{"fixture": {Server: api.URL, CertificateAuthorityData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw})}}, AuthInfos: map[string]*clientapi.AuthInfo{"fixture": {Token: "fixture-only"}}, Contexts: map[string]*clientapi.Context{"fixture": {Cluster: "fixture", AuthInfo: "fixture", Namespace: "apps"}}})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := kube.ParseConfig(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	client, err := kube.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := client.Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return raw, checked.Details
}

func TestManagedProfileLegacyRuntimeAndReceipt(t *testing.T) {
	raw, details := managedProfileAPI(t)
	f, _, plan, result, _ := managedDemandFixture(t, details)
	svc := planning.NewService(planning.Deps{Store: f.plans, Capacity: managedDemandAuthority{disabled: true}})
	if err := svc.PrepareClusterWorkloadDemands(t.Context(), plan, result); err != nil {
		t.Fatal(err)
	}
	resultRaw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,manifest_id,manifest_revision,result,executable,created_by,created_at) VALUES($1,'org','proj','env','production','ready','legacy-review','pool-manifest-proj',1,$2,true,'usr',now())`, plan.ID, resultRaw); err != nil {
		t.Fatal(err)
	}
	acceptFuture(t, f, plan.ID)
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployment_workflow_runs(org_id,plan_id,workflow_id,request) VALUES('org',$1,'legacy-profile-run','{"build_id":"deployment-v2"}')`, plan.ID); err != nil {
		t.Fatal(err)
	}
	details["managed_attachment_id"] = "attachment"
	details["managed_resource_id"] = "cluster"
	details["managed_project_id"] = "proj"
	connection := &connections.Connection{ID: "cluster-runtime", OrgID: "org", Cloud: connections.Kubernetes, Method: connections.MethodManagedKubernetes, Status: connections.StatusReady, Details: details}
	store := kube.NewControllerStore(f.db)
	registry := cpprovider.NewRegistry()
	target := &deployments.OwnedTarget{Cloud: connections.Kubernetes, Controllers: store, Credentials: managedProfileCredentials{connection, raw}, Ctrl: deployments.CtrlPlane{Providers: registry}}
	key := target.Register("org", connection.ID)
	registered, err := registry.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	runtime := registered.(*owned.Provider).Backend.(*kube.Runtime)
	runtime.Values = func(context.Context, map[string]string) (owned.RuntimeValues, error) {
		return owned.RuntimeValues{}, nil
	}
	runtime.Wait = time.Second
	iid := id.New(id.PrefixInstance)
	reviewed, _ := json.Marshal(planning.ReviewedConnectionSettings(connection.Cloud, connection.Details))
	labels := map[string]string{owned.LabelOrg: "org", owned.LabelProject: "proj", owned.LabelEnvironment: "env", owned.LabelService: "api", owned.LabelPlan: plan.ID, owned.LabelConnection: connection.ID, owned.LabelAction: "service:api", owned.LabelRuntime: "org/env/api/runtime_legacy", owned.LabelRevision: "legacy-deployment/service:api", owned.LabelSettings: string(reviewed), owned.LabelReplicas: "1"}
	spec := cpprovider.ServiceSpec{Name: "api", Image: "registry.invalid/api@sha256:" + strings.Repeat("a", 64), Resources: cpprovider.ResourceSpec{CPUMillis: 100, MemoryMB: 128}}
	if _, err = runtime.Apply(t.Context(), iid, spec, labels); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.GetController(t.Context(), "org", connection.ID, iid.String(), labels[owned.LabelRuntime])
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ReleaseID != "" {
		t.Fatal("legacy deployment received journal receipt")
	}
	var count int
	for _, table := range []string{"loomplane_cluster_workload_holds", "loomplane_managed_workload_authority", "loomplane_managed_workload_issuances"} {
		if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("legacy %s count %d: %v", table, count, err)
		}
	}
	target.Controllers = store.WithClusterCapacity(true)
	if _, err = runtime.Apply(t.Context(), iid, spec, labels); !errors.Is(err, kube.ErrManagedReviewRequired) {
		t.Fatalf("legacy enabled write: %v", err)
	}
	if _, err = runtime.Status(t.Context(), iid, labels); err != nil {
		t.Fatalf("historical read: %v", err)
	}
	saved, err := store.WithClusterCapacity(true).GetController(t.Context(), "org", connection.ID, iid.String(), labels[owned.LabelRuntime])
	if err != nil || *saved != *receipt {
		t.Fatalf("historical identity changed: %v", err)
	}
	var after []byte
	if err = f.sql.QueryRow(t.Context(), `SELECT result FROM loomplane_plans WHERE id=$1`, plan.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	var beforeValue, afterValue any
	_ = json.Unmarshal(resultRaw, &beforeValue)
	_ = json.Unmarshal(after, &afterValue)
	beforeCanonical, _ := json.Marshal(beforeValue)
	afterCanonical, _ := json.Marshal(afterValue)
	var build string
	if err = f.sql.QueryRow(t.Context(), `SELECT request->>'build_id' FROM loomplane_deployment_workflow_runs WHERE workflow_id='legacy-profile-run'`).Scan(&build); err != nil || build != "deployment-v2" {
		t.Fatalf("historical workflow changed: %s %v", build, err)
	}
	if string(beforeCanonical) != string(afterCanonical) {
		t.Fatal("historical review was rewritten")
	}
}
