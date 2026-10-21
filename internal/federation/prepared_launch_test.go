package federation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/xraph/loomplane/internal/agentinstall"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type preparedLaunchEnrollment struct {
	*readyFixture
	preparation          *preparationMemory
	reservations, issues int
	failIssue            bool
}

func (b *preparedLaunchEnrollment) Reserve(context.Context, workloadidentity.Binding, map[string]runtimecatalog.ExportBounds) (*workloadidentity.EnrollmentRecord, error) {
	return nil, ErrInvalid
}
func (b *preparedLaunchEnrollment) ReservePrepared(ctx context.Context, binding workloadidentity.Binding, _ map[string]runtimecatalog.ExportBounds, id, _ string, guard func(context.Context) error) (*workloadidentity.EnrollmentRecord, error) {
	if err := guard(ctx); err != nil {
		return nil, err
	}
	if b.preparation.record.ID != id {
		return nil, ErrNotFound
	}
	if b.preparation.record.EnrollmentID == "" {
		b.reservations++
		binding.Agent = b.record.Binding.Agent
		binding.EntryID = uuid.NewString()
		b.record.Binding = binding
		b.record.Registration.DestinationGeneration = binding.Destination.Generation
		b.preparation.record.EnrollmentID = b.record.ID
		b.preparation.record.Generation++
	}
	return b.record, nil
}
func (b *preparedLaunchEnrollment) Issue(ctx context.Context, _, _ string, issuer workloadidentity.EnrollmentIssuer) error {
	b.issues++
	if _, err := issuer.Register(ctx, b.record.Binding); err != nil {
		return err
	}
	if b.failIssue {
		return errors.New("issuer response lost")
	}
	return nil
}
func (*preparedLaunchEnrollment) Revoke(context.Context, string, string, workloadidentity.EnrollmentIssuer) error {
	return nil
}

type preparedLaunchFixture struct {
	service      *PreparationLaunchService
	preparation  *preparationMemory
	enrollment   *preparedLaunchEnrollment
	installation *installationMemory
	client       *fake.Clientset
	principal    security.Principal
	input        PreparedLaunchInput
	writes       int
}

func newPreparedLaunchFixture(t *testing.T) *preparedLaunchFixture {
	t.Helper()
	launch, ready, installations, p, in, _ := launchServiceFixture(t)
	d := installations.destinations.rows[in.DestinationID]
	r := &Preparation{Scope: d.Scope, PreparationIntent: PreparationIntent{ID: in.AttemptID, Namespace: "workloads", NamespaceUID: d.Identity.NamespaceUID, Image: launch.cfg.AgentImage}, DestinationID: d.ID, DestinationGeneration: d.Generation, IdentityRevision: d.Identity.Revision(), State: PreparationPrepared, Generation: 5, AccountUID: "sa-uid", PodUID: "pod-uid"}
	memory := &preparationMemory{record: r}
	plan, err := agentinstall.NewKubernetesPlan(r.Namespace, r.ID, r.Image, false)
	if err != nil {
		t.Fatal(err)
	}
	sa, pod, err := plan.PreparationResources(r.NamespaceUID)
	if err != nil {
		t.Fatal(err)
	}
	sa.UID = types.UID(r.AccountUID)
	pod.UID = types.UID(r.PodUID)
	pod.Spec.NodeName = "node"
	pod.Status.Phase = corev1.PodPending
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}}
	client := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: r.Namespace, UID: types.UID(r.NamespaceUID)}}, sa, pod)
	prep := NewPreparationService(launch, memory)
	prep.open = func(context.Context, domain.Destination) (kubernetes.Interface, string, error) {
		return client, r.Namespace, nil
	}
	installations.record = nil
	launch.installations.verify = func(_ context.Context, d domain.Destination, profile KubernetesAttestationProfile) (workloadidentity.Binding, error) {
		return workloadidentity.Binding{Destination: d, Method: "kubernetes", CheckedAt: time.Now(), Namespace: r.Namespace, NamespaceUID: r.NamespaceUID, PodUID: profile.PodUID, ServiceAccount: profile.ServiceAccount, Container: profile.Container, ParentURI: "spiffe://loomplane.test/spire/agent/k8s_psat/cluster/node", TTL: time.Minute}, nil
	}
	backend := &preparedLaunchEnrollment{readyFixture: ready, preparation: memory}
	enroll := NewEnrollmentService(launch.installations.destinations, backend, launch.installations.Resolve, &enrollmentIssuerFixture{})
	f := &preparedLaunchFixture{preparation: memory, enrollment: backend, installation: installations, client: client, principal: p, input: PreparedLaunchInput{DestinationID: d.ID, PreparationID: r.ID, SPIRECluster: "cluster", SPIRENamespace: "spire", SPIREServiceAccount: "spire-agent"}}
	f.service = NewPreparationLaunchService(prep, enroll)
	client.PrependReactor("create", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
		f.writes++
		create, ok := action.(ktesting.CreateAction)
		if !ok {
			t.Fatal("not create")
		}
		cm, ok := create.GetObject().(*corev1.ConfigMap)
		if !ok {
			t.Fatal("not config")
		}
		cm.UID = "config-uid"
		return false, nil, nil
	})
	launch.deliver = func(ctx context.Context, plan *agentinstall.KubernetesPlan, _ domain.Destination, r workloadidentity.EnrollmentRecord, s agentinstall.LaunchSettings, guard func(context.Context) error) (*corev1.ConfigMap, error) {
		return plan.Deliver(ctx, client, r, s, guard)
	}
	launch.inspect = func(ctx context.Context, _ domain.Destination, a *LaunchAttempt) (*corev1.ConfigMap, error) {
		return client.CoreV1().ConfigMaps(a.Namespace).Get(ctx, a.ConfigMapName, metav1.GetOptions{})
	}
	return f
}
func TestPreparedLaunchResumesLostIssuanceAndDelivery(t *testing.T) {
	for _, lost := range []string{"none", "issuance", "delivery"} {
		t.Run(lost, func(t *testing.T) {
			f := newPreparedLaunchFixture(t)
			launch := f.service.preparation.launch
			if lost == "issuance" {
				f.enrollment.failIssue = true
			}
			if lost == "delivery" {
				deliver := launch.deliver
				launch.deliver = func(ctx context.Context, p *agentinstall.KubernetesPlan, d domain.Destination, r workloadidentity.EnrollmentRecord, s agentinstall.LaunchSettings, g func(context.Context) error) (*corev1.ConfigMap, error) {
					if _, err := deliver(ctx, p, d, r, s, g); err != nil {
						return nil, err
					}
					return nil, errors.New("delivery reply lost")
				}
			}
			result, err := f.service.Launch(t.Context(), f.principal, f.input)
			if lost != "none" {
				if err == nil {
					t.Fatal("lost reply not surfaced")
				}
				f.enrollment.failIssue = false
				if lost == "delivery" {
					f.enrollment.fail = true
				}
				f.service = NewPreparationLaunchService(f.service.preparation, f.service.enrollment)
				result, err = f.service.Launch(t.Context(), f.principal, f.input)
			}
			if err != nil || result.ConfigMapUID != "config-uid" || f.installation.puts != 1 || f.enrollment.reservations != 1 || f.writes != 1 {
				t.Fatal("unsafe or incomplete recovery", result, err, f.installation.puts, f.enrollment.reservations, f.writes)
			}
			calls := f.enrollment.issues
			f.enrollment.fail = true
			if _, err = f.service.Launch(t.Context(), f.principal, f.input); err != nil || f.enrollment.issues != calls || f.writes != 1 || f.installation.puts != 1 {
				t.Fatal("completed launch repeated effects", err)
			}
		})
	}
}
func TestPreparedLaunchRejectsUnsafePreparationBeforeEnrollment(t *testing.T) {
	for _, mode := range []string{"unscheduled", "running", "restarted", "changed account", "changed image", "foreign config", "retiring", "foreign namespace", "foreign organization", "foreign destination", "read only", "unverified node"} {
		t.Run(mode, func(t *testing.T) {
			f := newPreparedLaunchFixture(t)
			r := f.preparation.record
			name := "loomplane-agent-" + r.ID
			pod, err := f.client.CoreV1().Pods(r.Namespace).Get(t.Context(), name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "unscheduled":
				pod.Spec.NodeName = ""
			case "running":
				pod.Status.Phase = corev1.PodRunning
			case "restarted":
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "agent", RestartCount: 1}}
			case "changed image":
				pod.Spec.Containers[0].Image = "example.test/foreign:latest"
			case "changed account":
				r.AccountUID = "foreign"
			case "retiring":
				r.State = PreparationRetiring
			case "foreign namespace":
				r.NamespaceUID = "foreign"
			case "foreign organization":
				f.principal.OrgID = "foreign"
			case "foreign destination":
				f.input.DestinationID = "foreign"
			case "read only":
				f.principal.ReadOnly = true
			case "foreign config":
				if _, err = f.client.CoreV1().ConfigMaps(r.Namespace).Create(t.Context(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name}}, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
				f.writes = 0
			case "unverified node":
				f.service.preparation.launch.installations.verify = func(context.Context, domain.Destination, KubernetesAttestationProfile) (workloadidentity.Binding, error) {
					return workloadidentity.Binding{}, ErrUnverified
				}
			}
			if _, err = f.client.CoreV1().Pods(r.Namespace).Update(t.Context(), pod, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err = f.service.Launch(t.Context(), f.principal, f.input); err == nil {
				t.Fatal("unsafe preparation launched")
			}
			if f.enrollment.reservations != 0 || f.enrollment.issues != 0 || f.writes != 0 {
				t.Fatal("denied preparation issued authority")
			}
		})
	}
}

type lostInstallationReply struct {
	InstallationStore
	lose bool
}

func (s *lostInstallationReply) PutInstallation(ctx context.Context, d domain.Destination, p KubernetesAttestationProfile, expected int64, actor string, checked time.Time, guard func(context.Context) error) (*KubernetesInstallation, error) {
	r, err := s.InstallationStore.PutInstallation(ctx, d, p, expected, actor, checked, guard)
	if err != nil {
		return nil, err
	}
	if s.lose {
		s.lose = false
		return nil, errors.New("installation reply lost")
	}
	return r, nil
}
func TestPreparedLaunchRecoversInstallationWithoutReplacingIt(t *testing.T) {
	f := newPreparedLaunchFixture(t)
	f.service.preparation.launch.installations.store = &lostInstallationReply{InstallationStore: f.installation, lose: true}
	if _, err := f.service.Launch(t.Context(), f.principal, f.input); err == nil || f.enrollment.reservations != 0 {
		t.Fatal("lost installation reply ignored")
	}
	if _, err := f.service.Launch(t.Context(), f.principal, f.input); err != nil || f.installation.puts != 1 || f.enrollment.reservations != 1 {
		t.Fatal("installation recovery repeated write", err)
	}
}
func TestPreparedLaunchDoesNotRepeatMissingDelivery(t *testing.T) {
	f := newPreparedLaunchFixture(t)
	calls := 0
	f.service.preparation.launch.deliver = func(context.Context, *agentinstall.KubernetesPlan, domain.Destination, workloadidentity.EnrollmentRecord, agentinstall.LaunchSettings, func(context.Context) error) (*corev1.ConfigMap, error) {
		calls++
		return nil, errors.New("unknown delivery result")
	}
	for range 2 {
		if _, err := f.service.Launch(t.Context(), f.principal, f.input); err == nil {
			t.Fatal("unknown delivery completed")
		}
	}
	if calls != 1 || f.enrollment.reservations != 1 || f.enrollment.issues != 1 || f.installation.puts != 1 {
		t.Fatal("unknown write replayed", calls)
	}
}
func TestPreparedLaunchDoesNotReplaceTrustedProfile(t *testing.T) {
	for _, mode := range []string{"spire profile", "pod identity", "generation", "preparation receipt"} {
		t.Run(mode, func(t *testing.T) {
			f := newPreparedLaunchFixture(t)
			if _, err := f.service.Launch(t.Context(), f.principal, f.input); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "spire profile":
				f.input.SPIRECluster = "other"
			case "pod identity":
				f.installation.record.Profile.PodUID = "other"
			case "generation":
				d := f.installation.destinations.rows[f.input.DestinationID]
				d.Generation++
				f.installation.destinations.rows[d.ID] = d
				f.installation.record.Generation = d.Generation
			case "preparation receipt":
				f.preparation.record.EnrollmentID = uuid.NewString()
			}
			if _, err := f.service.Launch(t.Context(), f.principal, f.input); err == nil {
				t.Fatal("mismatched authority recovered")
			}
			if f.installation.puts != 1 || f.enrollment.reservations != 1 || f.enrollment.issues != 1 || f.writes != 1 {
				t.Fatal("mismatch changed authority")
			}
		})
	}
}
