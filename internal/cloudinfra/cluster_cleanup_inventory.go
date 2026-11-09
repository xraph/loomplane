package cloudinfra

import (
	"encoding/hex"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// ClusterCleanupInventory contains workload identities for detecting residuals.
// It carries no resource requests and cannot grant receipt credit or deletion.
type ClusterCleanupInventory struct {
	CheckedAt     time.Time                      `json:"checked_at"`
	SchemaVersion int                            `json:"schema_version"`
	Complete      bool                           `json:"complete"`
	NamespaceUIDs []string                       `json:"namespace_uids"`
	Objects       []ClusterCleanupObject         `json:"objects,omitempty"`
	HPAs          []ClusterCleanupHPAObservation `json:"hpas,omitempty"`
}

type ClusterCleanupObject struct {
	Deleting      bool   `json:"deleting,omitempty"`
	Resource      string `json:"resource"`
	NamespaceUID  string `json:"namespace_uid,omitempty"`
	UID           string `json:"uid"`
	ControllerUID string `json:"controller_uid,omitempty"`
	ReleaseID     string `json:"release_id,omitempty"`
	Generation    int64  `json:"generation,omitempty"`
}

type ClusterCleanupHPAObservation struct {
	Deleting       bool   `json:"deleting,omitempty"`
	NamespaceUID   string `json:"namespace_uid"`
	Name           string `json:"name"`
	UID            string `json:"uid,omitempty"`
	DeploymentName string `json:"deployment_name,omitempty"`
	Generation     int64  `json:"generation,omitempty"`
	// An empty UID represents NotFound only after a successful, qualified GET.
	Complete bool `json:"complete"`
}

func cleanupReleaseMarker(annotations map[string]string) string {
	value := annotations[kubernetes.WorkloadReleaseAnnotation]
	if len(value) != 64 || strings.ToLower(value) != value {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return value
}

// NormalizeCleanupInventory preserves the raw release and controller identities
// of an orphan without copying its pod spec, environment, labels or annotations.
func NormalizeCleanupInventory(in kubernetes.CapacityInventory) ClusterCleanupInventory {
	out := ClusterCleanupInventory{SchemaVersion: 1, Complete: in.Complete, CheckedAt: in.CheckedAt}
	namespaces := map[string]string{}
	seen := map[string]bool{}
	for i := range in.Namespaces {
		ns := &in.Namespaces[i]
		uid := string(ns.UID)
		if ns.DeletionTimestamp != nil {
			continue
		}
		if ns.Name == "" || !domain.ValidIdentitySegment(uid) || namespaces[ns.Name] != "" || seen[uid] {
			out.Complete = false
			continue
		}
		namespaces[ns.Name] = uid
		seen[uid] = true
		out.NamespaceUIDs = append(out.NamespaceUIDs, uid)
	}
	slices.Sort(out.NamespaceUIDs)
	appendObject := func(resource, controllerKind string, obj metav1.Object, annotations map[string]string) {
		uid := string(obj.GetUID())
		if !domain.ValidIdentitySegment(uid) || seen[uid] {
			out.Complete = false
			return
		}
		seen[uid] = true
		ns := namespaces[obj.GetNamespace()]
		if ns == "" {
			out.Complete = false
		}
		owner := ""
		if controllerKind != "" {
			owner = workloadController(obj.GetOwnerReferences(), controllerKind)
			if owner != "" && !domain.ValidIdentitySegment(owner) {
				owner = ""
				out.Complete = false
			}
		}
		out.Objects = append(out.Objects, ClusterCleanupObject{Resource: resource, NamespaceUID: ns, UID: uid, ControllerUID: owner, ReleaseID: cleanupReleaseMarker(annotations), Generation: obj.GetGeneration(), Deleting: obj.GetDeletionTimestamp() != nil})
	}
	for i := range in.Deployments {
		d := &in.Deployments[i]
		appendObject("deployments", "", d, d.Spec.Template.Annotations)
	}
	for i := range in.ReplicaSets {
		rs := &in.ReplicaSets[i]
		appendObject("replicasets", "Deployment", rs, rs.Spec.Template.Annotations)
	}
	for i := range in.Pods {
		p := &in.Pods[i]
		appendObject("pods", "ReplicaSet", p, p.Annotations)
	}
	return out
}
