package deployments

import (
	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/planning"
	domain "github.com/xraph/loomplane/pkg/federation"
	"github.com/xraph/loomplane/pkg/provider"
)

// FederationWorkloadDeployment projects a stored service binding for attestation.
// The caller must load and recheck the current binding under its authorization
// guard. Controller UIDs come from a separate trusted installation record.
func FederationWorkloadDeployment(b *planning.Binding) (federation.WorkloadDeployment, error) {
	if b == nil || b.Subject != planning.ServiceSubject || b.Provider != "kubernetes" || b.Engine != provider.CtrlPlane || b.Drift != "" || !domain.ValidPinnedImage(b.Revision) {
		return federation.WorkloadDeployment{}, federation.ErrUnverified
	}
	instance, err := id.ParseWithPrefix(b.ExternalID, id.PrefixInstance)
	if err != nil || instance.String() != b.ExternalID {
		return federation.WorkloadDeployment{}, federation.ErrUnverified
	}
	for _, value := range []string{b.OrgID, b.ProjectID, b.EnvironmentID, b.ID, b.Name, b.ConnectionID} {
		if !domain.ValidIdentitySegment(value) {
			return federation.WorkloadDeployment{}, federation.ErrUnverified
		}
	}
	return federation.WorkloadDeployment{Scope: domain.Scope{OrgID: b.OrgID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID}, BindingID: b.ID, InstanceID: b.ExternalID, ConnectionID: b.ConnectionID, ServiceID: b.Name, Container: mainService, Image: b.Revision}, nil
}
