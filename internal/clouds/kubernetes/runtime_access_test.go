package kubernetes

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func runtimeAccessFixture(t *testing.T) (*fake.Clientset, provider.RuntimeAccessInput, []rbacv1.PolicyRule) {
	t.Helper()
	disabled := false
	labels := map[string]string{enrollmentLabel: "attachment", projectLabel: "project"}
	in := provider.RuntimeAccessInput{AttachmentID: "attachment", ProjectID: "project", Namespace: "apps", NamespaceUID: "ns", ServiceAccountName: "runtime", ServiceAccountUID: "sa", RoleName: "runtime", RoleUID: "role", RoleResourceVersion: "1", RoleBindingName: "runtime", RoleBindingUID: "binding", RoleBindingResourceVersion: "1", Profile: provider.RuntimeAccessProfile{SchemaVersion: 1, CleanupDependents: true}}
	request := EnrollmentRequest{ServiceAccountName: "runtime"}
	old, err := EnrollmentRoleRules(request)
	if err != nil {
		t.Fatal(err)
	}
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "runtime", Namespace: "apps", UID: "role", ResourceVersion: "1", Labels: labels}, Rules: old}
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "runtime", Namespace: "apps", UID: "binding", ResourceVersion: "1", Labels: labels}, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "runtime"}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "runtime", Namespace: "apps"}}}
	in.RoleRulesDigest = RuntimeAccessDigest(old)
	in.RoleBindingSubjectsRefDigest = RuntimeBindingDigest(binding)
	request.RuntimeAccess = &in.Profile
	desired, err := EnrollmentRoleRules(request)
	if err != nil {
		t.Fatal(err)
	}
	return fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps", UID: "ns"}}, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "runtime", Namespace: "apps", UID: "sa"}, AutomountServiceAccountToken: &disabled}, role, binding), in, desired
}

func TestRuntimeAccessObservedRecovery(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "lost-response"}[lost], func(t *testing.T) {
			k, in, rules := runtimeAccessFixture(t)
			writes := 0
			k.PrependReactor("update", "roles", func(action ktesting.Action) (bool, runtime.Object, error) {
				writes++
				role := action.(ktesting.UpdateAction).GetObject().(*rbacv1.Role).DeepCopy()
				if role.ResourceVersion != "1" || role.UID != "role" {
					t.Fatal("conditional identity dropped")
				}
				role.ResourceVersion = "2"
				if err := k.Tracker().Update(rbacv1.SchemeGroupVersion.WithResource("roles"), role, "apps"); err != nil {
					return true, nil, err
				}
				if lost {
					return true, nil, errors.New("lost response")
				}
				return true, role, nil
			})
			state := &RuntimeAccessState{}
			check := func(context.Context) error { return nil }
			err := ReconcileRuntimeAccess(t.Context(), k, in, rules, "operation", "digest", state, check)
			if lost && err == nil {
				t.Fatal("lost response accepted")
			}
			if !lost && err != nil {
				t.Fatal(err)
			}
			if err = ReconcileRuntimeAccess(t.Context(), k, in, rules, "operation", "digest", state, check); err != nil {
				t.Fatal(err)
			}
			if writes != 1 || state.Receipt == nil || state.Receipt.ResourceVersion != "2" {
				t.Fatalf("recovery: %+v writes=%d", state, writes)
			}
			if lost && (state.NetworkOutcome != "unknown" || state.Receipt.Kind != "observed-role-update") {
				t.Fatal("network history fabricated")
			}
		})
	}
}

func TestRuntimeAccessRecoveryRejectsUnprovedEffect(t *testing.T) {
	for _, name := range []string{"uid", "old-version", "marker", "digest", "rules", "binding", "namespace", "revoked"} {
		t.Run(name, func(t *testing.T) {
			k, in, rules := runtimeAccessFixture(t)
			role, err := k.RbacV1().Roles("apps").Get(t.Context(), "runtime", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			role.ResourceVersion = "2"
			role.Rules = rules
			role.Annotations = map[string]string{RuntimeAccessOperationAnnotation: "operation", RuntimeAccessDigestAnnotation: "digest"}
			switch name {
			case "uid":
				role.UID = "replacement"
			case "old-version":
				role.ResourceVersion = "1"
			case "marker":
				delete(role.Annotations, RuntimeAccessOperationAnnotation)
			case "digest":
				role.Annotations[RuntimeAccessDigestAnnotation] = "foreign"
			case "rules":
				role.Rules = RuntimeRoleRules()
			case "binding":
				in.RoleBindingUID = "different"
			case "namespace":
				in.NamespaceUID = "different"
			}
			if err = k.Tracker().Update(rbacv1.SchemeGroupVersion.WithResource("roles"), role, "apps"); err != nil {
				t.Fatal(err)
			}
			state := &RuntimeAccessState{Attempted: true, NetworkOutcome: "unknown"}
			err = ReconcileRuntimeAccess(t.Context(), k, in, rules, "operation", "digest", state, func(context.Context) error {
				if name == "revoked" {
					return errors.New("revoked")
				}
				return nil
			})
			if err == nil || state.Receipt != nil {
				t.Fatal("unproved role published")
			}
			for _, action := range k.Actions() {
				if action.GetVerb() == "update" {
					t.Fatal("unknown update resent")
				}
			}
		})
	}
}

func TestRuntimeAccessProfileCompatibility(t *testing.T) {
	request := EnrollmentRequest{ServiceAccountName: "runtime"}
	legacy, err := EnrollmentRoleRules(request)
	if err != nil || !reflect.DeepEqual(legacy[:3], RuntimeRoleRules()) {
		t.Fatal("legacy rules changed")
	}
	request.RuntimeAccess = &provider.RuntimeAccessProfile{SchemaVersion: 1, HPA: true}
	if _, err = EnrollmentRoleRules(request); err == nil {
		t.Fatal("empty cleanup profile accepted")
	}
	request.RuntimeAccess.CleanupDependents = true
	rules, err := EnrollmentRoleRules(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range rules[len(legacy):] {
		for _, verb := range rule.Verbs {
			if verb == "patch" || len(rule.APIGroups) == 0 || rule.Resources[0] == "horizontalpodautoscalers" && verb == "watch" {
				t.Fatal("unreviewed verb")
			}
		}
	}
}

func TestRuntimeAccessDependencyReplacementAfterEffect(t *testing.T) {
	for _, kind := range []string{"serviceaccount", "rolebinding", "namespace"} {
		t.Run(kind, func(t *testing.T) {
			k, in, rules := runtimeAccessFixture(t)
			writes := 0
			k.PrependReactor("update", "roles", func(action ktesting.Action) (bool, runtime.Object, error) {
				writes++
				role := action.(ktesting.UpdateAction).GetObject().(*rbacv1.Role).DeepCopy()
				role.ResourceVersion = "2"
				if err := k.Tracker().Update(rbacv1.SchemeGroupVersion.WithResource("roles"), role, "apps"); err != nil {
					return true, nil, err
				}
				switch kind {
				case "serviceaccount":
					value, err := k.Tracker().Get(corev1.SchemeGroupVersion.WithResource("serviceaccounts"), "apps", "runtime")
					if err != nil {
						return true, nil, err
					}
					object := value.(*corev1.ServiceAccount).DeepCopy()
					object.UID = "new-sa"
					if err = k.Tracker().Update(corev1.SchemeGroupVersion.WithResource("serviceaccounts"), object, "apps"); err != nil {
						return true, nil, err
					}
				case "rolebinding":
					value, err := k.Tracker().Get(rbacv1.SchemeGroupVersion.WithResource("rolebindings"), "apps", "runtime")
					if err != nil {
						return true, nil, err
					}
					object := value.(*rbacv1.RoleBinding).DeepCopy()
					object.UID = "new-binding"
					if err = k.Tracker().Update(rbacv1.SchemeGroupVersion.WithResource("rolebindings"), object, "apps"); err != nil {
						return true, nil, err
					}
				case "namespace":
					value, err := k.Tracker().Get(corev1.SchemeGroupVersion.WithResource("namespaces"), "", "apps")
					if err != nil {
						return true, nil, err
					}
					object := value.(*corev1.Namespace).DeepCopy()
					object.UID = "new-ns"
					if err = k.Tracker().Update(corev1.SchemeGroupVersion.WithResource("namespaces"), object, ""); err != nil {
						return true, nil, err
					}
				}
				return true, role, nil
			})
			state := &RuntimeAccessState{}
			checkpoint := func(context.Context) error { return nil }
			for range 2 {
				if err := ReconcileRuntimeAccess(t.Context(), k, in, rules, "operation", "digest", state, checkpoint); err == nil {
					t.Fatal("changed dependent accepted")
				}
			}
			if writes != 1 || state.Receipt != nil || state.NetworkOutcome != "accepted" {
				t.Fatalf("effect history: %+v writes=%d", state, writes)
			}
		})
	}
}
