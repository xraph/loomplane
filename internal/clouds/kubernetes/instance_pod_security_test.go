package kubernetes

import (
	"bytes"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/pkg/provider"
)

func requireInstanceSecurity(t *testing.T, pod corev1.PodSpec) {
	t.Helper()
	if pod.SecurityContext == nil || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot || pod.SecurityContext.SeccompProfile == nil || pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatal("missing restricted pod security")
	}
	for _, c := range pod.Containers {
		s := c.SecurityContext
		if s == nil || s.AllowPrivilegeEscalation == nil || *s.AllowPrivilegeEscalation || s.Capabilities == nil || len(s.Capabilities.Drop) != 1 || s.Capabilities.Drop[0] != "ALL" {
			t.Fatal("missing restricted container security")
		}
		if c.Resources.Requests.StorageEphemeral().Value() < provider.DefaultInstanceContainerStorageMiB<<20 {
			t.Fatal("unreserved container storage")
		}
	}
}

func TestInstancePodRestrictedSecurity(t *testing.T) {
	spec, labels := podGroupFixture(t)
	var group provider.PodGroup
	if err := json.Unmarshal([]byte(labels[owned.LabelPodGroup]), &group); err != nil {
		t.Fatal(err)
	}
	group.IsolationRevision = provider.InstanceIsolationRevision
	group.RuntimeStorageMiB = provider.DefaultInstanceContainerStorageMiB
	raw, err := json.Marshal(group)
	if err != nil {
		t.Fatal(err)
	}
	labels[owned.LabelPodGroup] = string(raw)
	pod, _, err := runtimePod(spec, labels, owned.RuntimeValues{}, &corev1.Secret{}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	requireInstanceSecurity(t, pod.Spec)
}

func TestInstanceLifecycleRestrictedSecurity(t *testing.T) {
	run := lifecycleRunFixture()
	legacy, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(legacy, []byte("IsolationRevision")) {
		t.Fatal("omitted isolation revision changed legacy descriptors")
	}
	oldObjects, err := lifecycleObjects(run, 1, nil)
	if err != nil || oldObjects.Job.Spec.Template.Spec.SecurityContext != nil {
		t.Fatal("legacy job rendering changed", err)
	}
	legacyDigest := run.Digest()
	run.IsolationRevision = provider.InstanceIsolationRevision
	if run.Digest() == legacyDigest {
		t.Fatal("isolation policy was not sealed")
	}
	objects, err := lifecycleObjects(run, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	requireInstanceSecurity(t, objects.Job.Spec.Template.Spec)
	run.IsolationRevision = "unknown"
	if _, err := lifecycleObjects(run, 1, nil); err == nil {
		t.Fatal("unknown isolation revision accepted")
	}
}
