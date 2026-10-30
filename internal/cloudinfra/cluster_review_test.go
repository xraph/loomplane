package cloudinfra

import (
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestClusterFeatureGate(t *testing.T) {
	s := NewService(Deps{})
	if _, err := s.PreviewCluster(t.Context(), security.Principal{OrgID: "org", UserID: "manager"}, "cloud", ClusterInput{Kind: "create"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("disabled feature: %v", err)
	}
}

func TestClusterPurchaseRequiresQualificationAndFixedPools(t *testing.T) {
	for _, change := range []func(*Service, *ClusterInput){
		func(s *Service, _ *ClusterInput) { s.ManagedClusters.QualificationOperatorID = "other" },
		func(_ *Service, in *ClusterInput) { in.Spec.NodePools[0].Autoscale = true },
		func(_ *Service, in *ClusterInput) { in.Spec.NodePools[0].Max = 5 },
		func(_ *Service, in *ClusterInput) { in.Spec.NodePools[0].Count = 4 },
		func(_ *Service, in *ClusterInput) { in.ManagementReachable = false },
		func(_ *Service, in *ClusterInput) { in.AcknowledgeUnknownCosts = false },
		func(_ *Service, in *ClusterInput) { in.WorkerImage = "latest" },
	} {
		s, _, adapter, _, p, in := clusterFixture(t)
		change(s, &in)
		if _, err := s.PreviewCluster(t.Context(), p, "cloud", in); err == nil {
			t.Fatal("unsafe purchase preview accepted")
		}
		if adapter.creates != 0 {
			t.Fatal("preview mutated provider")
		}
	}
}
func TestClusterAdoptionKeepsExternalOwnership(t *testing.T) {
	s, store, adapter, _, p, in := clusterFixture(t)
	in.Kind = "adopt-external"
	in.Spec = provider.ClusterSpec{}
	in.Ref = clusterObservation().Ref
	o := acceptCluster(t, s, p, in)
	if err := s.ReconcileClusterOperation(t.Context(), p.OrgID, o.ID, nil); err != nil {
		t.Fatal(err)
	}
	if adapter.creates != 0 || store.resource.Ownership != "external" || store.op.Status != "needs_attention" {
		t.Fatal("adoption transferred ownership or advertised enrollment")
	}
	in.Kind = "adopt-managed"
	if _, err := s.PreviewCluster(t.Context(), p, "cloud", in); err == nil {
		t.Fatal("managed adoption inferred ownership")
	}
}

func TestClusterCallerCannotQualifyManagementPath(t *testing.T) {
	s, _, adapter, _, p, in := clusterFixture(t)
	s.ManagedClusters.ManagementProbe = config.ManagedClusterManagementProbeConfig{}
	// The caller's flag is already true. No server-owned path evidence exists.
	if _, err := s.PreviewCluster(t.Context(), p, "cloud", in); err == nil {
		t.Fatal("caller flag bypassed absent management qualification")
	}
	if adapter.creates != 0 {
		t.Fatal("management preflight created provider resources")
	}
}
