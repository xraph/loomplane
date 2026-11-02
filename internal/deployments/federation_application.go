package deployments

import (
	"context"
	"time"

	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// ApplicationSources reloads server-owned deployment and installation records
// on every issuance guard. It is configured internally, never from an API body.
type ApplicationSources struct {
	Bindings interface {
		ListBindings(context.Context, string, string) ([]*planning.Binding, error)
	}
	Instances interface {
		Get(context.Context, id.ID) (*instance.Instance, error)
	}
	Controllers   ControllerReceipts
	Installations interface {
		GetInstallation(context.Context, string, string) (*federation.KubernetesInstallation, error)
	}
	Credentials                 Credentials
	Nodes                       federation.SPIRENodeReader
	TrustDomain, OrganizationID string
}
type applicationRecords struct {
	binding    *planning.Binding
	instance   *instance.Instance
	deployment federation.WorkloadDeployment
	profile    federation.KubernetesWorkloadProfile
	receipt    *kube.ControllerReceipt
}

func (s ApplicationSources) load(ctx context.Context, d domain.Destination, target federation.ApplicationTarget) (*applicationRecords, error) {
	if s.Bindings == nil || s.Instances == nil || s.Controllers == nil || s.Installations == nil {
		return nil, federation.ErrDisabled
	}
	bindings, err := s.Bindings.ListBindings(ctx, d.OrgID, d.EnvironmentID)
	if err != nil {
		return nil, err
	}
	var b *planning.Binding
	for _, candidate := range bindings {
		if candidate != nil && candidate.ID == target.BindingID {
			if b != nil {
				return nil, federation.ErrUnverified
			}
			b = cloneBinding(candidate)
		}
	}
	deployment, err := FederationWorkloadDeployment(b)
	if err != nil {
		return nil, err
	}
	if deployment.Scope != d.Scope || deployment.ConnectionID != d.ConnectionID {
		return nil, federation.ErrUnverified
	}
	iid, err := id.ParseWithPrefix(deployment.InstanceID, id.PrefixInstance)
	if err != nil {
		return nil, err
	}
	inst, err := s.Instances.Get(ctx, iid)
	if err != nil {
		return nil, err
	}
	installation, err := s.Installations.GetInstallation(ctx, d.OrgID, d.ID)
	if err != nil {
		return nil, err
	}
	if installation == nil || installation.Generation != d.Generation || installation.Profile.Scope != d.Scope || installation.Profile.DestinationID != d.ID || installation.Profile.ConnectionID != d.ConnectionID || installation.Profile.IdentityRevision != d.Identity.Revision() {
		return nil, federation.ErrUnverified
	}
	base := installation.Profile
	base.PodName, base.PodUID = target.PodName, target.PodUID
	profile, receipt, err := loadFederationController(ctx, s.Controllers, b, inst, d, base)
	if err != nil {
		return nil, err
	}
	return &applicationRecords{binding: b, instance: inst, deployment: deployment, profile: profile, receipt: receipt}, nil
}
func (s ApplicationSources) Resolve(ctx context.Context, d domain.Destination, target federation.ApplicationTarget) (workloadidentity.WorkloadIntent, error) {
	denied := workloadidentity.WorkloadIntent{}
	if d.OrgID != s.OrganizationID || s.Credentials == nil || s.Nodes == nil {
		return denied, federation.ErrDisabled
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	loaded, err := s.load(bounded, d, target)
	if err != nil {
		return denied, err
	}
	verify := federation.VerifyKubernetesWorkloadEvidence(s.Credentials, s.Nodes, s.TrustDomain, s.OrganizationID, func(check context.Context, actual federation.WorkloadDeployment, reported, operatingSystem, architecture string) ([][]byte, error) {
		if actual != loaded.deployment {
			return nil, federation.ErrUnverified
		}
		return LoadFederationImageProofs(check, s.Credentials, loaded.binding, loaded.instance, reported, operatingSystem, architecture)
	})
	evidence, err := verify(bounded, d, loaded.deployment, loaded.profile)
	if err != nil {
		return denied, err
	}
	return workloadidentity.WorkloadIntent{BindingID: loaded.binding.ID, PodName: target.PodName, Controller: *loaded.receipt, Workload: evidence.Workload, OperatingSystem: evidence.OperatingSystem, Architecture: evidence.Architecture, ImageIndexes: evidence.ImageIndexes}, nil
}
