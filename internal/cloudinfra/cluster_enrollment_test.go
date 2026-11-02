package cloudinfra

import (
	"testing"
)

func TestManagedClusterUnsupportedPrerequisites(t *testing.T) {
	if err := supportedClusterPrerequisites(AttachmentPrerequisites{}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []AttachmentPrerequisites{{StorageClass: "retained"}, {RequireBuilds: true}, {RegistryHostname: "registry.example.test"}} {
		if err := supportedClusterPrerequisites(p); err == nil {
			t.Fatalf("accepted unsupported prerequisite: %+v", p)
		}
	}
}

func TestManagedClusterEnrollmentCurrentAuthority(t *testing.T) {
	s, store, _, auth, p, in := clusterFixture(t)
	o := acceptCluster(t, s, p, in)
	o.ResourceID = "cluster"
	o.Enrollment = &ClusterEnrollment{ResourceRevision: 1}
	store.resource = &ManagedResource{ID: "cluster", OrgID: p.OrgID, CloudID: o.CloudID, ConnectionID: in.ConnectionID, AccountID: in.AccountID, Revision: 1}
	if _, _, err := s.enrollmentAuthority(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	auth.revoked = true
	if _, _, err := s.enrollmentAuthority(t.Context(), o); err == nil {
		t.Fatal("revoked approval authorized enrollment")
	}
	auth.revoked = false
	s.ManagedClusters.Enabled = false
	if _, _, err := s.enrollmentAuthority(t.Context(), o); err == nil {
		t.Fatal("disabled feature authorized enrollment")
	}
	s.ManagedClusters.Enabled = true
	s.Connections.(*doksCredentials).generation++
	if _, _, err := s.enrollmentAuthority(t.Context(), o); err == nil {
		t.Fatal("replaced parent authorized enrollment")
	}
}

func TestManagedClusterCreateRejectsUnavailableRoutesBeforePurchase(t *testing.T) {
	s, _, adapter, _, p, in := clusterFixture(t)
	in.Attachment.Prerequisites = AttachmentPrerequisites{RequireIngress: true, IngressAddress: "ingress.example.test"}
	if _, err := s.PreviewCluster(t.Context(), p, "cloud", in); err == nil {
		t.Fatal("create accepted a route prerequisite the provider will not install")
	}
	if adapter.creates != 0 {
		t.Fatal("unavailable routes bought a cluster")
	}
	in.Kind = "adopt"
	if _, _, err := s.clusterAuthority(t.Context(), p, "cloud", in); err != nil {
		t.Fatal("adoption cannot verify existing scoped ingress", err)
	}
}
