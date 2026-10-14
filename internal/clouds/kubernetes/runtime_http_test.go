package kubernetes

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	"k8s.io/client-go/rest"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

// A TLS HTTP fixture checks actual client serialization, namespace paths,
// resource versions and UID deletion preconditions without bypassing readiness.
func TestRuntime_strictKubernetesHTTP(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	name := WorkloadName(iid)
	objects := map[string]map[string]any{"/api/v1/namespaces/selected/secrets/existing-pull": {"kind": "Secret", "apiVersion": "v1", "data": map[string]string{"credential": "external-private"}}}
	var mu sync.Mutex
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !strings.Contains(r.URL.Path, "/namespaces/selected/") {
			t.Errorf("namespace escaped: %s", r.URL)
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/namespaces/selected/secrets/existing-pull" {
			if r.Method != "GET" || r.Header.Get("Accept") != "application/json;as=Table;g=meta.k8s.io;v=v1" || r.URL.Query().Get("includeObject") != "None" {
				t.Errorf("external pull Secret accessed: %s %s", r.Method, r.URL)
				w.WriteHeader(403)
				return
			}
			_, _ = io.WriteString(w, `{"kind":"Table","apiVersion":"meta.k8s.io/v1","columnDefinitions":[{"name":"Name"},{"name":"Type"}],"rows":[{"cells":["existing-pull","kubernetes.io/dockerconfigjson"]}]}`)
			return
		}
		key := r.URL.Path
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			var object map[string]any
			if err = json.Unmarshal(body, &object); err != nil {
				t.Error(err)
			}
			meta := object["metadata"].(map[string]any)
			key += "/" + meta["name"].(string)
			if objects[key] != nil {
				w.WriteHeader(409)
				return
			}
			meta["uid"], meta["resourceVersion"], meta["generation"] = "uid-"+meta["name"].(string), "1", 1
			if strings.Contains(key, "/deployments/") {
				spec := object["spec"].(map[string]any)
				replicas := spec["replicas"]
				object["status"] = map[string]any{"observedGeneration": 1, "updatedReplicas": replicas, "availableReplicas": replicas, "unavailableReplicas": 0}
			}
			objects[key] = object
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(object)
			return
		}
		object := objects[key]
		if object == nil {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`)
			return
		}

		if r.Method == http.MethodPut {
			var updated map[string]any
			if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
				t.Error(err)
			}
			updated["metadata"].(map[string]any)["uid"] = object["metadata"].(map[string]any)["uid"]
			updated["metadata"].(map[string]any)["resourceVersion"] = "2"
			updated["status"] = object["status"]
			objects[key] = updated
			_ = json.NewEncoder(w).Encode(updated)
			return
		}
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(object)
			return
		}
		if r.Method == http.MethodDelete {
			var options struct {
				Preconditions struct {
					UID string `json:"uid"`
				} `json:"preconditions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
				t.Error(err)
			}
			if options.Preconditions.UID != object["metadata"].(map[string]any)["uid"] {
				t.Errorf("delete lacked verified UID: %+v", options)
				w.WriteHeader(409)
				return
			}
			delete(objects, key)
			_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Success"}`)
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(400)
	}))
	defer api.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw})
	c, err := New(&Config{Namespace: "selected", Details: map[string]string{"image_pull_secret": "existing-pull"}, REST: &rest.Config{Host: api.URL, TLSClientConfig: rest.TLSClientConfig{CAData: ca}}})
	if err != nil {
		t.Fatal(err)
	}
	r := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) { return c, nil }, Values: func(context.Context, map[string]string) (owned.RuntimeValues, error) {
		return owned.RuntimeValues{Env: map[string]string{"TOKEN": "runtime-private"}}, nil
	}}
	labels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelRevision: "reviewed", owned.LabelHealth: "/_/health", owned.LabelReplicas: "2"}
	result, err := r.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: "registry.example.com/team/api@sha256:fixture", Ports: []cpprovider.PortSpec{{Container: 8080}}}, labels)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Endpoints) != 1 || !strings.Contains(result.Endpoints[0].URL, name+".selected.svc") {
		t.Fatalf("endpoint: %+v", result)
	}
	deployment := objects["/apis/apps/v1/namespaces/selected/deployments/"+name]
	encoded, _ := json.Marshal(deployment["spec"])
	if !strings.Contains(string(encoded), `"imagePullSecrets":[{"name":"existing-pull"}]`) || strings.Contains(string(encoded), "external-private") {
		t.Fatalf("pull Secret reference: %s", encoded)
	}
	status, err := r.Status(t.Context(), iid, labels)
	if err != nil || !status.Ready {
		t.Fatalf("status: %+v %v", status, err)
	}
	mu.Lock()
	deploymentJSON, _ := json.Marshal(objects["/apis/apps/v1/namespaces/selected/deployments/"+name])
	mu.Unlock()
	if strings.Contains(string(deploymentJSON), "runtime-private") {
		t.Fatal("runtime secret entered the deployment spec")
	}

	r.WaitReady = func(context.Context, *Client, string) error { return errors.New("failed replacement") }
	if _, err = r.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: "changed-image"}, labels); err == nil {
		t.Fatal("replacement unexpectedly succeeded")
	}
	restored, _ := json.Marshal(objects["/apis/apps/v1/namespaces/selected/deployments/"+name]["spec"])
	if string(restored) != string(encoded) || objects["/api/v1/namespaces/selected/secrets/existing-pull"] == nil {
		t.Fatal("rollback changed the external pull Secret or its reference")
	}
	if err = r.Remove(t.Context(), iid, labels); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(objects) != 1 || objects["/api/v1/namespaces/selected/secrets/existing-pull"] == nil {
		t.Fatalf("cleanup left %d owned objects", len(objects))
	}
}
