package workloadidentity

import (
	"crypto/x509"
	"testing"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
)

func TestWorkloadIdentityCannotBeChosenByAgent(t *testing.T) {
	selector := domain.WorkloadSelector{Scope: domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, DestinationID: "east", ServiceID: "checkout"}
	uri, err := WorkloadURI("loomplane.test", selector)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseWorkloadURI(uri)
	if err != nil || got != selector {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	now := time.Now()
	ca := newCA(t, now)
	trust, err := NewTrust("loomplane.test", []*x509.Certificate{ca.cert})
	if err != nil {
		t.Fatal(err)
	}
	cert := ca.issue(t, now, uri)
	got, err = VerifyWorkloadPeer(trust, []*x509.Certificate{cert}, selector.OrgID, now)
	if err != nil || got != selector {
		t.Fatalf("workload verification: %+v %v", got, err)
	}
	a := identityFixture()
	agentURI, err := AgentURI("loomplane.test", a)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{agentURI, uri + "/", uri + "?service=other", uri + "#other", "spiffe://other.test/loomplane/org/org/project/project/environment/env/destination/east/service/checkout"} {
		cert = ca.issue(t, now, bad)
		if _, err = VerifyWorkloadPeer(trust, []*x509.Certificate{cert}, selector.OrgID, now); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	foreign := selector.Scope
	foreign.OrgID = "other"
	if _, err = VerifyWorkloadPeer(trust, []*x509.Certificate{ca.issue(t, now, uri)}, foreign.OrgID, now); err == nil {
		t.Fatal("accepted foreign scope")
	}
	otherCA := newCA(t, now)
	if _, err = VerifyWorkloadPeer(trust, []*x509.Certificate{otherCA.issue(t, now, uri)}, selector.OrgID, now); err == nil {
		t.Fatal("accepted foreign CA")
	}
	if _, err = VerifyWorkloadPeer(trust, []*x509.Certificate{ca.issue(t, now, uri)}, selector.OrgID, now.Add(time.Hour)); err == nil {
		t.Fatal("accepted expired certificate")
	}
	for _, bad := range []domain.WorkloadSelector{{}, {Scope: selector.Scope, DestinationID: "east", ServiceID: "../neighbor"}} {
		if _, err = WorkloadURI("loomplane.test", bad); err == nil {
			t.Fatal("accepted invalid workload selector")
		}
	}
}
