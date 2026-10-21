//go:build integration

package federation

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/agentinstall"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
)

// This checks API defaulting, foreground deletion and replacement preservation.
// It does not install SPIRE, run an agent or qualify workload attestation.
func TestKubernetesPreparationCleanupAPI(t *testing.T) {
	path := os.Getenv("LOOMPLANE_AGENT_INSTALL_TEST_KUBECONFIG")
	if path == "" {
		t.Skip("requires a disposable Kubernetes cluster")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	namespace := "loomplane-cleanup-" + uuid.NewString()[:8]
	cfg, err := kube.ParseConfig(raw, map[string]string{"namespace": namespace})
	if err != nil {
		t.Fatal(err)
	}
	client, err := kube.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	ns, err := client.Kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if deleteErr := client.Kube.CoreV1().Namespaces().Delete(cleanup, namespace, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &ns.UID}}); deleteErr != nil {
			t.Error(deleteErr)
		}
	})
	for _, recoverOwnership := range []bool{false, true} {
		t.Run(map[bool]string{false: "recorded", true: "recovered"}[recoverOwnership], func(t *testing.T) {
			r := &Preparation{PreparationIntent: PreparationIntent{ID: uuid.NewString(), Namespace: namespace, NamespaceUID: string(ns.UID), Image: "example.test/agent@sha256:" + strings.Repeat("a", 64)}, State: PreparationRetiring, CleanupFrom: PreparationPrepared, Generation: 6}
			plan, buildErr := agentinstall.NewKubernetesPlan(namespace, r.ID, r.Image, false)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			desiredAccount, desiredPod, buildErr := plan.PreparationResources(r.NamespaceUID)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			account, createErr := client.Kube.CoreV1().ServiceAccounts(namespace).Create(ctx, desiredAccount, metav1.CreateOptions{})
			if createErr != nil {
				t.Fatal(createErr)
			}
			pod, createErr := client.Kube.CoreV1().Pods(namespace).Create(ctx, desiredPod, metav1.CreateOptions{})
			if createErr != nil {
				t.Fatal(createErr)
			}
			r.AccountUID = string(account.UID)
			r.PodUID = string(pod.UID)
			if recoverOwnership {
				r.PodUID = ""
				r.CleanupFrom = PreparationPodPending
			}
			capture := func(_ context.Context, accountUID, podUID string) (*Preparation, error) {
				saved := *r
				if accountUID != "" {
					saved.AccountUID = accountUID
				}
				if podUID != "" {
					saved.PodUID = podUID
				}
				saved.Generation++
				return &saved, nil
			}
			allow := func(context.Context) error { return nil }
			for {
				r, err = cleanupPreparationResources(ctx, client.Kube, r, capture, allow)
				if err == nil {
					break
				}
				if !errors.Is(err, ErrInUse) {
					t.Fatal(err)
				}
				select {
				case <-ctx.Done():
					t.Fatal("cleanup did not finish", ctx.Err())
				case <-time.After(200 * time.Millisecond):
				}
			}
			if _, readErr := client.Kube.CoreV1().Pods(namespace).Get(ctx, pod.Name, metav1.GetOptions{}); !apierrors.IsNotFound(readErr) {
				t.Fatal("pod remains", readErr)
			}
			if _, readErr := client.Kube.CoreV1().ServiceAccounts(namespace).Get(ctx, account.Name, metav1.GetOptions{}); !apierrors.IsNotFound(readErr) {
				t.Fatal("account remains", readErr)
			}
			replacement, createErr := client.Kube.CoreV1().ServiceAccounts(namespace).Create(ctx, desiredAccount, metav1.CreateOptions{})
			if createErr != nil {
				t.Fatal(createErr)
			}
			if _, cleanupErr := cleanupPreparationResources(ctx, client.Kube, r, capture, allow); cleanupErr != nil {
				t.Fatal(cleanupErr)
			}
			preserved, readErr := client.Kube.CoreV1().ServiceAccounts(namespace).Get(ctx, replacement.Name, metav1.GetOptions{})
			if readErr != nil || preserved.UID != replacement.UID {
				t.Fatal("replacement deleted", readErr)
			}
		})
	}
}
