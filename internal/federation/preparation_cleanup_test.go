package federation

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/xraph/loomplane/internal/agentinstall"
)

func preparationCleanupFixture(t *testing.T) (*fake.Clientset, *Preparation) {
	t.Helper()
	launch, _, installations, _, in, _ := launchServiceFixture(t)
	d := installations.destinations.rows[in.DestinationID]
	r := &Preparation{Scope: d.Scope, PreparationIntent: PreparationIntent{ID: in.AttemptID, Namespace: "workloads", NamespaceUID: d.Identity.NamespaceUID, Image: launch.cfg.AgentImage}, DestinationID: d.ID, IdentityRevision: d.Identity.Revision(), State: PreparationRetiring, CleanupFrom: PreparationPrepared, Generation: 6, AccountUID: "sa-uid", PodUID: "pod-uid"}
	plan, err := agentinstall.NewKubernetesPlan(r.Namespace, r.ID, r.Image, false)
	if err != nil {
		t.Fatal(err)
	}
	sa, pod, err := plan.PreparationResources(r.NamespaceUID)
	if err != nil {
		t.Fatal(err)
	}
	sa.UID = types.UID(r.AccountUID)
	sa.ResourceVersion = "1"
	pod.UID = types.UID(r.PodUID)
	pod.ResourceVersion = "1"
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: r.Namespace, UID: types.UID(r.NamespaceUID)}}
	return fake.NewClientset(ns, sa, pod), r
}

func TestPreparationCleanupPinsOwnershipBeforeDeleting(t *testing.T) {
	for _, mode := range []string{"known", "unknown pod", "unknown account", "lost delete", "replacement account", "replacement pod"} {
		t.Run(mode, func(t *testing.T) {
			client, r := preparationCleanupFixture(t)
			ctx := t.Context()
			name := "loomplane-agent-" + r.ID
			switch mode {
			case "unknown pod":
				r.CleanupFrom = PreparationPodPending
				r.PodUID = ""
			case "unknown account":
				r.CleanupFrom = PreparationAccountPending
				r.PodUID = ""
				r.AccountUID = ""
				if err := client.CoreV1().Pods(r.Namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			case "replacement account":
				sa, err := client.CoreV1().ServiceAccounts(r.Namespace).Get(ctx, name, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				sa.UID = "replacement"
				if _, err = client.CoreV1().ServiceAccounts(r.Namespace).Update(ctx, sa, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "replacement pod":
				pod, err := client.CoreV1().Pods(r.Namespace).Get(ctx, name, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				pod.UID = "replacement"
				pod.Spec.ServiceAccountName = "different-account"
				if _, err = client.CoreV1().Pods(r.Namespace).Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			client.ClearActions()
			saved := *r
			capture := func(_ context.Context, account, pod string) (*Preparation, error) {
				if account != "" {
					saved.AccountUID = account
				}
				if pod != "" {
					saved.PodUID = pod
				}
				saved.Generation++
				next := saved
				return &next, nil
			}
			lost := mode == "lost delete"
			client.PrependReactor("delete", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
				deletion, ok := action.(ktesting.DeleteAction)
				if !ok {
					t.Fatal("not delete")
				}
				pre := deletion.GetDeleteOptions().Preconditions
				uid := saved.AccountUID
				if action.GetResource().Resource == "pods" {
					uid = saved.PodUID
				}
				if uid == "" || pre == nil || pre.UID == nil || string(*pre.UID) != uid || pre.ResourceVersion == nil || *pre.ResourceVersion != "1" {
					t.Fatal("delete without persisted ownership")
				}
				if lost {
					lost = false
					if err := client.Tracker().Delete(action.GetResource(), action.GetNamespace(), deletion.GetName()); err != nil {
						t.Fatal(err)
					}
					return true, nil, errors.New("delete response lost")
				}
				return false, nil, nil
			})
			result, err := cleanupPreparationResources(ctx, client, r, capture, func(context.Context) error { return nil })
			if mode == "lost delete" {
				if err == nil {
					t.Fatal("lost response completed cleanup")
				}
				result, err = cleanupPreparationResources(ctx, client, result, capture, func(context.Context) error { return nil })
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "replacement account" {
				sa, readErr := client.CoreV1().ServiceAccounts(r.Namespace).Get(ctx, name, metav1.GetOptions{})
				if readErr != nil || sa.UID != "replacement" {
					t.Fatal("replacement account deleted", readErr)
				}
			} else if _, readErr := client.CoreV1().ServiceAccounts(r.Namespace).Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(readErr) {
				t.Fatal("owned account retained", readErr)
			}
			if mode == "replacement pod" {
				pod, readErr := client.CoreV1().Pods(r.Namespace).Get(ctx, name, metav1.GetOptions{})
				if readErr != nil || pod.UID != "replacement" {
					t.Fatal("replacement pod deleted", readErr)
				}
			}
			if result.AccountUID != saved.AccountUID || result.PodUID != saved.PodUID {
				t.Fatal("capture lost")
			}
		})
	}
}

func TestPreparationCleanupRetainsUnresolvedResources(t *testing.T) {
	for _, mode := range []string{"missing unknown account", "missing unknown pod", "foreign unknown pod", "foreign namespace", "unknown config", "account in use", "finalizer", "permission revoked", "capture failed"} {
		t.Run(mode, func(t *testing.T) {
			client, r := preparationCleanupFixture(t)
			ctx := t.Context()
			name := "loomplane-agent-" + r.ID
			switch mode {
			case "missing unknown account":
				r.CleanupFrom = PreparationAccountPending
				r.AccountUID = ""
				r.PodUID = ""
				if err := client.CoreV1().ServiceAccounts(r.Namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			case "missing unknown pod":
				r.CleanupFrom = PreparationPodPending
				r.PodUID = ""
				if err := client.CoreV1().Pods(r.Namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			case "foreign unknown pod":
				r.CleanupFrom = PreparationPodPending
				r.PodUID = ""
				pod, err := client.CoreV1().Pods(r.Namespace).Get(ctx, name, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				pod.Annotations = nil
				if _, err = client.CoreV1().Pods(r.Namespace).Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "foreign namespace":
				r.NamespaceUID = "foreign"
			case "unknown config":
				if _, err := client.CoreV1().ConfigMaps(r.Namespace).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name}}, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "account in use":
				if _, err := client.CoreV1().Pods(r.Namespace).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "other", UID: "other"}, Spec: corev1.PodSpec{ServiceAccountName: name}}, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "capture failed":
				r.CleanupFrom = PreparationPodPending
				r.PodUID = ""
			}
			client.ClearActions()
			client.PrependReactor("delete", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
				if mode == "finalizer" && action.GetResource().Resource == "pods" {
					return true, nil, nil
				}
				return false, nil, nil
			})
			capture := func(context.Context, string, string) (*Preparation, error) { return nil, errors.New("save failed") }
			guard := func(context.Context) error {
				if mode == "permission revoked" {
					return errors.New("permission revoked")
				}
				return nil
			}
			if _, err := cleanupPreparationResources(ctx, client, r, capture, guard); err == nil {
				t.Fatal("unresolved cleanup completed")
			}
			for _, action := range client.Actions() {
				if action.GetVerb() == "delete" && (mode != "account in use" && mode != "finalizer" || action.GetResource().Resource == "serviceaccounts") {
					t.Fatal("unsafe delete", mode, action)
				}
			}
		})
	}
}
