package deployments

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	nativepg "github.com/xraph/loomplane/internal/clouds/postgres"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

func TestKubernetesDatabaseSessionRechecksRecoveryPermission(t *testing.T) {
	var revoked atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"gitVersion":"v1.36.4"}`))
		case "/api/v1/namespaces/databases":
			_, _ = w.Write([]byte(`{"kind":"Namespace","apiVersion":"v1","metadata":{"name":"databases","uid":"namespace-identity"}}`))
		case "/apis/storage.k8s.io/v1/storageclasses/retained":
			_, _ = w.Write([]byte(`{"kind":"StorageClass","apiVersion":"storage.k8s.io/v1","metadata":{"name":"retained","uid":"class-identity"},"provisioner":"fixture.csi","reclaimPolicy":"Retain","volumeBindingMode":"WaitForFirstConsumer"}`))
		case "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
			var review authv1.SelfSubjectAccessReview
			if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			attrs := review.Spec.ResourceAttributes
			review.Status.Allowed = !revoked.Load() || attrs.Resource != "networkpolicies" || attrs.Verb != "update"
			review.Kind, review.APIVersion = "SelfSubjectAccessReview", "authorization.k8s.io/v1"
			_ = json.NewEncoder(w).Encode(review)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	raw, err := clientcmd.Write(clientapi.Config{CurrentContext: "fixture", Clusters: map[string]*clientapi.Cluster{"fixture": {Server: server.URL, CertificateAuthorityData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})}}, AuthInfos: map[string]*clientapi.AuthInfo{"fixture": {Token: "fixture-only-token"}}, Contexts: map[string]*clientapi.Context{"fixture": {Cluster: "fixture", AuthInfo: "fixture", Namespace: "databases"}}})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := kubernetes.ParseConfig(raw, map[string]string{"storage_class": "retained", "architecture": "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	client, err := kubernetes.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := client.Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if checked.Details["postgres_recovery_permissions"] != "ready" {
		t.Fatal(checked.Details)
	}
	connection := &connections.Connection{ID: "selected", OrgID: "org", Cloud: connections.Kubernetes, Status: connections.StatusReady, Details: checked.Details}
	reviewed, err := json.Marshal(planning.ReviewedConnectionSettings(connection.Cloud, connection.Details))
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{owned.LabelOrg: "org", owned.LabelConnection: "selected", owned.LabelSettings: string(reviewed), owned.LabelArchitecture: "arm64", owned.LabelOperation: "read", nativepg.LabelDatabase: "database"}
	target := &OwnedTarget{Cloud: connections.Kubernetes, Credentials: buildCredentialFunc(func(context.Context, string, string) (*connections.Connection, []byte, error) {
		return connection, raw, nil
	})}
	if _, err = target.kubernetesSession("org", "selected")(t.Context(), labels); err != nil {
		t.Fatal(err)
	}
	revoked.Store(true)
	if _, err = target.kubernetesSession("org", "selected")(t.Context(), labels); err == nil {
		t.Fatal("database reused checked readiness after its recovery permission was revoked")
	}
}
