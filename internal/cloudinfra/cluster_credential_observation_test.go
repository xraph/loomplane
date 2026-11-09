package cloudinfra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	authv1 "k8s.io/api/authorization/v1"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

type observationClusterStore struct {
	*clusterMemory
	attachment *RuntimeAttachment
}

func (s *observationClusterStore) GetRuntimeAttachment(context.Context, string, string) (*RuntimeAttachment, error) {
	a := *s.attachment
	return &a, nil
}

type observationClusterProvider struct {
	*clusterProvider
	observation provider.ClusterObservation
}

func (p *observationClusterProvider) GetCluster(context.Context, provider.AccountAccess, provider.ClusterRef) (provider.ClusterObservation, error) {
	return p.observation, nil
}

type observationSecrets struct {
	bootstrapSecrets
	puts, deletes int
}

func (s *observationSecrets) Put(ctx context.Context, org, id string, raw []byte) error {
	s.puts++
	return s.bootstrapSecrets.Put(ctx, org, id, raw)
}
func (s *observationSecrets) Delete(ctx context.Context, org, id string) error {
	s.deletes++
	return s.bootstrapSecrets.Delete(ctx, org, id)
}
func TestPlacementGroupObservationRequiresUsableCredential(t *testing.T) {
	s, store, adapter, _, principal, in := clusterFixture(t)
	ca := strings.Repeat("a", 64)
	endpoint := "https://cluster.example.test"
	obs := clusterObservation()
	obs.Endpoint = endpoint
	obs.CAHash = ca
	in.Kind = "adopt-external"
	in.Ref = obs.Ref
	in.ResourceID = "cluster"
	in.ResourceRevision = 1
	in.Attachment.ResourceRevision = 1
	op := &ClusterOperation{ID: "operation", OrgID: "org", CloudID: "cloud", ResourceID: "cluster", Kind: "adopt-external", Status: "succeeded", Principal: principal, Input: in, Enrollment: &ClusterEnrollment{AttachmentID: "attachment", ResourceRevision: 1}}
	store.op = op
	store.resource = &ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", ConnectionID: "conn", Provider: "digitalocean", AccountID: "account", ExternalID: obs.Ref.ID, Incarnation: obs.Ref.Incarnation, Revision: 1, Observation: &obs}
	a := &RuntimeAttachment{ID: "attachment", OrgID: "org", ProjectID: "proj", CloudID: "cloud", ClusterID: "cluster", OperationID: op.ID, Namespace: "ns", NamespaceUID: "ns-uid", ServiceAccountName: "runtime", ServiceAccountUID: "sa-uid", RuntimeConnectionID: "runtime", TrustRevision: 1, TrustFingerprint: ca, EndpointFingerprint: endpointFingerprint(endpoint), Revision: 1, CredentialExpiry: time.Now().Add(30 * time.Second)}
	s.Clusters = &observationClusterStore{store, a}
	s.Secrets = bootstrapSecrets{}
	adapters := NewAdapters()
	if registrationErr := adapters.RegisterCluster("digitalocean", &observationClusterProvider{adapter, obs}, provider.ClusterDiscovery); registrationErr != nil {
		t.Fatal(registrationErr)
	}
	s.Adapters = adapters
	c := connections.Connection{ID: "runtime", OrgID: "org", Cloud: connections.Kubernetes, Method: connections.MethodManagedKubernetes, Generation: 1, Details: map[string]string{"managed_attachment_id": a.ID, "managed_project_id": a.ProjectID, "namespace_uid": a.NamespaceUID, "cluster_ca": ca, "endpoint": endpoint}}
	if _, err := s.ResolveManagedCredentialForObservation(t.Context(), c); !errors.Is(err, connections.ErrCredentialRefreshNeeded) {
		t.Fatalf("no refresh-needed boundary: %v", err)
	}
	if adapter.creates != 0 || a.Revision != 1 || a.CredentialVersion != 0 {
		t.Fatal("observation maintained infrastructure")
	}
	var writes atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var value any
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
			var review authv1.SelfSubjectAccessReview
			if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			review.Status.Allowed = false
			value = review
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/ns":
			value = map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]string{"name": "ns", "uid": "ns-uid"}}
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/ns/serviceaccounts/runtime":
			value = map[string]any{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]string{"name": "runtime", "namespace": "ns", "uid": "sa-uid"}}
		default:
			writes.Add(1)
			w.WriteHeader(403)
			return
		}
		if err := json.NewEncoder(w).Encode(value); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	hash := sha256.Sum256(caPEM)
	ca = hex.EncodeToString(hash[:])
	raw, err := kubernetes.RuntimeKubeconfig(server.URL, caPEM, "ns", "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	a.CredentialExpiry = time.Now().Add(5 * time.Minute)
	a.VaultKey = "runtime-key"
	a.CredentialVersion = 1
	a.TrustFingerprint = ca
	a.EndpointFingerprint = endpointFingerprint(server.URL)
	c.Details["cluster_ca"] = ca
	c.Details["endpoint"] = server.URL
	c.Details["namespace"] = "ns"
	obs.Endpoint = server.URL
	obs.CAHash = ca
	store.resource.Observation = &obs
	adapters = NewAdapters()
	if registrationErr := adapters.RegisterCluster("digitalocean", &observationClusterProvider{adapter, obs}, provider.ClusterDiscovery); registrationErr != nil {
		t.Fatal(registrationErr)
	}
	s.Adapters = adapters
	secrets := &observationSecrets{bootstrapSecrets: bootstrapSecrets{}}
	s.Secrets = secrets
	if saveErr := s.putManagedCredential(t.Context(), a.OrgID, a.VaultKey, raw, a.CredentialExpiry); saveErr != nil {
		t.Fatal(saveErr)
	}
	secrets.puts = 0
	op.Enrollment.RetiredVaultKeys = []string{"retired-key"}
	credential, err := s.ResolveManagedCredentialForObservation(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if string(credential.Kubeconfig) != string(raw) || credential.HPA || credential.AccessRevision != 0 {
		t.Fatal("observation changed material or claimed runtime capabilities")
	}
	if secrets.puts != 0 || secrets.deletes != 0 || writes.Load() != 0 || a.CredentialVersion != 1 || a.Revision != 1 {
		t.Fatalf("observation triggered maintenance: puts=%d deletes=%d writes=%d", secrets.puts, secrets.deletes, writes.Load())
	}
}
