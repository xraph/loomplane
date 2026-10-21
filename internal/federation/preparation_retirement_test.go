package federation

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/xraph/loomplane/internal/agentinstall"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type preparationRetirementMemory struct{ *preparationMemory }

func (m *preparationRetirementMemory) BeginPreparationRetirement(ctx context.Context, org, id string, expected int64, actor string, guard func(context.Context) error) (*Preparation, error) {
	if err := guard(ctx); err != nil {
		return nil, err
	}
	r, err := m.GetPreparation(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if r.Generation != expected || r.State == PreparationRetired || r.State == PreparationRetiring {
		return nil, ErrConflict
	}
	m.record.CleanupFrom = r.State
	m.record.State = PreparationRetiring
	m.record.Generation++
	m.record.RecordedBy = actor
	return m.GetPreparation(ctx, org, id)
}
func (m *preparationRetirementMemory) CapturePreparationOwnership(ctx context.Context, org, id string, expected int64, account, pod, actor string, guard func(context.Context) error) (*Preparation, error) {
	if err := guard(ctx); err != nil {
		return nil, err
	}
	r, err := m.GetPreparation(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if r.Generation != expected || r.State != PreparationRetiring {
		return nil, ErrConflict
	}
	if account != "" {
		m.record.AccountUID = account
	}
	if pod != "" {
		m.record.PodUID = pod
	}
	m.record.Generation++
	m.record.RecordedBy = actor
	return m.GetPreparation(ctx, org, id)
}
func (m *preparationRetirementMemory) CompletePreparationRetirement(ctx context.Context, org, id string, expected int64, actor string, guard func(context.Context) error) (*Preparation, error) {
	if err := guard(ctx); err != nil {
		return nil, err
	}
	r, err := m.GetPreparation(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if r.Generation != expected || r.State != PreparationRetiring {
		return nil, ErrConflict
	}
	m.record.State = PreparationRetired
	m.record.Generation++
	m.record.RecordedBy = actor
	return m.GetPreparation(ctx, org, id)
}
func preparationRetirementFixture(t *testing.T) (*PreparationRetirementService, *preparationRetirementMemory, *fake.Clientset, security.Principal, string) {
	t.Helper()
	launch, _, installations, p, in, _ := launchServiceFixture(t)
	d := installations.destinations.rows[in.DestinationID]
	record := &Preparation{Scope: d.Scope, PreparationIntent: PreparationIntent{ID: in.AttemptID, Namespace: "workloads", NamespaceUID: d.Identity.NamespaceUID, Image: launch.cfg.AgentImage}, DestinationID: d.ID, IdentityRevision: d.Identity.Revision(), State: PreparationPrepared, Generation: 5, AccountUID: "sa-uid", PodUID: installations.record.Profile.PodUID}
	memory := &preparationRetirementMemory{&preparationMemory{record: record}}
	plan, err := agentinstall.NewKubernetesPlan(record.Namespace, record.ID, record.Image, false)
	if err != nil {
		t.Fatal(err)
	}
	sa, pod, err := plan.PreparationResources(record.NamespaceUID)
	if err != nil {
		t.Fatal(err)
	}
	sa.UID = types.UID(record.AccountUID)
	sa.ResourceVersion = "1"
	pod.UID = types.UID(record.PodUID)
	pod.ResourceVersion = "1"
	client := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: record.Namespace, UID: types.UID(record.NamespaceUID)}}, sa, pod)
	prep := NewPreparationService(launch, memory)
	prep.open = func(context.Context, domain.Destination) (kubernetes.Interface, string, error) {
		return client, record.Namespace, nil
	}
	retirement := NewLaunchRetirementService(launch, func(context.Context, string, string) error { return nil })
	service := NewPreparationRetirementService(prep, retirement, func(context.Context, string, string, string) error { return nil })
	return service, memory, client, p, d.ID
}
func TestPreparationRetirementResumesAfterRevocationFailure(t *testing.T) {
	service, memory, client, p, destination := preparationRetirementFixture(t)
	ctx := t.Context()
	failed := true
	revokes := 0
	service.revoke = func(_ context.Context, org, dest, id string) error {
		revokes++
		if org != p.OrgID || dest != destination || id != memory.record.ID || memory.record.State != PreparationRetiring {
			t.Fatal("unscoped or premature revoke")
		}
		if failed {
			return errors.New("SPIRE unavailable")
		}
		return nil
	}
	r, err := service.Retire(ctx, p, destination, memory.record.ID)
	if err == nil || r == nil || r.State != PreparationRetiring {
		t.Fatal("failed revocation discarded record", r, err)
	}
	if len(client.Actions()) != 0 {
		t.Fatal("provider contacted before successful revocation")
	}
	failed = false
	service = NewPreparationRetirementService(service.preparation, service.launch, service.revoke)
	r, err = service.Retire(ctx, p, destination, memory.record.ID)
	if err != nil || r.State != PreparationRetired {
		t.Fatal("retry failed", err)
	}
	before := len(client.Actions())
	if _, err = service.Retire(ctx, p, destination, memory.record.ID); err != nil || revokes != 2 || len(client.Actions()) != before {
		t.Fatal("completed cleanup repeated", err)
	}
}
func TestPreparationRetirementChecksAuthorityBeforeEffects(t *testing.T) {
	for _, mode := range []string{"read only", "foreign destination", "foreign organization", "changed identity", "revoked after revoke", "new generation"} {
		t.Run(mode, func(t *testing.T) {
			service, memory, client, p, destination := preparationRetirementFixture(t)
			svc := service.preparation.launch.installations.destinations
			calls := 0
			service.revoke = func(context.Context, string, string, string) error {
				calls++
				if mode == "revoked after revoke" {
					svc.authz = &authorizer{deny: true}
				}
				return nil
			}
			switch mode {
			case "read only":
				p.ReadOnly = true
			case "foreign destination":
				destination = "other"
			case "foreign organization":
				p.OrgID = "other"
			case "changed identity", "new generation":
				store, ok := svc.store.(*memoryStore)
				if !ok {
					t.Fatal("fixture store changed")
				}
				d := store.rows[destination]
				d.Generation++
				if mode == "changed identity" {
					d.Identity.NamespaceUID = "other"
				}
				store.rows[destination] = d
			}
			result, err := service.Retire(t.Context(), p, destination, memory.record.ID)
			if mode == "new generation" {
				if err != nil || result.State != PreparationRetired {
					t.Fatal("new generation blocked cleanup", err)
				}
				return
			}
			if err == nil || memory.record.State == PreparationRetired {
				t.Fatal("denied cleanup completed", err)
			}
			if len(client.Actions()) != 0 {
				t.Fatal("denied provider effects")
			}
			if mode != "revoked after revoke" && calls != 0 {
				t.Fatal("unauthorized revocation")
			}
		})
	}
}

func TestPreparationRetirementRunsLaunchCleanupBeforeAccount(t *testing.T) {
	service, memory, client, p, destination := preparationRetirementFixture(t)
	ctx := t.Context()
	d, err := service.preparation.launch.installations.destinations.store.Get(ctx, p.OrgID, destination)
	if err != nil {
		t.Fatal(err)
	}
	allow := func(context.Context) error { return nil }
	in := LaunchIntent{ID: memory.record.ID, EnrollmentID: "enrollment", Namespace: memory.record.Namespace, PodUID: memory.record.PodUID, ConfigMapName: "loomplane-agent-" + memory.record.ID, Digest: strings.Repeat("a", 64)}
	if _, err = service.launch.store.ReserveLaunch(ctx, *d, in, p.UserID, allow); err != nil {
		t.Fatal(err)
	}
	scoped := false
	service.revoke = func(context.Context, string, string, string) error { scoped = true; return nil }
	failed := true
	service.launch.cleanup = func(context.Context, domain.Destination, *LaunchAttempt, func(context.Context) error) error {
		if !scoped {
			t.Fatal("launch cleanup before scoped revoke")
		}
		if failed {
			return errors.New("launch cleanup unavailable")
		}
		return nil
	}
	if _, err = service.Retire(ctx, p, destination, memory.record.ID); err == nil {
		t.Fatal("failed launch released preparation")
	}
	if _, err = client.CoreV1().ServiceAccounts(memory.record.Namespace).Get(ctx, in.ConfigMapName, metav1.GetOptions{}); err != nil {
		t.Fatal("account removed before launch cleanup", err)
	}
	failed = false
	result, err := service.Retire(ctx, p, destination, memory.record.ID)
	if err != nil || result.State != PreparationRetired {
		t.Fatal("launch retry failed", err)
	}
	launch, err := service.launch.store.GetLaunch(ctx, p.OrgID, memory.record.ID)
	if err != nil || launch.State != LaunchRetired {
		t.Fatal("launch not retired", err)
	}
}

func TestPreparationRetirementPersistsRecoveredPod(t *testing.T) {
	service, memory, client, p, destination := preparationRetirementFixture(t)
	uid := memory.record.PodUID
	memory.record.State = PreparationPodPending
	memory.record.PodUID = ""
	memory.record.Generation = 4
	client.PrependReactor("delete", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		if memory.record.PodUID != uid || memory.record.State != PreparationRetiring {
			t.Fatal("pod deleted before ownership save")
		}
		return false, nil, nil
	})
	result, err := service.Retire(t.Context(), p, destination, memory.record.ID)
	if err != nil || result.State != PreparationRetired || result.PodUID != uid {
		t.Fatal("recovery cleanup failed", err)
	}
}
