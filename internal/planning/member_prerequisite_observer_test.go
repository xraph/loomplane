package planning

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
)

type prerequisiteCredentialFixture struct {
	connection *connections.Connection
	raw        []byte
}

func (f prerequisiteCredentialFixture) ObservationCredential(context.Context, string, string) (*connections.Connection, []byte, error) {
	return f.connection, f.raw, nil
}
func prerequisiteCertificate(t *testing.T, serial int64) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "api.example.com"}, DNSNames: []string{"api.example.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(30 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: encoded})
}
func TestPlacementGroupProductionObserverReadOnly(t *testing.T) {
	cert, key := prerequisiteCertificate(t, 1)
	namespaceUID := "namespace-uid"
	secretUID := "secret-uid"
	var mu sync.Mutex
	denySecret := false
	writes := 0
	reads := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodGet {
			writes++
			http.Error(w, "no writes", http.StatusForbidden)
			return
		}
		reads++
		w.Header().Set("Content-Type", "application/json")
		var value any
		switch r.URL.Path {
		case "/api/v1/namespaces/apps":
			value = &corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: "apps", UID: types.UID(namespaceUID)}}
		case "/api/v1/namespaces/apps/serviceaccounts/runtime":
			value = &corev1.ServiceAccount{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"}, ObjectMeta: metav1.ObjectMeta{Name: "runtime", Namespace: "apps", UID: "sa-uid"}}
		case "/api/v1/namespaces/apps/secrets/tls":
			if denySecret {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			value = &corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{Name: "tls", Namespace: "apps", UID: types.UID(secretUID), ResourceVersion: "1"}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{corev1.TLSCertKey: cert, corev1.TLSPrivateKeyKey: key}}
		default:
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(value); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	ca := server.Certificate().Raw
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca})
	hash := sha256.Sum256(caPEM)
	endpoint := sha256.Sum256([]byte(server.URL))
	config := clientapi.Config{CurrentContext: "selected", Clusters: map[string]*clientapi.Cluster{"cluster": {Server: server.URL, CertificateAuthorityData: caPEM}}, AuthInfos: map[string]*clientapi.AuthInfo{"runtime": {Token: "fixture-token"}}, Contexts: map[string]*clientapi.Context{"selected": {Cluster: "cluster", AuthInfo: "runtime", Namespace: "apps"}}}
	raw, err := clientcmd.Write(config)
	if err != nil {
		t.Fatal(err)
	}
	a := &cloudinfra.RuntimeAttachment{ID: "attachment", OrgID: "org", ProjectID: "project", ClusterID: "cluster", Namespace: "apps", NamespaceUID: namespaceUID, ServiceAccountName: "runtime", ServiceAccountUID: "sa-uid", RuntimeConnectionID: "connection", TrustRevision: 1, TrustFingerprint: hex.EncodeToString(hash[:]), EndpointFingerprint: hex.EncodeToString(endpoint[:])}
	c := &connections.Connection{ID: "connection", OrgID: "org", Cloud: connections.Kubernetes, Method: connections.MethodManagedKubernetes, Generation: 1, Status: connections.StatusReady, Details: map[string]string{"managed_attachment_id": a.ID, "managed_project_id": a.ProjectID, "managed_resource_id": a.ClusterID, "namespace": a.Namespace, "namespace_uid": a.NamespaceUID, "cluster_ca": a.TrustFingerprint, "endpoint": server.URL}}
	observer := &KubernetesPrerequisiteObserver{Credentials: prerequisiteCredentialFixture{c, raw}}
	first, err := observer.Observe(t.Context(), a, "tls")
	if err != nil {
		t.Fatal(err)
	}
	if err = first.VerifyHostname("api.example.com", time.Now()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	cert, key = prerequisiteCertificate(t, 2)
	mu.Unlock()
	second, err := observer.Observe(t.Context(), a, "tls")
	if err != nil || first.Fingerprint == second.Fingerprint || second.SecretUID != first.SecretUID {
		t.Fatalf("renewal observation failed: %v", err)
	}
	encoded, err := json.Marshal(second)
	if err != nil || strings.Contains(string(encoded), "PRIVATE KEY") || strings.Contains(string(encoded), "fixture-token") {
		t.Fatal("credential escaped")
	}
	mu.Lock()
	denySecret = true
	mu.Unlock()
	if _, err = observer.Observe(t.Context(), a, "tls"); !errors.Is(err, ErrPrerequisiteAccessDenied) {
		t.Fatalf("read denial was not explicit: %v", err)
	}
	mu.Lock()
	denySecret = false
	mu.Unlock()
	mu.Lock()
	namespaceUID = "recreated"
	mu.Unlock()
	if _, err = observer.Observe(t.Context(), a, "tls"); err == nil {
		t.Fatal("recreated namespace accepted")
	}
	mu.Lock()
	defer mu.Unlock()
	if writes != 0 || reads < 7 {
		t.Fatalf("unexpected Kubernetes requests: reads=%d writes=%d", reads, writes)
	}
}
