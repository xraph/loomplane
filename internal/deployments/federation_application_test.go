package deployments

import (
	"context"
	"testing"

	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"

	"github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/planning"
)

type applicationSourcesFixture struct {
	binding      *planning.Binding
	inst         *instance.Instance
	installation *federation.KubernetesInstallation
}

func (s *applicationSourcesFixture) ListBindings(_ context.Context, org, env string) ([]*planning.Binding, error) {
	if org != s.binding.OrgID || env != s.binding.EnvironmentID {
		return nil, federation.ErrNotFound
	}
	return []*planning.Binding{s.binding}, nil
}
func (s *applicationSourcesFixture) Get(_ context.Context, iid id.ID) (*instance.Instance, error) {
	if iid != s.inst.ID {
		return nil, federation.ErrNotFound
	}
	return s.inst, nil
}
func (s *applicationSourcesFixture) GetInstallation(_ context.Context, org, dest string) (*federation.KubernetesInstallation, error) {
	if org != s.installation.Profile.OrgID || dest != s.installation.Profile.DestinationID {
		return nil, federation.ErrNotFound
	}
	return s.installation, nil
}
func TestApplicationSourcesDeriveProfileFromCommittedRecords(t *testing.T) {
	records, b, inst, d, base := federationControllerFixture()
	d.Generation = 4
	fixture := &applicationSourcesFixture{binding: b, inst: inst, installation: &federation.KubernetesInstallation{Profile: base, Generation: d.Generation}}
	sources := ApplicationSources{Bindings: fixture, Instances: fixture, Controllers: records, Installations: fixture}
	target := federation.ApplicationTarget{BindingID: b.ID, PodName: "current-pod", PodUID: "current-uid"}
	loaded, err := sources.load(t.Context(), d, target)
	if err != nil || loaded.deployment.BindingID != b.ID || loaded.profile.PodName != target.PodName || loaded.profile.PodUID != target.PodUID || loaded.profile.SPIRECluster != base.SPIRECluster || loaded.profile.DeploymentUID != records.receipt.DeploymentUID || *loaded.receipt != records.receipt {
		t.Fatal("untrusted profile", err)
	}
	for _, scenario := range []string{"foreign binding", "foreign instance", "stale installation", "changed installation trust", "wrong receipt"} {
		t.Run(scenario, func(t *testing.T) {
			oldBinding, oldInstance, oldInstall, oldReceipt := *b, *inst, *fixture.installation, records.receipt
			defer func() {
				*b = oldBinding
				*inst = oldInstance
				*fixture.installation = oldInstall
				records.receipt = oldReceipt
			}()
			switch scenario {
			case "foreign binding":
				b.OrgID = "other"
			case "foreign instance":
				inst.TenantID = "other"
			case "stale installation":
				fixture.installation.Generation--
			case "changed installation trust":
				fixture.installation.Profile.IdentityRevision = "other"
			case "wrong receipt":
				records.receipt.PlanID = "other"
			}
			if _, err := sources.load(t.Context(), d, target); err == nil {
				t.Fatal("changed source accepted")
			}
		})
	}
}
