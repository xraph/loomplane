package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func enrollmentFixture() (*fake.Clientset, EnrollmentRequest, *EnrollmentState) {
	k := fake.NewClientset()
	n := 0
	k.PrependReactor("create", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		create, ok := action.(ktesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		object, ok := create.GetObject().(metav1.Object)
		if ok {
			n++
			object.SetUID(types.UID(fmt.Sprintf("uid-%d", n)))
		}
		return false, nil, nil
	})
	return k, EnrollmentRequest{AttachmentID: "attachment", ProjectID: "project", Namespace: "runtime", ServiceAccountName: "worker"}, &EnrollmentState{}
}
func TestManagedClusterEnrollmentResume(t *testing.T) {
	k, in, state := enrollmentFixture()
	calls := 0
	interrupted := errors.New("restart")
	checkpoint := func(context.Context, *EnrollmentState) error {
		calls++
		if calls == 5 {
			return interrupted
		}
		return nil
	}
	if err := EnrollRuntime(t.Context(), k, in, state, checkpoint); !errors.Is(err, interrupted) {
		t.Fatalf("first run: %v", err)
	}
	namespaceUID := state.NamespaceUID
	if err := EnrollRuntime(t.Context(), k, in, state, func(context.Context, *EnrollmentState) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if state.NamespaceUID != namespaceUID || len(state.Objects) != 5 {
		t.Fatalf("receipts: %+v", state)
	}
	before := len(k.Actions())
	if err := EnrollRuntime(t.Context(), k, in, state, func(context.Context, *EnrollmentState) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, action := range k.Actions()[before:] {
		if action.GetVerb() == "create" {
			t.Fatal("resume created an existing object")
		}
	}
}
func TestManagedClusterNamespaceRecreated(t *testing.T) {
	k, in, state := enrollmentFixture()
	checkpoint := func(context.Context, *EnrollmentState) error { return nil }
	if err := EnrollRuntime(t.Context(), k, in, state, checkpoint); err != nil {
		t.Fatal(err)
	}
	ns, err := k.CoreV1().Namespaces().Get(t.Context(), in.Namespace, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ns.UID = "replacement"
	if _, err = k.CoreV1().Namespaces().Update(t.Context(), ns, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = EnrollRuntime(t.Context(), k, in, state, checkpoint); err == nil {
		t.Fatal("recreated namespace accepted")
	}
}
func TestManagedClusterCrossProjectAttachment(t *testing.T) {
	k, in, state := enrollmentFixture()
	checkpoint := func(context.Context, *EnrollmentState) error { return nil }
	if err := EnrollRuntime(t.Context(), k, in, state, checkpoint); err != nil {
		t.Fatal(err)
	}
	in.ProjectID = "other"
	if err := EnrollRuntime(t.Context(), k, in, state, checkpoint); err == nil {
		t.Fatal("another project claimed namespace")
	}
}
func TestManagedClusterScopedRBAC(t *testing.T) {
	for _, rule := range RuntimeClusterRules("runtime") {
		for _, resource := range rule.Resources {
			if resource == "namespaces" && (len(rule.ResourceNames) != 1 || rule.ResourceNames[0] != "runtime" || len(rule.Verbs) != 1 || rule.Verbs[0] != "get") {
				t.Fatal("namespace inventory permission")
			}
			if resource != "namespaces" && resource != "selfsubjectaccessreviews" {
				t.Fatal("unexpected cluster privilege")
			}
		}
	}
	for _, rule := range RuntimeRoleRules() {
		for _, resource := range rule.Resources {
			if resource == "*" || resource == "roles" || resource == "rolebindings" {
				t.Fatal("runtime can change authority")
			}
		}
	}
}
func TestManagedClusterShortToken(t *testing.T) {
	now := time.Now()
	for _, duration := range []time.Duration{time.Minute, time.Hour, 2 * time.Hour} {
		k := fake.NewClientset(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "runtime"}})
		k.PrependReactor("create", "serviceaccounts", func(action ktesting.Action) (bool, runtime.Object, error) {
			requested := action.(ktesting.CreateAction).GetObject().(*authenticationv1.TokenRequest)
			if requested.Spec.ExpirationSeconds == nil || *requested.Spec.ExpirationSeconds != 3600 {
				t.Fatal("wrong requested lifetime")
			}
			return true, &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{Token: "token", ExpirationTimestamp: metav1.NewTime(now.Add(duration))}}, nil
		})
		_, expiry, err := RequestRuntimeToken(t.Context(), k, "runtime", "worker", now)
		if duration == time.Hour {
			if err != nil || !expiry.Equal(now.Add(duration)) {
				t.Fatal("valid expiry refused")
			}
		} else if err == nil {
			t.Fatal("unsupported expiry accepted")
		}
	}
}

func TestManagedClusterEnrollmentRevocation(t *testing.T) {
	for stop := 1; stop <= 12; stop++ {
		k, in, state := enrollmentFixture()
		calls := 0
		deniedAt := 0
		denied := errors.New("authority revoked")
		err := EnrollRuntime(t.Context(), k, in, state, func(context.Context, *EnrollmentState) error {
			calls++
			if calls == stop {
				deniedAt = len(k.Actions())
				return denied
			}
			return nil
		})
		if !errors.Is(err, denied) {
			t.Fatalf("checkpoint %d: %v", stop, err)
		}
		for _, action := range k.Actions()[deniedAt:] {
			if action.GetVerb() == "create" {
				t.Fatalf("effect after checkpoint %d revocation", stop)
			}
		}
	}
}
