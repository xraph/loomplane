package federation

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/xraph/loomplane/internal/agentinstall"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func cleanupFixture(t *testing.T) (*fake.Clientset, domain.Destination, *LaunchAttempt) {
	t.Helper()
	launch, _, installations, p, in, _ := launchServiceFixture(t)
	var cm *corev1.ConfigMap
	launch.deliver = func(_ context.Context, plan *agentinstall.KubernetesPlan, _ domain.Destination, r workloadidentity.EnrollmentRecord, s agentinstall.LaunchSettings, _ func(context.Context) error) (*corev1.ConfigMap, error) {
		var err error
		cm, err = plan.DesiredConfigMap(r, s)
		if err != nil {
			return nil, err
		}
		cm.UID = "cm-uid"
		cm.ResourceVersion = "1"
		return cm, nil
	}
	if _, err := launch.Deliver(t.Context(), p, in); err != nil {
		t.Fatal(err)
	}
	ledger, ok := launch.attempts.(*launchMemory)
	if !ok {
		t.Fatal("unexpected fixture")
	}
	attempt, err := ledger.BeginLaunchRetirement(t.Context(), p.OrgID, in.AttemptID, 2, p.UserID, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	d := installations.destinations.rows[in.DestinationID]
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: attempt.ConfigMapName, Namespace: attempt.Namespace, UID: types.UID(attempt.PodUID), ResourceVersion: "1"}}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: attempt.Namespace, UID: types.UID(d.Identity.NamespaceUID)}}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: attempt.ConfigMapName, Namespace: attempt.Namespace, UID: "sa-uid"}}
	return fake.NewClientset(ns, pod, cm, sa), d, attempt
}
func TestLaunchCleanupPinsDeleteUIDsAndPreservesReplacement(t *testing.T) {
	for _, mode := range []string{"owned", "replacement pod", "already absent", "pod terminating", "delete lost response"} {
		t.Run(mode, func(t *testing.T) {
			client, d, a := cleanupFixture(t)
			pods := client.CoreV1().Pods(a.Namespace)
			if mode == "replacement pod" {
				pod, err := pods.Get(t.Context(), a.ConfigMapName, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				pod.UID = "replacement"
				if _, err = pods.Update(t.Context(), pod, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "already absent" {
				if err := pods.Delete(t.Context(), a.ConfigMapName, metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
				if err := client.CoreV1().ConfigMaps(a.Namespace).Delete(t.Context(), a.ConfigMapName, metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			deletes := 0
			client.PrependReactor("delete", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
				deletion, ok := action.(ktesting.DeleteAction)
				if !ok {
					t.Fatal("not a delete")
				}
				options := deletion.GetDeleteOptions()
				if options.Preconditions == nil || options.Preconditions.UID == nil || options.Preconditions.ResourceVersion == nil {
					t.Fatal("unconditional cleanup delete")
				}
				expected := a.PodUID
				if action.GetResource().Resource == "configmaps" {
					expected = a.ConfigMapUID
				}
				if string(*options.Preconditions.UID) != expected {
					t.Fatal("delete targeted unrelated UID")
				}
				deletes++
				if mode == "pod terminating" && action.GetResource().Resource == "pods" {
					return true, nil, nil
				}
				if mode == "delete lost response" && action.GetResource().Resource == "pods" {
					if err := client.Tracker().Delete(action.GetResource(), a.Namespace, a.ConfigMapName); err != nil {
						t.Fatal(err)
					}
					return true, nil, errors.New("response lost")
				}
				return false, nil, nil
			})
			allow := func(context.Context) error { return nil }
			err := cleanupLaunchResources(t.Context(), client, d, a, allow)
			if mode == "pod terminating" {
				if !errors.Is(err, ErrInUse) {
					t.Fatal("terminating pod released", err)
				}
				return
			}
			if mode == "delete lost response" {
				if err == nil {
					t.Fatal("lost delete response claimed complete")
				}
				err = cleanupLaunchResources(t.Context(), client, d, a, allow)
			}
			if err != nil {
				t.Fatal(err)
			}
			expected := 2
			if mode == "already absent" {
				expected = 0
			}
			if mode == "replacement pod" {
				expected = 1
				pod, readErr := pods.Get(t.Context(), a.ConfigMapName, metav1.GetOptions{})
				if readErr != nil || pod.UID != "replacement" {
					t.Fatal("replacement was deleted", readErr)
				}
			}
			if deletes != expected {
				t.Fatal("unexpected deletes", deletes, expected)
			}
			if _, err = client.CoreV1().ServiceAccounts(a.Namespace).Get(t.Context(), a.ConfigMapName, metav1.GetOptions{}); err != nil {
				t.Fatal("unrecorded service account deleted", err)
			}
		})
	}
}
func TestLaunchCleanupRejectsForeignEvidenceAndRevokedAuthority(t *testing.T) {
	for _, mode := range []string{"namespace", "payload", "owner", "config UID", "permission"} {
		t.Run(mode, func(t *testing.T) {
			client, d, a := cleanupFixture(t)
			switch mode {
			case "namespace":
				d.Identity.NamespaceUID = "foreign"
			case "payload", "owner", "config UID":
				cm, err := client.CoreV1().ConfigMaps(a.Namespace).Get(t.Context(), a.ConfigMapName, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "payload":
					cm.Data[agentinstall.ConfigKey] = "{}"
				case "owner":
					cm.OwnerReferences[0].UID = "foreign"
				case "config UID":
					cm.UID = "foreign"
				}
				if _, err = client.CoreV1().ConfigMaps(a.Namespace).Update(t.Context(), cm, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			checks := 0
			guard := func(context.Context) error {
				checks++
				if mode == "permission" && checks > 1 {
					return security.ErrForbidden
				}
				return nil
			}
			if err := cleanupLaunchResources(t.Context(), client, d, a, guard); err == nil {
				t.Fatal("unverified cleanup succeeded")
			}
			for _, action := range client.Actions() {
				if action.GetVerb() == "delete" {
					t.Fatal("unsafe delete", action)
				}
			}
		})
	}
}
