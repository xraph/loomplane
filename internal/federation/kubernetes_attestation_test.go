package federation

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	agentv1 "github.com/spiffe/spire-api-sdk/proto/spire/api/server/agent/v1"
	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type nodeClientFixture struct {
	agent *spiretypes.Agent
	err   error
}

func (n nodeClientFixture) GetAgent(_ context.Context, request *agentv1.GetAgentRequest, _ ...grpc.CallOption) (*spiretypes.Agent, error) {
	if request.Id.Path != "/spire/agent/k8s_psat/cluster/node-uid" {
		return nil, errors.New("wrong parent requested")
	}
	return n.agent, n.err
}
func kubernetesAttestationFixture() (*KubernetesAttestationResolver, domain.Destination, *corev1.Pod) {
	now := time.Now()
	ca := []byte("fixture-ca")
	digest := sha256.Sum256(ca)
	scope := domain.Scope{OrgID: "o", ProjectID: "p", EnvironmentID: "e"}
	d := domain.Destination{Scope: scope, ID: "d", ConnectionID: "c", Generation: 1, Identity: domain.PhysicalIdentity{Provider: "kubernetes", Account: hex.EncodeToString(digest[:]), ResourceID: "namespace-uid", NamespaceUID: "namespace-uid"}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "loomplane-agent", Namespace: "workloads", UID: "pod-uid"}, Spec: corev1.PodSpec{NodeName: "node", ServiceAccountName: "loomplane-agent", Containers: []corev1.Container{{Name: "agent", Image: "fixture"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	spirePod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "spire-agent", Namespace: "spire", UID: "spire-pod-uid"}, Spec: corev1.PodSpec{NodeName: "node", ServiceAccountName: "spire-agent"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	client := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "workloads", UID: "namespace-uid"}}, pod, spirePod, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node-uid"}})
	profile := KubernetesAttestationProfile{Scope: scope, DestinationID: "d", ConnectionID: "c", IdentityRevision: d.Identity.Revision(), PodName: pod.Name, PodUID: string(pod.UID), ServiceAccount: pod.Spec.ServiceAccountName, Container: "agent", SPIRECluster: "cluster", SPIRENamespace: "spire", SPIREServiceAccount: "spire-agent"}
	node := &spiretypes.Agent{Id: &spiretypes.SPIFFEID{TrustDomain: "example.test", Path: "/spire/agent/k8s_psat/cluster/node-uid"}, AttestationType: "k8s_psat", X509SvidExpiresAt: now.Add(time.Hour).Unix()}
	for _, v := range []string{"cluster:cluster", "agent_ns:spire", "agent_sa:spire-agent", "agent_node_name:node", "agent_pod_name:spire-agent", "agent_pod_uid:spire-pod-uid"} {
		node.Selectors = append(node.Selectors, &spiretypes.Selector{Type: "k8s_psat", Value: v})
	}
	return &KubernetesAttestationResolver{client: &kube.Client{Config: &kube.Config{Namespace: "workloads", REST: &rest.Config{TLSClientConfig: rest.TLSClientConfig{CAData: ca}}}, Kube: client}, nodes: nodeClientFixture{agent: node}, profile: profile, domain: "example.test", now: time.Now}, d, pod
}
func TestKubernetesBindingUsesVerifiedPodAndParent(t *testing.T) {
	r, d, _ := kubernetesAttestationFixture()
	b, err := r.Resolve(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	if b.ParentURI != "spiffe://example.test/spire/agent/k8s_psat/cluster/node-uid" || b.PodUID != "pod-uid" || b.NamespaceUID != "namespace-uid" || b.Method != "kubernetes" || !reflect.DeepEqual(b.Destination, d) || b.CheckedAt.IsZero() {
		t.Fatalf("incomplete binding: %+v", b)
	}
}
func TestKubernetesBindingDeniesReplacedOrUntrustedWorkload(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*KubernetesAttestationResolver, *domain.Destination, *corev1.Pod)
	}{
		{"foreign scope", func(_ *KubernetesAttestationResolver, d *domain.Destination, _ *corev1.Pod) { d.OrgID = "foreign" }},
		{"foreign connection", func(_ *KubernetesAttestationResolver, d *domain.Destination, _ *corev1.Pod) { d.ConnectionID = "other" }},
		{"changed CA", func(r *KubernetesAttestationResolver, _ *domain.Destination, _ *corev1.Pod) {
			r.client.Config.REST.CAData = []byte("other")
		}},
		{"replaced pod", func(_ *KubernetesAttestationResolver, _ *domain.Destination, p *corev1.Pod) { p.UID = "replacement" }},
		{"wrong service account", func(_ *KubernetesAttestationResolver, _ *domain.Destination, p *corev1.Pod) {
			p.Spec.ServiceAccountName = "other"
		}},
		{"terminating pod", func(_ *KubernetesAttestationResolver, _ *domain.Destination, p *corev1.Pod) {
			v := metav1.Now()
			p.DeletionTimestamp = &v
		}},
		{"quarantined", func(_ *KubernetesAttestationResolver, d *domain.Destination, _ *corev1.Pod) { d.Quarantined = true }},
		{"parent banned", func(r *KubernetesAttestationResolver, _ *domain.Destination, _ *corev1.Pod) {
			r.nodes.(nodeClientFixture).agent.Banned = true
		}},
		{"parent expired", func(r *KubernetesAttestationResolver, _ *domain.Destination, _ *corev1.Pod) {
			r.nodes.(nodeClientFixture).agent.X509SvidExpiresAt = time.Now().Add(-time.Minute).Unix()
		}},
		{"unattested parent", func(r *KubernetesAttestationResolver, _ *domain.Destination, _ *corev1.Pod) {
			r.nodes.(nodeClientFixture).agent.AttestationType = "join_token"
		}},
		{"wrong cluster", func(r *KubernetesAttestationResolver, _ *domain.Destination, _ *corev1.Pod) {
			r.nodes.(nodeClientFixture).agent.Selectors[0].Value = "cluster:other"
		}},
		{"conflicting selectors", func(r *KubernetesAttestationResolver, _ *domain.Destination, _ *corev1.Pod) {
			evidence := r.nodes.(nodeClientFixture).agent
			evidence.Selectors = append(evidence.Selectors, &spiretypes.Selector{Type: "k8s_psat", Value: "cluster:other"})
		}},
		{"missing container", func(_ *KubernetesAttestationResolver, _ *domain.Destination, p *corev1.Pod) { p.Spec.Containers = nil }},
		{"parent query failed", func(r *KubernetesAttestationResolver, _ *domain.Destination, _ *corev1.Pod) {
			r.nodes = nodeClientFixture{err: errors.New("unavailable")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, d, p := kubernetesAttestationFixture()
			tc.mutate(r, &d, p)
			if _, err := r.client.Kube.CoreV1().Pods(p.Namespace).Update(t.Context(), p, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Resolve(t.Context(), d); err == nil {
				t.Fatal("unsafe binding accepted")
			}
		})
	}
}

func TestKubernetesBindingCancellationDoesNotContactProviders(t *testing.T) {
	r, d, _ := kubernetesAttestationFixture()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Resolve(ctx, d); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled binding: %v", err)
	}
	client, ok := r.client.Kube.(*fake.Clientset)
	if !ok {
		t.Fatal("fixture client type")
	}
	if len(client.Actions()) != 0 {
		t.Fatal("cancelled enrollment contacted Kubernetes")
	}
}

func TestKubernetesBindingChecksLiveResources(t *testing.T) {
	for _, tc := range []struct {
		name, resource, namespace string
		object                    runtime.Object
	}{
		{"namespace replacement", "namespaces", "", &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "workloads", UID: "replacement"}}},
		{"node replacement", "nodes", "", &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "replacement"}}},
		{"SPIRE pod replacement", "pods", "spire", &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "spire-agent", Namespace: "spire", UID: "replacement"}, Spec: corev1.PodSpec{NodeName: "node", ServiceAccountName: "spire-agent"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}},
		{"SPIRE pod moved", "pods", "spire", &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "spire-agent", Namespace: "spire", UID: "spire-pod-uid"}, Spec: corev1.PodSpec{NodeName: "other", ServiceAccountName: "spire-agent"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, d, _ := kubernetesAttestationFixture()
			client, ok := r.client.Kube.(*fake.Clientset)
			if !ok {
				t.Fatal("fixture client type")
			}
			if err := client.Tracker().Update(schema.GroupVersionResource{Version: "v1", Resource: tc.resource}, tc.object, tc.namespace); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Resolve(t.Context(), d); err == nil {
				t.Fatal("changed infrastructure accepted")
			}
		})
	}
	r, d, _ := kubernetesAttestationFixture()
	client, ok := r.client.Kube.(*fake.Clientset)
	if !ok {
		t.Fatal("fixture client type")
	}
	client.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("access denied") })
	if _, err := r.Resolve(t.Context(), d); err == nil {
		t.Fatal("permission denial used stale evidence")
	}
}

func TestKubernetesBindingConstructorUsesExplicitCredentials(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	raw := []byte(fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: selected
clusters:
- name: cluster
  cluster:
    server: %s
    certificate-authority-data: %s
users:
- name: agent
  user:
    token: fixture-token
contexts:
- name: selected
  context:
    cluster: cluster
    user: agent
    namespace: workloads
`, server.URL, base64.StdEncoding.EncodeToString(ca)))
	fixture, _, _ := kubernetesAttestationFixture()
	resolver, err := NewKubernetesAttestationResolver(raw, nil, "example.test", fixture.nodes, fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if resolver.client.Config.Namespace != "workloads" || resolver.client.Config.REST.Host != server.URL {
		t.Fatal("explicit connection lost")
	}
	if _, err = NewKubernetesAttestationResolver(nil, nil, "example.test", fixture.nodes, fixture.profile); err == nil {
		t.Fatal("ambient Kubernetes credentials accepted")
	}
	profile := fixture.profile
	profile.PodUID = ""
	if _, err = NewKubernetesAttestationResolver(raw, nil, "example.test", fixture.nodes, profile); err == nil {
		t.Fatal("unpinned pod accepted")
	}
	if _, err = NewKubernetesAttestationResolver(raw, nil, "example.test", nil, fixture.profile); err == nil {
		t.Fatal("missing SPIRE authority accepted")
	}
}
