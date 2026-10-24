package deployments

import (
	"context"

	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/planning"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type ControllerReceipts interface {
	RecordController(context.Context, kube.ControllerReceipt) error
	GetController(context.Context, string, string, string, string) (*kube.ControllerReceipt, error)
}

// LoadFederationController joins a current stored binding and instance to the
// receipt written by its deployment operation. The caller must authorize and
// load both records, then recheck them before issuance. This does not register
// an identity or replace live owner-chain and image-proof verification.
func LoadFederationController(ctx context.Context, records ControllerReceipts, b *planning.Binding, inst *instance.Instance, d domain.Destination, base federation.KubernetesAttestationProfile) (federation.KubernetesWorkloadProfile, error) {
	denied := federation.KubernetesWorkloadProfile{}
	if err := ctx.Err(); err != nil {
		return denied, err
	}
	deployment, ref, err := federationWorkloadInstance(b, inst)
	if err != nil {
		return denied, err
	}
	if records == nil || d.Quarantined || d.Scope != deployment.Scope || d.ConnectionID != deployment.ConnectionID || d.Identity.Provider != "kubernetes" || base.Scope != d.Scope || base.DestinationID != d.ID || base.ConnectionID != d.ConnectionID || base.IdentityRevision != d.Identity.Revision() {
		return denied, federation.ErrUnverified
	}
	receipt, err := records.GetController(ctx, b.OrgID, b.ConnectionID, b.ExternalID, ref)
	if err != nil {
		return denied, err
	}
	if receipt == nil || !receipt.Valid() || receipt.Scope != deployment.Scope || receipt.ConnectionID != deployment.ConnectionID || receipt.InstanceID != deployment.InstanceID || receipt.RuntimeRef != ref || receipt.PlanID != b.PlanID || receipt.ServiceID != b.Name || receipt.Container != deployment.Container || receipt.Image != deployment.Image || receipt.ClusterCA != d.Identity.Account || receipt.NamespaceUID != d.Identity.NamespaceUID || receipt.NamespaceUID != d.Identity.ResourceID {
		return denied, federation.ErrUnverified
	}
	base.ServiceAccount, base.Container = receipt.ServiceAccount, receipt.Container
	return federation.KubernetesWorkloadProfile{KubernetesAttestationProfile: base, BindingID: b.ID, DeploymentUID: receipt.DeploymentUID, DeploymentGeneration: receipt.DeploymentGeneration, RuntimeRevision: receipt.RuntimeRevision}, nil
}

func federationWorkloadInstance(b *planning.Binding, inst *instance.Instance) (federation.WorkloadDeployment, string, error) {
	deployment, err := FederationWorkloadDeployment(b)
	if err != nil {
		return federation.WorkloadDeployment{}, "", err
	}
	if inst == nil || !domain.ValidIdentitySegment(b.PlanID) || inst.ID.String() != deployment.InstanceID || inst.TenantID != b.OrgID || inst.ProviderName != "kubernetes."+b.ConnectionID || inst.State != cpprovider.StateRunning || (inst.Kind != "" && inst.Kind != cpprovider.KindDeployment) || len(inst.Services) != 1 || inst.Services[0].Name != deployment.Container || inst.Services[0].Image != deployment.Image || len(inst.Services[0].Volumes) != 0 {
		return federation.WorkloadDeployment{}, "", federation.ErrUnverified
	}
	labels := inst.Labels
	for key, value := range map[string]string{owned.LabelOrg: b.OrgID, owned.LabelProject: b.ProjectID, owned.LabelEnvironment: b.EnvironmentID, owned.LabelConnection: b.ConnectionID, owned.LabelService: b.Name, owned.LabelPlan: b.PlanID} {
		if labels[key] != value {
			return federation.WorkloadDeployment{}, "", federation.ErrUnverified
		}
	}
	ref := labels[owned.LabelRuntime]
	if ref == "" || validateRuntimeRef(b.OrgID, b.EnvironmentID, b.Name, ref) != nil {
		return federation.WorkloadDeployment{}, "", federation.ErrUnverified
	}
	return deployment, ref, nil
}
