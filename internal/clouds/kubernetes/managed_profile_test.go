package kubernetes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xraph/ctrlplane/id"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

type legacyProfileGuard struct {
	calls  int
	denied bool
}

func (g *legacyProfileGuard) CheckLegacyManagedWrite(context.Context, ManagedRuntimeIdentity) error {
	g.calls++
	if g.denied {
		return ErrManagedAuthority
	}
	return nil
}

func TestManagedLegacyProfileStillChecksEachWrite(t *testing.T) {
	scope := transportScope()
	identity := ManagedRuntimeIdentity{Org: scope.Org, Project: scope.Project, Environment: scope.Environment, Instance: scope.Instance, Namespace: scope.Namespace}
	guard := &legacyProfileGuard{}
	sends := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sends++
		guard.denied = true
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"TooManyRequests","code":429}`))
	}))
	defer api.Close()
	client, err := New(&Config{REST: &rest.Config{Host: api.URL}, Namespace: scope.Namespace, ManagedWorkload: true, ManagedProfile: ManagedProfileLegacy, ManagedIdentity: identity, ManagedLegacyGuard: guard})
	if err != nil {
		t.Fatal(err)
	}
	iid, _ := id.Parse(scope.Instance)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: WorkloadName(iid) + "-runtime", Namespace: scope.Namespace, Labels: map[string]string{owned.LabelOrg: scope.Org, owned.LabelProject: scope.Project, owned.LabelEnvironment: scope.Environment, owned.LabelInstance: scope.Instance}}}
	_, err = client.Kube.CoreV1().Secrets(scope.Namespace).Create(t.Context(), secret, metav1.CreateOptions{})
	if !errors.Is(err, ErrManagedAuthority) || guard.calls != 2 || sends != 1 {
		t.Fatalf("stale legacy retry: checks=%d sends=%d err=%v", guard.calls, sends, err)
	}
	guard.denied = false
	secret.Name = "another-instance-runtime"
	_, err = client.Kube.CoreV1().Secrets(scope.Namespace).Create(t.Context(), secret, metav1.CreateOptions{})
	if !errors.Is(err, ErrManagedAuthority) || guard.calls != 2 || sends != 1 {
		t.Fatalf("legacy identity targeted foreign object: checks=%d sends=%d err=%v", guard.calls, sends, err)
	}
}
