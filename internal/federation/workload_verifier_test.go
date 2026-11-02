package federation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/connections"
)

func TestWorkloadVerifierUsesScopedConnectionAndTLSOwnershipReads(t *testing.T) {
	r, d, deployment, rs, pod := workloadKubernetesFixture(t)
	spirePod, err := r.attestation.client.Kube.CoreV1().Pods("spire").Get(t.Context(), "spire-agent", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}
	spirePod.TypeMeta = pod.TypeMeta
	deployment.TypeMeta = metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}
	rs.TypeMeta = metav1.TypeMeta{APIVersion: "apps/v1", Kind: "ReplicaSet"}
	objects := map[string]any{
		"/api/v1/namespaces/workloads":                                      &corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: "workloads", UID: "namespace-uid"}},
		"/api/v1/nodes/node":                                                &corev1.Node{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Node"}, ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node-uid"}},
		"/api/v1/namespaces/spire/pods/spire-agent":                         spirePod,
		"/api/v1/namespaces/workloads/pods/" + pod.Name:                     pod,
		"/apis/apps/v1/namespaces/workloads/replicasets/" + rs.Name:         rs,
		"/apis/apps/v1/namespaces/workloads/deployments/" + deployment.Name: deployment,
	}
	var calls atomic.Int32
	var deny atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.Method != http.MethodGet || req.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("unexpected method or credential")
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		if deny.Load() {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		object, ok := objects[req.URL.Path]
		if !ok {
			t.Error("unexpected object read", req.URL.Path)
			http.NotFound(w, req)
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
	profile := r.profile
	profile.IdentityRevision = d.Identity.Revision()
	now := time.Now()
	credentials := &installationCredentialFixture{raw: raw, connection: connections.Connection{ID: "c", OrgID: "o", Cloud: connections.Kubernetes, Status: connections.StatusReady, CheckedAt: &now, Details: map[string]string{"cluster_ca": ca, "namespace_uid": d.Identity.NamespaceUID, "namespace": "workloads"}}}
	proofCalls := 0
	verify := VerifyKubernetesWorkloadEvidence(credentials, r.attestation.nodes, "example.test", "o", func(_ context.Context, b WorkloadDeployment, reported, _, _ string) ([][]byte, error) {
		proofCalls++
		if b != r.binding || reported != r.binding.Image {
			t.Fatal("wrong image request")
		}
		return nil, nil
	})
	got, err := verify(t.Context(), d, r.binding, profile)
	if err != nil {
		t.Fatal(err)
	}
	if got.Workload.Workload.ServiceID != "api" || got.Workload.Binding.Container != "main" || got.Workload.ContainerImage != r.binding.Image || calls.Load() != 10 || proofCalls != 1 {
		t.Fatalf("incorrect verified result/calls: %+v %d", got, calls.Load())
	}
	for _, value := range credentials.last {
		if value != 0 {
			t.Fatal("credential bytes retained")
		}
	}
	foreign := r.binding
	foreign.OrgID = "foreign"
	if _, verifyErr := verify(t.Context(), d, foreign, profile); verifyErr == nil || calls.Load() != 10 {
		t.Fatal("foreign workload reached cluster", verifyErr)
	}
	credentials.connection.Details["namespace_uid"] = "replacement"
	if _, verifyErr := verify(t.Context(), d, r.binding, profile); verifyErr == nil || calls.Load() != 10 {
		t.Fatal("changed connection reached cluster", verifyErr)
	}
	credentials.connection.Details["namespace_uid"] = d.Identity.NamespaceUID
	deny.Store(true)
	if _, verifyErr := verify(t.Context(), d, r.binding, profile); verifyErr == nil {
		t.Fatal("provider denial reused evidence")
	}
}
