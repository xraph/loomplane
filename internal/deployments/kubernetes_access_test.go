package deployments

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	cpprovider "github.com/xraph/ctrlplane/provider"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	nativepg "github.com/xraph/loomplane/internal/clouds/postgres"
	nativecache "github.com/xraph/loomplane/internal/clouds/redis"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
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

// kubernetesSessionFixture is a checked cluster whose access reviews
// allow what allowed says, and a target whose session opens it.
func kubernetesSessionFixture(t *testing.T, allowed func(*authv1.ResourceAttributes) bool) (*OwnedTarget, map[string]string) {
	t.Helper()
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
			review.Status.Allowed = allowed(review.Spec.ResourceAttributes)
			review.Kind, review.APIVersion = "SelfSubjectAccessReview", "authorization.k8s.io/v1"
			_ = json.NewEncoder(w).Encode(review)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
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
	connection := &connections.Connection{ID: "selected", OrgID: "org", Cloud: connections.Kubernetes, Status: connections.StatusReady, Details: checked.Details}
	reviewed, err := json.Marshal(planning.ReviewedConnectionSettings(connection.Cloud, connection.Details))
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{owned.LabelOrg: "org", owned.LabelConnection: "selected", owned.LabelSettings: string(reviewed), owned.LabelArchitecture: "arm64"}
	target := &OwnedTarget{Cloud: connections.Kubernetes, Credentials: buildCredentialFunc(func(context.Context, string, string) (*connections.Connection, []byte, error) {
		return connection, raw, nil
	})}
	return target, labels
}

func TestKubernetesSessionChecksExecForCaches(t *testing.T) {
	var revoked atomic.Bool
	target, labels := kubernetesSessionFixture(t, func(attrs *authv1.ResourceAttributes) bool {
		return !revoked.Load() || attrs.Resource != "pods" || attrs.Subresource != "exec"
	})
	labels[owned.LabelOperation] = "read"
	labels[nativecache.LabelEngine] = "valkey"
	if _, err := target.kubernetesSession("org", "selected")(t.Context(), labels); err != nil {
		t.Fatal(err)
	}
	revoked.Store(true)
	if _, err := target.kubernetesSession("org", "selected")(t.Context(), labels); err == nil {
		t.Fatal("a cache session ran without pod exec")
	}
	delete(labels, nativecache.LabelEngine)
	if _, err := target.kubernetesSession("org", "selected")(t.Context(), labels); err != nil {
		t.Fatalf("an ordinary workload needed pod exec: %v", err)
	}
}

func TestKubernetesSessionChecksRBACForComponentAccess(t *testing.T) {
	var revoked atomic.Bool
	target, labels := kubernetesSessionFixture(t, func(attrs *authv1.ResourceAttributes) bool {
		return !revoked.Load() || attrs.Resource != "rolebindings" || attrs.Verb != "create"
	})
	labels[owned.LabelOperation] = "apply"
	labels[owned.LabelKubernetesRules] = `[{"api_groups":[""],"resources":["services"],"verbs":["get","list","watch"]}]`
	if _, err := target.kubernetesSession("org", "selected")(t.Context(), labels); err != nil {
		t.Fatal(err)
	}
	revoked.Store(true)
	_, err := target.kubernetesSession("org", "selected")(t.Context(), labels)
	if err == nil || !strings.Contains(err.Error(), "needs a Role to discover Services") {
		t.Fatalf("apply without rbac: %v", err)
	}
	// Reading or removing the component doesn't need it.
	labels[owned.LabelOperation] = "remove"
	if _, err = target.kubernetesSession("org", "selected")(t.Context(), labels); err != nil {
		t.Fatalf("remove needed rbac: %v", err)
	}
	labels[owned.LabelOperation] = "apply"
	delete(labels, owned.LabelKubernetesRules)
	if _, err = target.kubernetesSession("org", "selected")(t.Context(), labels); err != nil {
		t.Fatalf("a workload without rules needed rbac: %v", err)
	}
}

func TestComponentSpecCarriesAccessReadinessAndVolumeGroup(t *testing.T) {
	component := recipes.Component{
		Name: "gateway", Role: recipes.RoleProcess,
		Ports:           []recipes.Port{{Name: "http", Container: 8080, Protocol: "http"}, {Name: "queue", Container: 6379, Protocol: "redis"}},
		Readiness:       &recipes.HTTPProbe{Port: "queue"},
		Volume:          &recipes.Volume{MountPath: "/data", KubernetesSizeMB: 1024, FSGroup: 1000},
		KubernetesRules: []recipes.KubernetesRule{{APIGroups: []string{""}, Resources: []string{"services"}, Verbs: []string{"get", "list", "watch"}}},
	}
	labels := map[string]string{}
	componentSpec(&cpprovider.ServiceSpec{}, labels, &planning.ComponentConfig{Kind: "sample", Resource: "app", Component: recipes.ResolvedComponent{Component: component}}, "kubernetes")
	if labels[owned.LabelKubernetesRules] != `[{"api_groups":[""],"resources":["services"],"verbs":["get","list","watch"]}]` || labels[owned.LabelHealthProtocol] != "redis" || labels[owned.LabelHealthPort] != "6379" || labels[owned.LabelFSGroup] != "1000" {
		t.Fatalf("labels: %v", labels)
	}

	component.Readiness = &recipes.HTTPProbe{Port: "http", Path: "/readyz"}
	component.Volume.FSGroup = 0
	component.KubernetesRules = nil
	labels = map[string]string{}
	componentSpec(&cpprovider.ServiceSpec{}, labels, &planning.ComponentConfig{Kind: "sample", Resource: "app", Component: recipes.ResolvedComponent{Component: component}}, "kubernetes")
	for _, key := range []string{owned.LabelKubernetesRules, owned.LabelHealthProtocol, owned.LabelFSGroup} {
		if _, ok := labels[key]; ok {
			t.Fatalf("%s set: %v", key, labels)
		}
	}
}
