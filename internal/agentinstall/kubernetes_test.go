package agentinstall

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/agent"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func launchFixture(t *testing.T, publisher bool) (*KubernetesPlan, *fake.Clientset, workloadidentity.EnrollmentRecord, LaunchSettings) {
	t.Helper()
	plan, err := NewKubernetesPlan("workloads", uuid.NewString(), "registry.example/agent@sha256:"+strings.Repeat("a", 64), publisher)
	if err != nil {
		t.Fatal(err)
	}
	pod := plan.Pod()
	pod.UID = types.UID(uuid.NewString())
	pod.Spec.NodeName = "node-one"
	pod.Status.Phase = corev1.PodPending
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: pod.Namespace, UID: "namespace-uid"}}
	client := fake.NewClientset(ns, pod)
	identity := domain.AgentIdentity{Scope: domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, DestinationID: "destination", AgentID: uuid.NewString(), Epoch: 1}
	identity.URI, err = workloadidentity.AgentURI("loomplane.test", identity)
	if err != nil {
		t.Fatal(err)
	}
	destination := domain.Destination{Scope: identity.Scope, ID: identity.DestinationID, Generation: 4, Identity: domain.PhysicalIdentity{Provider: "kubernetes", NamespaceUID: string(ns.UID)}}
	now := time.Now().UTC()
	record := workloadidentity.EnrollmentRecord{ID: uuid.NewString(), CreatedAt: now, ExpiresAt: now.Add(time.Minute), Binding: workloadidentity.Binding{Agent: identity, Destination: destination, Method: "kubernetes", Namespace: pod.Namespace, NamespaceUID: string(ns.UID), PodUID: string(pod.UID), ServiceAccount: pod.Spec.ServiceAccountName, Container: ContainerName, CheckedAt: now}, Registration: runtimecatalog.Registration{Agent: identity, DestinationGeneration: 4, Active: true}}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	settings := LaunchSettings{ControlEndpoint: "https://control.example:9443", TrustGeneration: 1, SigningKeys: []access.SnapshotKey{{PublicKey: public, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)}}}
	if publisher {
		record.Registration.Exports = map[string]runtimecatalog.ExportBounds{"export": {Scope: identity.Scope, DestinationID: identity.DestinationID, PlacementID: "placement", BindingID: "binding", Port: 8080, CIDRs: []string{"10.0.0.0/24"}}}
		settings.Targets = []runtimecatalog.KubernetesTarget{{ExportID: "export", ServiceName: "example", ServiceUID: "service-uid", PortName: "http"}}
	}
	return plan, client, record, settings
}

func TestKubernetesLaunchWaitsForExactPodAndNeverOverwrites(t *testing.T) {
	plan, client, record, settings := launchFixture(t, false)
	calls := 0
	guard := func(context.Context) error { calls++; return nil }
	cm, err := plan.Deliver(context.Background(), client, record, settings, guard)
	if err != nil {
		t.Fatal(err)
	}
	if calls < 2 || cm.Immutable == nil || !*cm.Immutable || len(cm.OwnerReferences) != 1 || string(cm.OwnerReferences[0].UID) != record.Binding.PodUID {
		t.Fatal("delivery lost its guard or pod ownership")
	}
	if cm.Name != plan.Pod().Name || cm.Namespace != record.Binding.Namespace {
		t.Fatal("configuration escaped its attempt")
	}
	var cfg agent.LaunchConfig
	if err := json.Unmarshal([]byte(cm.Data[ConfigKey]), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Registration.Agent != record.Binding.Agent || cfg.EnrollmentID != record.ID || cfg.Validate(time.Now()) != nil {
		t.Fatal("configuration is not the enrolled authority")
	}
	if _, err := plan.Deliver(context.Background(), client, record, settings, guard); err == nil {
		t.Fatal("existing configuration overwritten")
	}
	stored, _ := client.CoreV1().ConfigMaps(cm.Namespace).Get(context.Background(), cm.Name, metav1.GetOptions{})
	if stored.Data[ConfigKey] != cm.Data[ConfigKey] {
		t.Fatal("retry changed launch configuration")
	}
}

func TestKubernetesLaunchDeniesChangedEvidence(t *testing.T) {
	for name, mutate := range map[string]func(*corev1.Pod, *workloadidentity.EnrollmentRecord, *LaunchSettings){
		"replaced pod": func(p *corev1.Pod, _ *workloadidentity.EnrollmentRecord, _ *LaunchSettings) { p.UID = "replacement" },
		"unscheduled": func(p *corev1.Pod, _ *workloadidentity.EnrollmentRecord, _ *LaunchSettings) {
			p.Status.Conditions = nil
		},
		"already running": func(p *corev1.Pod, _ *workloadidentity.EnrollmentRecord, _ *LaunchSettings) {
			p.Status.Phase = corev1.PodRunning
		},
		"restart": func(p *corev1.Pod, _ *workloadidentity.EnrollmentRecord, _ *LaunchSettings) {
			p.Spec.RestartPolicy = corev1.RestartPolicyAlways
		},
		"sidecar": func(p *corev1.Pod, _ *workloadidentity.EnrollmentRecord, _ *LaunchSettings) {
			p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "sidecar"})
		},
		"init": func(p *corev1.Pod, _ *workloadidentity.EnrollmentRecord, _ *LaunchSettings) {
			p.Spec.InitContainers = []corev1.Container{{Name: "init"}}
		},
		"host networking": func(p *corev1.Pod, _ *workloadidentity.EnrollmentRecord, _ *LaunchSettings) {
			p.Spec.HostNetwork = true
		},
		"image": func(p *corev1.Pod, _ *workloadidentity.EnrollmentRecord, _ *LaunchSettings) {
			p.Spec.Containers[0].Image = "other:latest"
		},
		"command": func(p *corev1.Pod, _ *workloadidentity.EnrollmentRecord, _ *LaunchSettings) {
			p.Spec.Containers[0].Command = []string{"/bin/sh"}
		},
		"writable root": func(p *corev1.Pod, _ *workloadidentity.EnrollmentRecord, _ *LaunchSettings) {
			p.Spec.Containers[0].SecurityContext.ReadOnlyRootFilesystem = new(bool)
		},
		"token": func(p *corev1.Pod, _ *workloadidentity.EnrollmentRecord, _ *LaunchSettings) {
			v := true
			p.Spec.AutomountServiceAccountToken = &v
		},
		"expired": func(_ *corev1.Pod, r *workloadidentity.EnrollmentRecord, _ *LaunchSettings) {
			r.ExpiresAt = time.Now().Add(-time.Second)
		},
		"namespace": func(_ *corev1.Pod, r *workloadidentity.EnrollmentRecord, _ *LaunchSettings) {
			r.Binding.NamespaceUID = "other"
		},
		"wrong agent": func(_ *corev1.Pod, r *workloadidentity.EnrollmentRecord, _ *LaunchSettings) {
			r.Binding.Agent.AgentID = "other"
		},
		"generation": func(_ *corev1.Pod, r *workloadidentity.EnrollmentRecord, _ *LaunchSettings) {
			r.Registration.DestinationGeneration++
		},
		"foreign scope": func(_ *corev1.Pod, r *workloadidentity.EnrollmentRecord, _ *LaunchSettings) {
			r.Binding.Destination.OrgID = "other"
		},
		"invalid trust": func(_ *corev1.Pod, _ *workloadidentity.EnrollmentRecord, s *LaunchSettings) { s.SigningKeys = nil },
	} {
		t.Run(name, func(t *testing.T) {
			plan, client, record, settings := launchFixture(t, false)
			pod, _ := client.CoreV1().Pods("workloads").Get(context.Background(), plan.Pod().Name, metav1.GetOptions{})
			mutate(pod, &record, &settings)
			if _, err := client.CoreV1().Pods("workloads").Update(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := plan.Deliver(context.Background(), client, record, settings, func(context.Context) error { return nil }); err == nil {
				t.Fatal("unsafe delivery accepted")
			}
			cms, _ := client.CoreV1().ConfigMaps("workloads").List(context.Background(), metav1.ListOptions{})
			if len(cms.Items) != 0 {
				t.Fatal("configuration written after failed evidence check")
			}
		})
	}
}

func TestKubernetesLaunchRequiresFreshAuthority(t *testing.T) {
	for _, failAt := range []int{0, 1, 2} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			plan, client, r, s := launchFixture(t, false)
			var guard func(context.Context) error
			calls := 0
			if failAt > 0 {
				guard = func(context.Context) error {
					calls++
					if calls == failAt {
						return errors.New("authority revoked")
					}
					return nil
				}
			}
			if _, err := plan.Deliver(context.Background(), client, r, s, guard); err == nil {
				t.Fatal("missing or revoked authority accepted")
			}
			cms, _ := client.CoreV1().ConfigMaps("workloads").List(context.Background(), metav1.ListOptions{})
			if len(cms.Items) != 0 {
				t.Fatal("configuration written after revocation")
			}
		})
	}
}

func TestKubernetesPlanRestrictsRuntimePermissions(t *testing.T) {
	for _, publisher := range []bool{false, true} {
		plan, _, _, _ := launchFixture(t, publisher)
		pod := plan.Pod()
		if pod.Spec.RestartPolicy != corev1.RestartPolicyNever || *pod.Spec.AutomountServiceAccountToken || len(pod.Spec.Containers) != 1 || len(pod.Spec.InitContainers) != 0 || len(pod.Spec.Containers[0].Ports) != 0 {
			t.Fatal("unexpected workload authority")
		}
		if *plan.ServiceAccount().AutomountServiceAccountToken {
			t.Fatal("service account enables ambient tokens")
		}
		role, binding := plan.ObserverRBAC()
		if !publisher {
			if role != nil || binding != nil {
				t.Fatal("consumer acquired Kubernetes permission")
			}
			continue
		}
		if len(role.Rules) != 1 || strings.Join(role.Rules[0].Verbs, ",") != "list,watch" || strings.Join(role.Rules[0].Resources, ",") != "endpointslices" || role.Namespace != pod.Namespace || binding.Subjects[0].Name != pod.Spec.ServiceAccountName {
			t.Fatal("observer permissions widened")
		}
	}
	for _, image := range []string{"agent:latest", "registry.example/agent", "registry.example/agent@sha256:abc", "https://registry.example/agent@sha256:" + strings.Repeat("a", 64)} {
		if _, err := NewKubernetesPlan("workloads", uuid.NewString(), image, false); err == nil {
			t.Fatal("unpinned or invalid image accepted")
		}
	}
}
