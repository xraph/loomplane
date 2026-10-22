package kubernetes

import (
	"context"
	"encoding/hex"
	"strings"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/xraph/loomplane/internal/clouds/owned"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// ControllerReceipt records a successful provider mutation, not a discovery
// observation. It contains no credentials and does not authorize issuance.
type ControllerReceipt struct {
	domain.Scope
	ConnectionID         string `json:"connection_id"`
	InstanceID           string `json:"instance_id"`
	PlanID               string `json:"plan_id"`
	RuntimeRef           string `json:"runtime_ref"`
	ServiceID            string `json:"service_id"`
	Container            string `json:"container"`
	Image                string `json:"image"`
	Namespace            string `json:"namespace"`
	NamespaceUID         string `json:"namespace_uid"`
	ClusterCA            string `json:"cluster_ca"`
	ServiceAccount       string `json:"service_account"`
	DeploymentUID        string `json:"deployment_uid"`
	DeploymentGeneration int64  `json:"deployment_generation"`
	RuntimeRevision      string `json:"runtime_revision"`
}

func (r ControllerReceipt) Valid() bool {
	for _, v := range []string{r.OrgID, r.ProjectID, r.EnvironmentID, r.ConnectionID, r.PlanID, r.ServiceID, r.NamespaceUID, r.DeploymentUID} {
		if !domain.ValidIdentitySegment(v) {
			return false
		}
	}
	instance, err := id.ParseWithPrefix(r.InstanceID, id.PrefixInstance)
	ca, caErr := hex.DecodeString(r.ClusterCA)
	return err == nil && instance.String() == r.InstanceID && caErr == nil && len(ca) == 32 && r.ClusterCA == strings.ToLower(r.ClusterCA) && len(validation.IsDNS1123Label(r.Namespace)) == 0 && len(validation.IsDNS1123Subdomain(r.ServiceAccount)) == 0 && len(validation.IsDNS1123Label(r.Container)) == 0 && domain.ValidPinnedImage(r.Image) && r.DeploymentGeneration > 0 && r.RuntimeRef != "" && len(r.RuntimeRef) <= 1024 && !strings.ContainsRune(r.RuntimeRef, 0) && r.RuntimeRevision != "" && len(r.RuntimeRevision) <= 512 && !strings.ContainsRune(r.RuntimeRevision, 0)
}

func (r *Runtime) recordController(ctx context.Context, c *Client, iid id.ID, spec cpprovider.ServiceSpec, labels map[string]string, d *appsv1.Deployment) error {
	if r.RecordController == nil {
		return nil
	}
	if d == nil || d.Name != WorkloadName(iid) || d.Namespace != c.Config.Namespace || d.DeletionTimestamp != nil || d.ResourceVersion == "" || checkOwner(d, iid, labels) != nil || len(d.Spec.Template.Spec.Containers) != 1 || d.Spec.Template.Spec.Containers[0].Name != spec.Name || d.Spec.Template.Spec.Containers[0].Image != spec.Image || d.Spec.Template.Annotations[owned.LabelRevision] != labels[owned.LabelRevision] {
		return ErrOwned
	}
	account := d.Spec.Template.Spec.ServiceAccountName
	if account == "" {
		account = "default"
	}
	receipt := ControllerReceipt{Scope: domain.Scope{OrgID: labels[owned.LabelOrg], ProjectID: labels[owned.LabelProject], EnvironmentID: labels[owned.LabelEnvironment]}, ConnectionID: labels[owned.LabelConnection], InstanceID: iid.String(), PlanID: labels[owned.LabelPlan], RuntimeRef: labels[owned.LabelRuntime], ServiceID: labels[owned.LabelService], Container: spec.Name, Image: spec.Image, Namespace: d.Namespace, NamespaceUID: c.Config.Details["namespace_uid"], ClusterCA: c.Config.Details["cluster_ca"], ServiceAccount: account, DeploymentUID: string(d.UID), DeploymentGeneration: d.Generation, RuntimeRevision: labels[owned.LabelRevision]}
	if !receipt.Valid() {
		return ErrOwned
	}
	return r.RecordController(ctx, receipt)
}
