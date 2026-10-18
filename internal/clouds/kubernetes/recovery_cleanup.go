package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"time"

	"github.com/xraph/ctrlplane/id"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	klabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	"github.com/xraph/loomplane/internal/clouds/owned"
	nativepg "github.com/xraph/loomplane/internal/clouds/postgres"
)

const cleanupRefsAnnotation = "loomplane.recovery.volumes"
const destroyAnnotation = "loomplane.recovery.destroy"

type recoveryVolumeRef struct {
	Claim     string    `json:"claim"`
	ClaimUID  types.UID `json:"claim_uid"`
	Volume    string    `json:"volume,omitempty"`
	VolumeUID types.UID `json:"volume_uid,omitempty"`
}

var _ nativepg.Backend = (*Runtime)(nil)

func checkCleanupPV(pv *corev1.PersistentVolume, ref recoveryVolumeRef, namespace string) error {
	claim := pv.Spec.ClaimRef
	if ref.VolumeUID == "" || pv.UID != ref.VolumeUID || claim == nil || claim.UID != ref.ClaimUID || claim.Namespace != namespace || claim.Name != ref.Claim {
		return ErrOwned
	}
	return nil
}
func checkCleanupClaim(claim *corev1.PersistentVolumeClaim, iid id.ID, labels map[string]string) error {
	role := claim.Labels[owned.LabelVolumeRole]
	expected, err := persistentIdentity(iid, labels, role)
	if err != nil {
		return err
	}
	if claim.Name != owned.PersistentVolumeName(iid, role) || claim.UID == "" || len(claim.OwnerReferences) > 0 || claim.Labels[nativepg.LabelRecovery] != labels[nativepg.LabelRecovery] || claim.Labels[nativepg.LabelDatabase] != labels[nativepg.LabelDatabase] || claim.Annotations[volumeImageAnnotation] != labels[imageReferenceLabel] {
		return ErrOwned
	}
	for key, value := range expected {
		if claim.Labels[key] != value {
			return ErrOwned
		}
	}
	return nil
}
func (r *Runtime) PurgeRecovery(ctx context.Context, iid id.ID, labels map[string]string) error {
	if labels[nativepg.LabelRecovery] == "" || labels[owned.LabelPublic] != "false" || labels[owned.LabelStateful] != "true" || !nativepg.ValidDatabase(labels[nativepg.LabelDatabase]) {
		return ErrOwned
	}
	c, err := r.Session(ctx, owned.ForOperation(labels, "remove"))
	if err != nil {
		return err
	}
	ns := c.Config.Namespace
	objects, err := r.objects(ctx, c, iid, labels)
	if err != nil {
		return err
	}
	if objects.deployment != nil || objects.stateful != nil && (objects.stateful.Labels[nativepg.LabelRecovery] != labels[nativepg.LabelRecovery] || objects.stateful.Labels[nativepg.LabelDatabase] != labels[nativepg.LabelDatabase]) {
		return ErrOwned
	}
	policies := c.Kube.NetworkingV1().NetworkPolicies(ns)
	policy, err := policies.Get(ctx, recoveryPolicyName(iid), metav1.GetOptions{})
	missingPolicy := apierrors.IsNotFound(err)
	if err != nil && !missingPolicy {
		return err
	}
	refs := []recoveryVolumeRef{}
	if !missingPolicy {
		if checkErr := checkOwner(policy, iid, labels); checkErr != nil {
			return checkErr
		}
		if policy.Labels[nativepg.LabelRecovery] != labels[nativepg.LabelRecovery] {
			return ErrOwned
		}
		if data := policy.Annotations[cleanupRefsAnnotation]; data != "" {
			if jsonErr := json.Unmarshal([]byte(data), &refs); jsonErr != nil {
				return jsonErr
			}
		}
	}
	claims, err := c.Kube.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{LabelSelector: klabels.Set{owned.LabelInstance: iid.String()}.String()})
	if err != nil {
		return err
	}
	if len(claims.Items) > 16 || len(refs) > 16 {
		return ErrOwned
	}
	for _, claim := range claims.Items {
		if checkErr := checkCleanupClaim(&claim, iid, labels); checkErr != nil {
			return checkErr
		}
		ref := recoveryVolumeRef{Claim: claim.Name, ClaimUID: claim.UID, Volume: claim.Spec.VolumeName}
		if ref.Volume != "" {
			pv, getErr := c.Kube.CoreV1().PersistentVolumes().Get(ctx, ref.Volume, metav1.GetOptions{})
			if getErr != nil && !apierrors.IsNotFound(getErr) {
				return getErr
			}
			if getErr == nil {
				ref.VolumeUID = pv.UID
				if checkErr := checkCleanupPV(pv, ref, ns); checkErr != nil {
					return checkErr
				}
			}
		}
		found := false
		for index, saved := range refs {
			if saved.Claim == ref.Claim {
				if saved.ClaimUID != ref.ClaimUID || (saved.Volume != "" && saved.Volume != ref.Volume) || saved.VolumeUID != "" && ref.VolumeUID != "" && saved.VolumeUID != ref.VolumeUID {
					return ErrOwned
				}
				if ref.VolumeUID == "" {
					ref.VolumeUID = saved.VolumeUID
				}
				refs[index] = ref
				found = true
			}
		}
		if !found {
			refs = append(refs, ref)
		}
	}
	if len(refs) > 16 {
		return ErrOwned
	}
	// The policy is an independent, durable cleanup witness. It survives deletion
	// of the workload and claims until every recorded PV has finished deletion.
	if missingPolicy {
		policy = &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: recoveryPolicyName(iid), Namespace: ns, Labels: recoveryIdentity(iid, labels)}, Spec: denyRecoverySpec(recoveryIdentity(iid, labels))}
	}
	policy.Spec = denyRecoverySpec(recoveryIdentity(iid, labels))
	if policy.Annotations == nil {
		policy.Annotations = map[string]string{}
	}
	delete(policy.Annotations, proofAnnotation)
	encoded, err := json.Marshal(refs)
	if err != nil {
		return err
	}
	policy.Annotations[cleanupRefsAnnotation] = string(encoded)
	if missingPolicy {
		policy, err = policies.Create(ctx, policy, metav1.CreateOptions{})
	} else {
		policy, err = policies.Update(ctx, policy, metav1.UpdateOptions{})
	}
	if err != nil {
		return err
	}
	if checkErr := r.Remove(ctx, iid, labels); checkErr != nil {
		return checkErr
	}
	// A stale proof pod contains no restored data, but is owned and removed too.
	probeName := WorkloadName(iid) + "-check"
	probe, getErr := c.Kube.CoreV1().Pods(ns).Get(ctx, probeName, metav1.GetOptions{})
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return getErr
	}
	if getErr == nil {
		expected := recoveryIdentity(iid, labels)
		expected[owned.LabelInstance] = "probe-" + iid.String()
		expected[recoveryTargetLabel] = "false"
		expected[recoveryProbeLabel] = "true"
		for key, value := range expected {
			if probe.Labels[key] != value {
				return ErrOwned
			}
		}
		if err = c.Kube.CoreV1().Pods(ns).Delete(ctx, probe.Name, deleteUID(probe.UID)); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	pods, err := c.Kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		for _, mount := range pod.Spec.Volumes {
			if mount.PersistentVolumeClaim == nil {
				continue
			}
			for _, ref := range refs {
				if mount.PersistentVolumeClaim.ClaimName == ref.Claim {
					return errors.New("recovery storage is still attached to a pod")
				}
			}
		}
	}
	for _, ref := range refs {
		if ref.Volume != "" {
			pv, readErr := c.Kube.CoreV1().PersistentVolumes().Get(ctx, ref.Volume, metav1.GetOptions{})
			if readErr != nil && !apierrors.IsNotFound(readErr) {
				return readErr
			}
			if readErr == nil {
				if checkErr := checkCleanupPV(pv, ref, ns); checkErr != nil {
					return checkErr
				}
				pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
				pv.Annotations = maps.Clone(pv.Annotations)
				if pv.Annotations == nil {
					pv.Annotations = map[string]string{}
				}
				pv.Annotations[destroyAnnotation] = labels[nativepg.LabelRecovery]
				if _, updateErr := c.Kube.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{}); updateErr != nil {
					return updateErr
				}
			}
		}
		claim, readErr := c.Kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, ref.Claim, metav1.GetOptions{})
		if readErr != nil && !apierrors.IsNotFound(readErr) {
			return readErr
		}
		if readErr == nil {
			if claim.UID != ref.ClaimUID {
				return ErrOwned
			}
			if claim.Spec.VolumeName != ref.Volume {
				return errors.New("recovery volume binding changed during cleanup; retry to capture its identity")
			}
			if checkErr := checkCleanupClaim(claim, iid, labels); checkErr != nil {
				return checkErr
			}
			if deleteErr := c.Kube.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, ref.Claim, deleteUID(ref.ClaimUID)); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
				return deleteErr
			}
		}
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		remaining := false
		for _, ref := range refs {
			if ref.Volume != "" {
				pv, readErr := c.Kube.CoreV1().PersistentVolumes().Get(ctx, ref.Volume, metav1.GetOptions{})
				if readErr != nil && !apierrors.IsNotFound(readErr) {
					return readErr
				}
				if readErr == nil {
					if checkErr := checkCleanupPV(pv, ref, ns); checkErr != nil {
						return checkErr
					}
					remaining = true
				}
			}
			claim, readErr := c.Kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, ref.Claim, metav1.GetOptions{})
			if readErr != nil && !apierrors.IsNotFound(readErr) {
				return readErr
			}
			if readErr == nil {
				if claim.UID != ref.ClaimUID {
					return ErrOwned
				}
				remaining = true
			}
		}
		if !remaining {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("recovery volume deletion is still pending")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return policies.Delete(ctx, policy.Name, deleteUID(policy.UID))
}
