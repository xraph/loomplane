package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/xraph/ctrlplane/id"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/clouds/owned"
	nativepg "github.com/xraph/loomplane/internal/clouds/postgres"
)

func TestCleanupRefusesSourceBeforeOpeningTarget(t *testing.T) {
	r := Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		t.Fatal("resolved source credentials for destruction")
		return nil, nil
	}}
	if err := r.PurgeRecovery(t.Context(), id.New(id.PrefixInstance), map[string]string{nativepg.LabelDatabase: "database", owned.LabelPublic: "false", owned.LabelStateful: "true"}); !errors.Is(err, ErrOwned) {
		t.Fatalf("source cleanup accepted: %v", err)
	}
}

func TestCleanupResumesWhenCapturedPVIsAlreadyGone(t *testing.T) {
	r, c, labels, sp := persistentFixture()
	labels[nativepg.LabelDatabase] = "database"
	labels[nativepg.LabelRecovery] = "restore-fixture"
	labels[owned.LabelPublic] = "false"
	labels[imageReferenceLabel] = sp.Image
	sp.Env = map[string]string{"POSTGRES_DB": "database"}
	iid := id.New(id.PrefixInstance)
	if _, err := r.Apply(t.Context(), iid, sp, labels); err != nil {
		t.Fatal(err)
	}
	name := owned.PersistentVolumeName(iid, "data")
	claim, err := c.Kube.CoreV1().PersistentVolumeClaims("selected").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	claim.UID = "claim-identity"
	claim.Spec.VolumeName = "already-deleted-volume"
	if _, err = c.Kube.CoreV1().PersistentVolumeClaims("selected").Update(t.Context(), claim, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	policy, err := c.Kube.NetworkingV1().NetworkPolicies("selected").Get(t.Context(), recoveryPolicyName(iid), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := json.Marshal([]recoveryVolumeRef{{Claim: name, ClaimUID: claim.UID, Volume: claim.Spec.VolumeName, VolumeUID: "original-pv-identity"}})
	if err != nil {
		t.Fatal(err)
	}
	policy.Annotations = map[string]string{cleanupRefsAnnotation: string(refs)}
	if _, err = c.Kube.NetworkingV1().NetworkPolicies("selected").Update(t.Context(), policy, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = r.PurgeRecovery(t.Context(), iid, labels); err != nil {
		t.Fatalf("completed PV deletion could not be resumed: %v", err)
	}
	if _, err = c.Kube.CoreV1().PersistentVolumeClaims("selected").Get(t.Context(), name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("claim survived completed cleanup: %v", err)
	}
}
