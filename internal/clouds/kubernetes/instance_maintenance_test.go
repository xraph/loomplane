package kubernetes

import (
	"context"
	"errors"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
)

func maintenanceEnrollment(t *testing.T) (*fake.Clientset, EnrollmentRequest, *EnrollmentState, time.Time) {
	t.Helper()
	k, _, state := enrollmentFixture()
	in := observationEnrollment(t, "kubernetes-instance-observation-v1")
	if err := EnrollRuntime(t.Context(), k, in, state, func(context.Context, *EnrollmentState) error { return nil }); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	k.PrependReactor("create", "serviceaccounts", func(a kt.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "token" {
			return false, nil, nil
		}
		return true, &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{Token: "synthetic-token", ExpirationTimestamp: metav1.NewTime(now.Add(time.Hour))}}, nil
	})
	k.ClearActions()
	return k, in, state, now
}

func TestInstanceMaintenanceTokenOnly(t *testing.T) {
	k, in, state, now := maintenanceEnrollment(t)
	checks := 0
	token, expiry, err := RenewInstanceRuntimeToken(t.Context(), k, in, state, now, func(context.Context) error { checks++; return nil })
	if err != nil || token != "synthetic-token" || !expiry.Equal(now.Add(time.Hour)) {
		t.Fatalf("renewal failed: %v", err)
	}
	if checks < 2 {
		t.Fatal("renewal omitted authority validation")
	}
	writes := 0
	for _, a := range k.Actions() {
		if a.GetVerb() == "get" || a.GetVerb() == "list" {
			continue
		}
		writes++
		if a.GetVerb() != "create" || a.GetResource().Resource != "serviceaccounts" || a.GetSubresource() != "token" {
			t.Fatalf("maintenance changed %s", a.GetResource().Resource)
		}
	}
	if writes != 1 {
		t.Fatalf("got %d token requests", writes)
	}
}

func TestInstanceMaintenanceRejectsChangedEnrollment(t *testing.T) {
	for _, change := range []string{"namespace", "account", "role", "binding", "cluster-role", "quota", "missing-receipt", "policy", "unreviewed-rules"} {
		t.Run(change, func(t *testing.T) {
			k, in, state, now := maintenanceEnrollment(t)
			switch change {
			case "namespace":
				state.NamespaceUID = "replaced"
			case "account":
				state.ServiceAccountUID = "replaced"
			case "role":
				state.Objects["Role"] = "replaced"
			case "binding":
				state.Objects["RoleBinding"] = "replaced"
			case "cluster-role":
				state.Objects["ClusterRole"] = "replaced"
			case "quota":
				state.Isolation.Objects["ResourceQuota"] = "replaced"
			case "missing-receipt":
				delete(state.Objects, "Role")
			case "policy":
				in.InstancePolicy.CPUMillis++
			case "unreviewed-rules":
				role, err := k.RbacV1().Roles(in.Namespace).Get(t.Context(), in.ServiceAccountName, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				role.Rules = append(role.Rules, rbacv1.PolicyRule{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}})
				if _, err = k.RbacV1().Roles(in.Namespace).Update(t.Context(), role, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			k.ClearActions()
			token, _, err := RenewInstanceRuntimeToken(t.Context(), k, in, state, now, func(context.Context) error { return nil })
			if err == nil || token != "" {
				t.Fatal("changed enrollment renewed")
			}
			for _, a := range k.Actions() {
				if a.GetVerb() != "get" && a.GetVerb() != "list" {
					t.Fatalf("changed enrollment caused %s", a.GetVerb())
				}
			}
		})
	}
}

func TestInstanceMaintenanceRevokedBeforeToken(t *testing.T) {
	k, in, state, now := maintenanceEnrollment(t)
	denied := errors.New("authority revoked")
	token, _, err := RenewInstanceRuntimeToken(t.Context(), k, in, state, now, func(context.Context) error { return denied })
	if !errors.Is(err, denied) || token != "" {
		t.Fatalf("revoked authority: %v", err)
	}
	for _, a := range k.Actions() {
		if a.GetVerb() != "get" && a.GetVerb() != "list" {
			t.Fatal("revoked authority wrote")
		}
	}
}

func TestInstanceMaintenanceRevokedAfterToken(t *testing.T) {
	k, in, state, now := maintenanceEnrollment(t)
	issued := false
	k.PrependReactor("create", "serviceaccounts", func(a kt.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() == "token" {
			issued = true
		}
		return false, nil, nil
	})
	denied := errors.New("authority revoked")
	token, expiry, err := RenewInstanceRuntimeToken(t.Context(), k, in, state, now, func(context.Context) error {
		if issued {
			return denied
		}
		return nil
	})
	if !errors.Is(err, denied) || token != "" || !expiry.IsZero() {
		t.Fatalf("revoked token released: %v", err)
	}
}

func TestInstanceMaintenanceFreezesCallerInput(t *testing.T) {
	k, in, state, now := maintenanceEnrollment(t)
	first := true
	token, _, err := RenewInstanceRuntimeToken(t.Context(), k, in, state, now, func(context.Context) error {
		if first {
			first = false
			in.InstancePolicy.CPUMillis = 0
			state.Objects["Role"] = "changed-after-start"
		}
		return nil
	})
	if err != nil || token != "synthetic-token" {
		t.Fatalf("caller mutation changed frozen renewal: %v", err)
	}
	if in.InstancePolicy.CPUMillis != 0 || state.Objects["Role"] != "changed-after-start" {
		t.Fatal("maintenance rewrote caller input")
	}
}

func TestInstanceMaintenanceReplacedAccountAfterToken(t *testing.T) {
	k, in, state, now := maintenanceEnrollment(t)
	k.PrependReactor("create", "serviceaccounts", func(a kt.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() == "token" {
			object, err := k.Tracker().Get(corev1.SchemeGroupVersion.WithResource("serviceaccounts"), in.Namespace, in.ServiceAccountName)
			if err != nil {
				t.Fatal(err)
			}
			account := object.(*corev1.ServiceAccount)
			account.UID = "recreated"
			if err = k.Tracker().Update(corev1.SchemeGroupVersion.WithResource("serviceaccounts"), account, in.Namespace); err != nil {
				t.Fatal(err)
			}
		}
		return false, nil, nil
	})
	token, expiry, err := RenewInstanceRuntimeToken(t.Context(), k, in, state, now, func(context.Context) error { return nil })
	if err == nil || token != "" || !expiry.IsZero() {
		t.Fatal("token released after account replacement")
	}
}

func TestInstanceMaintenanceChangedRoleAfterToken(t *testing.T) {
	k, in, state, now := maintenanceEnrollment(t)
	k.PrependReactor("create", "serviceaccounts", func(a kt.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() == "token" {
			object, err := k.Tracker().Get(rbacv1.SchemeGroupVersion.WithResource("roles"), in.Namespace, in.ServiceAccountName)
			if err != nil {
				t.Fatal(err)
			}
			role := object.(*rbacv1.Role)
			role.Rules = append(role.Rules, rbacv1.PolicyRule{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}})
			if err = k.Tracker().Update(rbacv1.SchemeGroupVersion.WithResource("roles"), role, in.Namespace); err != nil {
				t.Fatal(err)
			}
		}
		return false, nil, nil
	})
	token, expiry, err := RenewInstanceRuntimeToken(t.Context(), k, in, state, now, func(context.Context) error { return nil })
	if err == nil || token != "" || !expiry.IsZero() {
		t.Fatal("token released after role gained unreviewed privileges")
	}
}

func TestInstanceMaintenanceCancellationAtFinalCheckpoint(t *testing.T) {
	k, in, state, now := maintenanceEnrollment(t)
	checkpoints := 0
	if _, _, err := RenewInstanceRuntimeToken(t.Context(), k, in, state, now, func(context.Context) error { checkpoints++; return nil }); err != nil {
		t.Fatal(err)
	}
	k, in, state, now = maintenanceEnrollment(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	token, expiry, err := RenewInstanceRuntimeToken(ctx, k, in, state, now, func(context.Context) error {
		calls++
		if calls == checkpoints {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || token != "" || !expiry.IsZero() {
		t.Fatalf("canceled maintenance released material: %v", err)
	}
}
