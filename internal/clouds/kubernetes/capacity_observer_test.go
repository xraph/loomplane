package kubernetes

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestClusterCapacityObserverRBAC(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("list", "nodes", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("forbidden") })
	facts, err := ReadCapacityInventory(t.Context(), client)
	if err == nil || facts.Complete {
		t.Fatal("forbidden list became known zero")
	}
	for _, rule := range CapacityObserverRules() {
		for _, verb := range rule.Verbs {
			if verb != "get" && verb != "list" {
				t.Fatal("observer may mutate")
			}
		}
	}
}
func TestClusterCapacityObserverPagination(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("list", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.NodeList{ListMeta: metav1.ListMeta{Continue: "more"}}, nil
	})
	facts, err := ReadCapacityInventory(t.Context(), client)
	if err != nil {
		t.Fatal(err)
	}
	if facts.Complete {
		t.Fatal("pagination silently truncated")
	}
}

func TestClusterScalingDaemonObserverDenied(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("list", "daemonsets", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("forbidden") })
	facts, err := ReadCapacityInventory(t.Context(), client)
	if err == nil || facts.Complete {
		t.Fatal("missing DaemonSet evidence became complete inventory")
	}
}
