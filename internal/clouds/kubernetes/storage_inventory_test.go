package kubernetes

import (
	"testing"

	"github.com/xraph/ctrlplane/id"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

func TestStorageInventoryKeepsDetachedClaimsAndChecksAllPods(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	claim := func(role, org string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: owned.PersistentVolumeName(iid, role), Namespace: "selected", UID: types.UID("claim-" + role), Labels: map[string]string{owned.LabelOrg: org, owned.LabelConnection: "connection", owned.LabelProject: "project", owned.LabelEnvironment: "env", owned.LabelInstance: iid.String(), owned.LabelVolumeRole: role}}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}
	}
	attached, retained := claim("data", "org"), claim("archive", "org")
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "selected", UID: "namespace"}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unlabelled-consumer", Namespace: "selected"}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: attached.Name}}}}}}
	c := &Client{Config: &Config{Namespace: "selected", Details: map[string]string{"namespace_uid": "namespace"}}, Kube: fake.NewClientset(ns, attached, retained, claim("foreign", "another-org"), pod)}
	volumes, err := c.StorageVolumes(t.Context(), "org", "connection")
	if err != nil || len(volumes) != 2 {
		t.Fatalf("scoped inventory: %+v %v", volumes, err)
	}
	byName := map[string]owned.StoredVolume{}
	for _, v := range volumes {
		byName[v.Name] = v
	}
	if byName[attached.Name].Status != "in-use" || byName[retained.Name].Status != "retained" || byName[attached.Name].CapacityBytes != 1<<30 {
		t.Fatalf("claim metadata: %+v", volumes)
	}
	for _, a := range c.Kube.(*fake.Clientset).Actions() {
		if a.GetVerb() != "get" && a.GetVerb() != "list" {
			t.Fatalf("inventory mutated storage: %v", a)
		}
	}
	attached.Status.Phase = corev1.ClaimPending
	if _, err = c.Kube.CoreV1().PersistentVolumeClaims("selected").UpdateStatus(t.Context(), attached, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	volumes, err = c.StorageVolumes(t.Context(), "org", "connection")
	if err != nil {
		t.Fatal(err)
	}
	for _, volume := range volumes {
		if volume.Name == attached.Name && volume.Status != "pending" {
			t.Fatalf("unprovisioned claim was reported as usable: %+v", volume)
		}
	}
	ns.UID = "replaced"
	if _, err = c.Kube.CoreV1().Namespaces().Update(t.Context(), ns, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.StorageVolumes(t.Context(), "org", "connection"); err == nil {
		t.Fatal("accepted replaced namespace")
	}
}
