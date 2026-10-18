package kubernetes

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

func persistentFixture() (*Runtime, *Client, map[string]string, cpprovider.ServiceSpec) {
	retain := corev1.PersistentVolumeReclaimRetain
	binding := storagev1.VolumeBindingWaitForFirstConsumer
	class := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "retained", UID: types.UID("class-identity")}, Provisioner: "example.csi", ReclaimPolicy: &retain, VolumeBindingMode: &binding}
	c := &Client{Config: &Config{Namespace: "selected", Details: map[string]string{"storage_class": "retained", "storage_class_uid": "class-identity"}}, Kube: fake.NewClientset(class)}
	r := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) { return c, nil }, WaitReady: func(context.Context, *Client, string) error { return nil }}
	labels := map[string]string{owned.LabelOrg: "org", owned.LabelProject: "project", owned.LabelEnvironment: "env", owned.LabelConnection: "target", "loomplane.stateful": "true"}
	sp := cpprovider.ServiceSpec{Name: "database", Image: "postgres@sha256:reviewed", Ports: []cpprovider.PortSpec{{Container: 5432, Protocol: "tcp"}}, Volumes: []cpprovider.VolumeSpec{{Name: "data", Type: "persistent", MountPath: "/data", SizeMB: 1024}}}
	return r, c, labels, sp
}

func TestPersistentRuntimeRefusesInvalidStorageBeforeWrites(t *testing.T) {
	for _, change := range []string{"delete-class", "class-replaced", "bound-policy", "capacity", "layout", "image", "public", "replicas", "remove-and-rename", "remove-and-change-image"} {
		t.Run(change, func(t *testing.T) {
			r, c, labels, sp := persistentFixture()
			iid := id.New(id.PrefixInstance)
			if _, err := r.Apply(t.Context(), iid, sp, labels); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "bound-policy":
				claims, err := c.Kube.CoreV1().PersistentVolumeClaims("selected").List(t.Context(), metav1.ListOptions{})
				if err != nil {
					t.Fatal(err)
				}
				claim := claims.Items[0].DeepCopy()
				claim.UID = "claim-identity"
				claim.Spec.VolumeName = "bound-volume"
				if _, err = c.Kube.CoreV1().PersistentVolumeClaims("selected").Update(t.Context(), claim, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
				if _, err = c.Kube.CoreV1().PersistentVolumes().Create(t.Context(), &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "bound-volume"}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete, ClaimRef: &corev1.ObjectReference{Namespace: "selected", Name: claim.Name, UID: claim.UID}}}, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "delete-class", "class-replaced":
				sc, err := c.Kube.StorageV1().StorageClasses().Get(t.Context(), "retained", metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if change == "delete-class" {
					policy := corev1.PersistentVolumeReclaimDelete
					sc.ReclaimPolicy = &policy
				} else {
					sc.UID = "another-class"
				}
				if _, err = c.Kube.StorageV1().StorageClasses().Update(t.Context(), sc, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "capacity":
				sp.Volumes[0].SizeMB = 512
			case "layout":
				sp.Volumes[0].MountPath = "/other"
			case "image":
				sp.Image = "postgres@sha256:another-version"
			case "public":
				labels[owned.LabelPublic] = "true"
			case "replicas":
				labels[owned.LabelReplicas] = "2"
			case "remove-and-rename":
				if err := r.Remove(t.Context(), iid, labels); err != nil {
					t.Fatal(err)
				}
				sp.Volumes[0].Name = "other"
			case "remove-and-change-image":
				if err := r.Remove(t.Context(), iid, labels); err != nil {
					t.Fatal(err)
				}
				sp.Image = "postgres@sha256:another-version"
			}
			kube := c.Kube.(*fake.Clientset)
			kube.ClearActions()
			if _, err := r.Apply(t.Context(), iid, sp, labels); err == nil {
				t.Fatal("unsafe persistent change accepted")
			}
			for _, a := range kube.Actions() {
				if a.GetVerb() != "get" && a.GetVerb() != "list" {
					t.Fatalf("mutated invalid storage: %+v", a)
				}
			}
		})
	}
}

func TestPersistentFailedRolloutRestoresRuntimeAndKeepsData(t *testing.T) {
	r, c, labels, sp := persistentFixture()
	iid := id.New(id.PrefixInstance)
	if _, err := r.Apply(t.Context(), iid, sp, labels); err != nil {
		t.Fatal(err)
	}
	prior, err := c.Kube.AppsV1().StatefulSets("selected").Get(t.Context(), WorkloadName(iid), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r.WaitReady = func(context.Context, *Client, string) error { return errors.New("rollout timeout") }
	sp.Env = map[string]string{"CHANGED": "yes"}
	if _, err = r.Apply(t.Context(), iid, sp, labels); err == nil {
		t.Fatal("failed rollout accepted")
	}
	got, err := c.Kube.AppsV1().StatefulSets("selected").Get(t.Context(), prior.Name, metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(got.Spec, prior.Spec) {
		t.Fatalf("previous workload not restored: %v", err)
	}
	claims, err := c.Kube.CoreV1().PersistentVolumeClaims("selected").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(claims.Items) != 1 {
		t.Fatalf("rollback lost data: %+v %v", claims, err)
	}
	if err = r.Remove(t.Context(), iid, labels); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Apply(t.Context(), iid, sp, labels); err == nil {
		t.Fatal("failed initial rollout accepted")
	}
	if _, err = c.Kube.AppsV1().StatefulSets("selected").Get(t.Context(), prior.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("failed initial rollout left workload: %v", err)
	}
	claims, err = c.Kube.CoreV1().PersistentVolumeClaims("selected").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(claims.Items) != 1 {
		t.Fatalf("failed initial rollout lost data: %+v %v", claims, err)
	}
}

func TestPersistentRuntimeRetainsClaimAcrossReplacementAndRemoval(t *testing.T) {
	r, c, labels, sp := persistentFixture()
	iid := id.New(id.PrefixInstance)
	if _, err := r.Apply(t.Context(), iid, sp, labels); err != nil {
		t.Fatal(err)
	}
	set, err := c.Kube.AppsV1().StatefulSets("selected").Get(t.Context(), WorkloadName(iid), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if *set.Spec.Replicas != 1 || set.Spec.PersistentVolumeClaimRetentionPolicy == nil || set.Spec.PersistentVolumeClaimRetentionPolicy.WhenDeleted != "Retain" || len(set.Spec.Template.Spec.Volumes) != 1 {
		t.Fatalf("unsafe stateful identity: %+v", set.Spec)
	}
	if probe := set.Spec.Template.Spec.Containers[0].ReadinessProbe; probe == nil || probe.TCPSocket == nil || probe.TCPSocket.Port.IntVal != 5432 {
		t.Fatalf("native protocol readiness is missing: %+v", probe)
	}
	claimName := set.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName
	claim, err := c.Kube.CoreV1().PersistentVolumeClaims("selected").Get(t.Context(), claimName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(claim.OwnerReferences) != 0 || claim.Labels[owned.LabelConnection] != "target" || claim.Spec.StorageClassName == nil || *claim.Spec.StorageClassName != "retained" {
		t.Fatalf("claim ownership/retention: %+v", claim)
	}
	labels[owned.LabelRevision] = "next-rollout"
	if _, err = r.Apply(t.Context(), iid, sp, labels); err != nil {
		t.Fatal(err)
	}
	if err = r.Scale(t.Context(), iid, labels, cpprovider.ResourceSpec{Replicas: 2}); err == nil {
		t.Fatal("single-writer profile scaled to two writers")
	}
	if err = r.SetRunning(t.Context(), iid, labels, false); err != nil {
		t.Fatal(err)
	}
	set, err = c.Kube.AppsV1().StatefulSets("selected").Get(t.Context(), set.Name, metav1.GetOptions{})
	if err != nil || *set.Spec.Replicas != 0 {
		t.Fatalf("stop: %+v %v", set, err)
	}
	if err = r.Remove(t.Context(), iid, labels); err != nil {
		t.Fatal(err)
	}
	kept, err := c.Kube.CoreV1().PersistentVolumeClaims("selected").Get(t.Context(), claimName, metav1.GetOptions{})
	if err != nil || kept.Name != claim.Name {
		t.Fatalf("removal lost data: %+v %v", kept, err)
	}
	if _, err = r.Apply(t.Context(), iid, sp, labels); err != nil {
		t.Fatal(err)
	}
	set, err = c.Kube.AppsV1().StatefulSets("selected").Get(t.Context(), set.Name, metav1.GetOptions{})
	if err != nil || set.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != claimName {
		t.Fatalf("reattach lost identity: %+v %v", set, err)
	}
}

func TestPersistentRuntimeRefusesForeignClaimsBeforeMutation(t *testing.T) {
	r, c, labels, sp := persistentFixture()
	iid := id.New(id.PrefixInstance)
	if _, err := r.Apply(t.Context(), iid, sp, labels); err != nil {
		t.Fatal(err)
	}
	claims, err := c.Kube.CoreV1().PersistentVolumeClaims("selected").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(claims.Items) != 1 {
		t.Fatalf("claims: %+v %v", claims, err)
	}
	claim := claims.Items[0].DeepCopy()
	claim.Labels[owned.LabelConnection] = "other"
	if _, err = c.Kube.CoreV1().PersistentVolumeClaims("selected").Update(t.Context(), claim, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	kube := c.Kube.(*fake.Clientset)
	kube.ClearActions()
	if _, err = r.Apply(t.Context(), iid, sp, labels); err == nil {
		t.Fatal("adopted another target's data")
	}
	for _, a := range kube.Actions() {
		if a.GetVerb() != "get" && a.GetVerb() != "list" {
			t.Fatalf("mutated before ownership check: %+v", a)
		}
	}
}

func TestPersistentRuntimeRefusesAnotherConnectionWorkload(t *testing.T) {
	r, c, labels, sp := persistentFixture()
	iid := id.New(id.PrefixInstance)
	if _, err := r.Apply(t.Context(), iid, sp, labels); err != nil {
		t.Fatal(err)
	}
	set, err := c.Kube.AppsV1().StatefulSets("selected").Get(t.Context(), WorkloadName(iid), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	set.Labels[owned.LabelConnection] = "other-target"
	if _, err = c.Kube.AppsV1().StatefulSets("selected").Update(t.Context(), set, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	kube := c.Kube.(*fake.Clientset)
	kube.ClearActions()
	if err = r.Remove(t.Context(), iid, labels); err == nil {
		t.Fatal("removed a workload from another connection")
	}
	for _, a := range kube.Actions() {
		if a.GetVerb() != "get" {
			t.Fatalf("mutated foreign workload: %+v", a)
		}
	}
}
