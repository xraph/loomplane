package federation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/connections"
)

type installationCredentialFixture struct {
	connection connections.Connection
	raw, last  []byte
}

func (c *installationCredentialFixture) Credential(_ context.Context, org, id string) (*connections.Connection, []byte, error) {
	if org != "o" || id != "c" {
		return nil, nil, errors.New("unscoped credential request")
	}
	c.last = append([]byte(nil), c.raw...)
	return &c.connection, c.last, nil
}
func TestInstallationVerifierUsesCurrentConnectionAndLiveEvidence(t *testing.T) {
	resolver, d, pod := kubernetesAttestationFixture()
	var calls atomic.Int32
	pod.TypeMeta = metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"}
	spirePod, err := resolver.client.Kube.CoreV1().Pods("spire").Get(t.Context(), "spire-agent", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spirePod.TypeMeta = pod.TypeMeta
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("connection credential missing")
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		var object any
		switch r.URL.Path {
		case "/api/v1/namespaces/workloads":
			object = &corev1.Namespace{TypeMeta: metav1.TypeMeta{Kind: "Namespace", APIVersion: "v1"}, ObjectMeta: metav1.ObjectMeta{Name: "workloads", UID: "namespace-uid"}}
		case "/api/v1/namespaces/workloads/pods/loomplane-agent":
			object = pod
		case "/api/v1/nodes/node":
			object = &corev1.Node{TypeMeta: metav1.TypeMeta{Kind: "Node", APIVersion: "v1"}, ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node-uid"}}
		case "/api/v1/namespaces/spire/pods/spire-agent":
			object = spirePod
		default:
			t.Error("unexpected cluster request", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if encodeErr := json.NewEncoder(w).Encode(object); encodeErr != nil {
			t.Error(encodeErr)
		}
	}))
	defer server.Close()
	raw, ca := attestationCredential(server)
	d.Identity.Account = ca
	profile := resolver.profile
	profile.IdentityRevision = d.Identity.Revision()
	now := time.Now()
	credentials := &installationCredentialFixture{raw: raw, connection: connections.Connection{ID: "c", OrgID: "o", Cloud: connections.Kubernetes, Status: connections.StatusReady, CheckedAt: &now, Details: map[string]string{"cluster_ca": ca, "namespace_uid": d.Identity.NamespaceUID, "namespace": "workloads"}}}
	verify := VerifyKubernetesInstallation(credentials, resolver.nodes, "example.test", "o")
	binding, err := verify(t.Context(), d, profile)
	if err != nil {
		t.Fatal(err)
	}
	if binding.PodUID != profile.PodUID || calls.Load() != 4 {
		t.Fatal("live evidence was not read", calls.Load())
	}
	for _, value := range credentials.last {
		if value != 0 {
			t.Fatal("revealed credential buffer was retained")
		}
	}
	credentials.connection.OrgID = "foreign"
	if _, verifyErr := verify(t.Context(), d, profile); verifyErr == nil || calls.Load() != 4 {
		t.Fatal("foreign connection reached provider", verifyErr)
	}
	credentials.connection.OrgID = "o"
	credentials.connection.Details["namespace_uid"] = "recreated"
	if _, verifyErr := verify(t.Context(), d, profile); verifyErr == nil || calls.Load() != 4 {
		t.Fatal("recreated namespace reached provider", verifyErr)
	}
}
