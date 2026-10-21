package federation

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	domain "github.com/xraph/loomplane/pkg/federation"
)

type preparationMemory struct{ record *Preparation }

func (m *preparationMemory) ReservePreparation(ctx context.Context, d domain.Destination, in PreparationIntent, actor string, guard func(context.Context) error) (*Preparation, error) {
	if err := guard(ctx); err != nil {
		return nil, err
	}
	if m.record != nil {
		return nil, ErrConflict
	}
	m.record = &Preparation{Scope: d.Scope, PreparationIntent: in, DestinationID: d.ID, DestinationGeneration: d.Generation, IdentityRevision: d.Identity.Revision(), State: PreparationReserved, Generation: 1, RecordedBy: actor}
	return m.GetPreparation(ctx, d.OrgID, in.ID)
}
func (m *preparationMemory) GetPreparation(_ context.Context, org, id string) (*Preparation, error) {
	if m.record == nil || m.record.OrgID != org || m.record.ID != id {
		return nil, ErrNotFound
	}
	r := *m.record
	return &r, nil
}
func (m *preparationMemory) CurrentPreparation(ctx context.Context, org, id string) (*Preparation, error) {
	if m.record == nil || m.record.DestinationID != id {
		return nil, ErrNotFound
	}
	return m.GetPreparation(ctx, org, m.record.ID)
}
func (m *preparationMemory) AdvancePreparation(ctx context.Context, org, id string, expected int64, target, uid, actor string, guard func(context.Context) error) (*Preparation, error) {
	if err := guard(ctx); err != nil {
		return nil, err
	}
	r, err := m.GetPreparation(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if r.Generation != expected || !preparationTransition(r.State, target, uid) {
		return nil, ErrConflict
	}
	m.record.State = target
	m.record.Generation++
	m.record.RecordedBy = actor
	if target == PreparationAccountCreated {
		m.record.AccountUID = uid
	}
	if target == PreparationPrepared {
		m.record.PodUID = uid
	}
	return m.GetPreparation(ctx, org, id)
}
func TestPreparationCreatesOnceAndRecoversLostResponse(t *testing.T) {
	for _, lost := range []string{"none", "serviceaccounts", "pods"} {
		t.Run(lost, func(t *testing.T) {
			launch, _, installations, p, in, _ := launchServiceFixture(t)
			d := installations.destinations.rows[in.DestinationID]
			ledger := &preparationMemory{}
			client := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "workloads", UID: types.UID(d.Identity.NamespaceUID)}})
			creates := map[string]int{}
			client.PrependReactor("create", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
				create, ok := action.(ktesting.CreateAction)
				if !ok {
					t.Fatal("not a create")
				}
				resource := action.GetResource().Resource
				creates[resource]++
				if ledger.record == nil {
					t.Fatal("create without durable intent")
				}
				want := PreparationAccountPending
				if resource == "pods" {
					want = PreparationPodPending
				}
				if ledger.record.State != want {
					t.Fatal("creation phase not persisted")
				}
				meta, ok := create.GetObject().(metav1.Object)
				if !ok {
					t.Fatal("missing metadata")
				}
				meta.SetUID(types.UID(resource + "-uid"))
				meta.SetResourceVersion("1")
				if resource == lost && creates[resource] == 1 {
					if err := client.Tracker().Create(action.GetResource(), create.GetObject(), action.GetNamespace()); err != nil {
						t.Fatal(err)
					}
					return true, nil, errors.New("create response lost")
				}
				return false, nil, nil
			})
			service := NewPreparationService(launch, ledger)
			service.open = func(context.Context, domain.Destination) (kubernetes.Interface, string, error) {
				return client, "workloads", nil
			}
			r, err := service.Start(t.Context(), p, d.ID)
			if lost != "none" {
				if err == nil || r == nil {
					t.Fatal("unknown create did not retain attempt", err)
				}
				service = NewPreparationService(launch, ledger)
				service.open = func(context.Context, domain.Destination) (kubernetes.Interface, string, error) {
					return client, "workloads", nil
				}
				r, err = service.Resume(t.Context(), p, d.ID, r.ID)
			}
			if err != nil || r.State != PreparationPrepared || r.AccountUID != "serviceaccounts-uid" || r.PodUID != "pods-uid" {
				t.Fatal("resource preparation failed", err, r)
			}
			if creates["pods"] != 1 || creates["serviceaccounts"] != 1 {
				t.Fatal("create replayed", creates)
			}
			if _, err = service.Start(t.Context(), p, d.ID); !errors.Is(err, ErrConflict) {
				t.Fatal("duplicate preparation", err)
			}
			if _, err = service.Resume(t.Context(), p, d.ID, r.ID); err != nil || creates["pods"] != 1 {
				t.Fatal("prepared attempt replayed", err)
			}
		})
	}
}

func TestPreparationRetainsUncertainMissingOrForeignAccount(t *testing.T) {
	for _, mode := range []string{"missing", "foreign account", "permission", "namespace"} {
		t.Run(mode, func(t *testing.T) {
			launch, _, installations, p, in, _ := launchServiceFixture(t)
			d := installations.destinations.rows[in.DestinationID]
			ledger := &preparationMemory{}
			client := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "workloads", UID: types.UID(d.Identity.NamespaceUID)}})
			writes := 0
			client.PrependReactor("create", "serviceaccounts", func(ktesting.Action) (bool, runtime.Object, error) {
				writes++
				return true, nil, errors.New("outcome unknown")
			})
			svc := NewPreparationService(launch, ledger)
			svc.open = func(context.Context, domain.Destination) (kubernetes.Interface, string, error) {
				return client, "workloads", nil
			}
			r, err := svc.Start(t.Context(), p, d.ID)
			if err == nil || r == nil || r.State != PreparationAccountPending {
				t.Fatal("ambiguous create not retained", err)
			}
			client.ClearActions()
			switch mode {
			case "foreign account":
				if err = client.Tracker().Add(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "loomplane-agent-" + r.ID, Namespace: r.Namespace, UID: "foreign"}}); err != nil {
					t.Fatal(err)
				}
			case "permission":
				launch.installations.destinations.authz = &authorizer{deny: true}
			case "namespace":
				ns, readErr := client.CoreV1().Namespaces().Get(t.Context(), r.Namespace, metav1.GetOptions{})
				if readErr != nil {
					t.Fatal(readErr)
				}
				ns.UID = "foreign"
				if _, err = client.CoreV1().Namespaces().Update(t.Context(), ns, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = svc.Resume(t.Context(), p, d.ID, r.ID); err == nil || writes != 1 || ledger.record.State != PreparationAccountPending {
				t.Fatal("uncertain create retried or foreign resource adopted", err, writes)
			}
			for _, action := range client.Actions() {
				if action.GetVerb() == "create" {
					t.Fatal("recovery repeated provider write")
				}
			}
		})
	}
}
func TestPreparationStopsAfterAuthorityChangesDuringCreation(t *testing.T) {
	for _, mode := range []string{"permission", "destination generation", "replaced account"} {
		t.Run(mode, func(t *testing.T) {
			launch, _, installations, p, in, _ := launchServiceFixture(t)
			d := installations.destinations.rows[in.DestinationID]
			ledger := &preparationMemory{}
			client := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "workloads", UID: types.UID(d.Identity.NamespaceUID)}})
			podWrites := 0
			client.PrependReactor("create", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
				create, ok := action.(ktesting.CreateAction)
				if !ok {
					t.Fatal("unexpected action")
				}
				meta, ok := create.GetObject().(metav1.Object)
				if !ok {
					t.Fatal("missing metadata")
				}
				meta.SetUID(types.UID(action.GetResource().Resource + "-uid"))
				if action.GetResource().Resource == "serviceaccounts" {
					if mode == "permission" {
						launch.installations.destinations.authz = &authorizer{deny: true}
					}
					if mode == "destination generation" {
						current := installations.destinations.rows[d.ID]
						current.Generation++
						installations.destinations.rows[d.ID] = current
					}
				} else {
					podWrites++
					if mode == "replaced account" {
						object, err := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("serviceaccounts"), meta.GetNamespace(), meta.GetName())
						if err != nil {
							t.Fatal(err)
						}
						sa, ok := object.(*corev1.ServiceAccount)
						if !ok {
							t.Fatal("unexpected account")
						}
						sa.UID = "replacement"
						if err = client.Tracker().Update(corev1.SchemeGroupVersion.WithResource("serviceaccounts"), sa, meta.GetNamespace()); err != nil {
							t.Fatal(err)
						}
					}
				}
				return false, nil, nil
			})
			svc := NewPreparationService(launch, ledger)
			svc.open = func(context.Context, domain.Destination) (kubernetes.Interface, string, error) {
				return client, "workloads", nil
			}
			r, err := svc.Start(t.Context(), p, d.ID)
			if err == nil || r == nil || r.State == PreparationPrepared {
				t.Fatal("changed authority prepared resources", err)
			}
			if mode != "replaced account" && podWrites != 0 {
				t.Fatal("stale authority created pod")
			}
		})
	}
}
