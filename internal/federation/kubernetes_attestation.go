package federation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	agentv1 "github.com/spiffe/spire-api-sdk/proto/spire/api/server/agent/v1"
	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// KubernetesAttestationProfile is an operator-owned installation record. It is
// never decoded from an enrollment request. Replacing the pod requires updating
// its pinned UID through the trusted installation path.
type KubernetesAttestationProfile struct {
	domain.Scope
	DestinationID       string `json:"destination_id"`
	ConnectionID        string `json:"connection_id"`
	IdentityRevision    string `json:"identity_revision"`
	PodName             string `json:"pod_name"`
	PodUID              string `json:"pod_uid"`
	ServiceAccount      string `json:"service_account"`
	Container           string `json:"container"`
	SPIRECluster        string `json:"spire_cluster"`
	SPIRENamespace      string `json:"spire_namespace"`
	SPIREServiceAccount string `json:"spire_service_account"`
}

// SPIRENodeReader reads node evidence through the authenticated SPIRE admin API.
type SPIRENodeReader = workloadidentity.NodeReader

// KubernetesAttestationResolver binds one installed agent to its actual pod and
// an active k8s_psat parent. Only the node-UID SPIRE profile is supported.
type KubernetesAttestationResolver struct {
	client  *kube.Client
	nodes   SPIRENodeReader
	profile KubernetesAttestationProfile
	domain  string
	now     func() time.Time
}

// NewKubernetesAttestationResolver uses explicit embedded connection credentials.
// The operator must bind SPIRECluster to this same cluster in SPIRE's config.
func NewKubernetesAttestationResolver(raw []byte, settings map[string]string, trustDomain string, nodes SPIRENodeReader, profile KubernetesAttestationProfile) (*KubernetesAttestationResolver, error) {
	td, err := spiffeid.TrustDomainFromString(trustDomain)
	if err != nil || td.Name() != trustDomain || nodes == nil || !validAttestationProfile(profile) {
		return nil, ErrInvalid
	}
	cfg, err := kube.ParseConfig(raw, settings)
	if err != nil {
		return nil, err
	}
	client, err := kube.New(cfg)
	if err != nil {
		return nil, err
	}
	return &KubernetesAttestationResolver{client: client, nodes: nodes, profile: profile, domain: trustDomain, now: time.Now}, nil
}
func validAttestationProfile(p KubernetesAttestationProfile) bool {
	for _, v := range []string{p.OrgID, p.ProjectID, p.EnvironmentID, p.DestinationID, p.ConnectionID, p.PodUID} {
		if !domain.ValidIdentitySegment(v) {
			return false
		}
	}
	rev, err := hex.DecodeString(p.IdentityRevision)
	if err != nil || len(rev) != sha256.Size || strings.ToLower(p.IdentityRevision) != p.IdentityRevision {
		return false
	}
	for _, v := range []string{p.PodName, p.ServiceAccount, p.SPIREServiceAccount} {
		if len(validation.IsDNS1123Subdomain(v)) != 0 {
			return false
		}
	}
	for _, v := range []string{p.Container, p.SPIRENamespace, p.SPIRECluster} {
		if len(validation.IsDNS1123Label(v)) != 0 {
			return false
		}
	}
	return true
}

// Resolve reads fresh namespace, pod, node and SPIRE parent evidence. Enrollment
// permissions are separate from the observer's namespace-scoped runtime RBAC.
func (r *KubernetesAttestationResolver) Resolve(ctx context.Context, d domain.Destination) (workloadidentity.Binding, error) {
	denied := workloadidentity.Binding{}
	if err := ctx.Err(); err != nil {
		return denied, err
	}
	p := r.profile
	if r.client == nil || r.client.Config == nil || r.client.Config.REST == nil || r.client.Kube == nil || r.nodes == nil || !validAttestationProfile(p) || d.Quarantined || d.Generation < 1 || d.Scope != p.Scope || d.ID != p.DestinationID || d.ConnectionID != p.ConnectionID || d.Identity.Provider != "kubernetes" || d.Identity.Revision() != p.IdentityRevision {
		return denied, ErrUnverified
	}
	ca := sha256.Sum256(r.client.Config.REST.CAData)
	if len(r.client.Config.REST.CAData) == 0 || hex.EncodeToString(ca[:]) != d.Identity.Account {
		return denied, ErrIdentityChanged
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	namespace := r.client.Config.Namespace
	ns, err := r.client.Kube.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		return denied, fmt.Errorf("read attestation namespace: %w", err)
	}
	if ns.DeletionTimestamp != nil || string(ns.UID) != d.Identity.NamespaceUID || d.Identity.NamespaceUID == "" {
		return denied, ErrIdentityChanged
	}
	pod, err := r.client.Kube.CoreV1().Pods(namespace).Get(ctx, p.PodName, metav1.GetOptions{})
	if err != nil {
		return denied, fmt.Errorf("read installed agent pod: %w", err)
	}
	if !installedPod(pod, p.PodUID, p.ServiceAccount) || pod.Spec.NodeName == "" {
		return denied, ErrUnverified
	}
	found := false
	for _, c := range pod.Spec.Containers {
		if c.Name == p.Container {
			found = true
		}
	}
	if !found {
		return denied, ErrUnverified
	}
	node, err := r.client.Kube.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
	if err != nil {
		return denied, fmt.Errorf("read agent node: %w", err)
	}
	if node.DeletionTimestamp != nil || node.UID == "" || strings.ContainsAny(string(node.UID), "/:") {
		return denied, ErrUnverified
	}
	parent, err := spiffeid.FromString("spiffe://" + r.domain + "/spire/agent/k8s_psat/" + p.SPIRECluster + "/" + string(node.UID))
	if err != nil {
		return denied, ErrUnverified
	}
	evidence, err := r.nodes.GetAgent(ctx, &agentv1.GetAgentRequest{Id: &spiretypes.SPIFFEID{TrustDomain: r.domain, Path: parent.Path()}})
	if err != nil {
		return denied, fmt.Errorf("read SPIRE parent evidence: %w", err)
	}
	if evidence == nil || evidence.Id == nil || evidence.Id.TrustDomain != r.domain || evidence.Id.Path != parent.Path() || evidence.Banned || evidence.AttestationType != "k8s_psat" || evidence.X509SvidExpiresAt <= r.now().Unix() {
		return denied, ErrUnverified
	}
	selectors, err := parentSelectors(evidence.Selectors)
	if err != nil {
		return denied, err
	}
	for key, want := range map[string]string{"cluster": p.SPIRECluster, "agent_ns": p.SPIRENamespace, "agent_sa": p.SPIREServiceAccount, "agent_node_name": pod.Spec.NodeName} {
		if selectors[key] != want {
			return denied, ErrUnverified
		}
	}
	if len(validation.IsDNS1123Subdomain(selectors["agent_pod_name"])) != 0 || selectors["agent_pod_uid"] == "" {
		return denied, ErrUnverified
	}
	spirePod, err := r.client.Kube.CoreV1().Pods(p.SPIRENamespace).Get(ctx, selectors["agent_pod_name"], metav1.GetOptions{})
	if err != nil {
		return denied, fmt.Errorf("read attested SPIRE pod: %w", err)
	}
	if !currentPod(spirePod, selectors["agent_pod_uid"], p.SPIREServiceAccount) || spirePod.Spec.NodeName != pod.Spec.NodeName {
		return denied, ErrUnverified
	}
	if err := ctx.Err(); err != nil {
		return denied, err
	}
	return workloadidentity.Binding{Destination: d, ParentURI: parent.String(), CheckedAt: r.now().UTC(), Method: "kubernetes", Namespace: namespace, NamespaceUID: string(ns.UID), ServiceAccount: p.ServiceAccount, PodUID: p.PodUID, Container: p.Container, TTL: time.Minute}, nil
}
func currentPod(p *corev1.Pod, uid, serviceAccount string) bool {
	return podIdentityMatches(p, uid, serviceAccount) && p.Status.Phase == corev1.PodRunning
}

// A scheduled pod may be waiting for its enrollment configuration. Its immutable
// UID and node pin the binding; SPIRE still attests the actual container at runtime.
func installedPod(p *corev1.Pod, uid, serviceAccount string) bool {
	if currentPod(p, uid, serviceAccount) {
		return true
	}
	if !podIdentityMatches(p, uid, serviceAccount) || p.Status.Phase != corev1.PodPending {
		return false
	}
	for _, condition := range p.Status.Conditions {
		if condition.Type == corev1.PodScheduled {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func podIdentityMatches(p *corev1.Pod, uid, serviceAccount string) bool {
	return p != nil && p.DeletionTimestamp == nil && string(p.UID) == uid && p.Spec.ServiceAccountName == serviceAccount
}
func parentSelectors(selectors []*spiretypes.Selector) (map[string]string, error) {
	if len(selectors) > 512 {
		return nil, ErrUnverified
	}
	out := map[string]string{}
	for _, s := range selectors {
		if s == nil || s.Type != "k8s_psat" {
			continue
		}
		key, value, ok := strings.Cut(s.Value, ":")
		if !ok {
			continue
		}
		switch key {
		case "cluster", "agent_ns", "agent_sa", "agent_node_name", "agent_pod_name", "agent_pod_uid":
		default:
			continue
		}
		if old, present := out[key]; present && old != value {
			return nil, ErrUnverified
		}
		out[key] = value
	}
	return out, nil
}
