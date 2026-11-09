package cloudinfra

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClusterEventsAndAuditOmitPrivateState(t *testing.T) {
	audit := &recordingAudit{}
	svc := NewService(Deps{Audit: audit})
	op := &ClusterOperation{ID: "operation", CloudID: "cloud", ResourceID: "resource", Revision: 4, Status: "needs_attention", Error: "private-token", LeaseOwner: "private-lease", Enrollment: &ClusterEnrollment{VaultKey: "private-vault", RetiredVaultKeys: []string{"private-retired"}}}
	svc.recordCluster(t.Context(), "cloud.cluster.reconcile", op)
	raw, err := json.Marshal(struct {
		Change ClusterChange
		Audit  any
	}{clusterChange(op), audit.events})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-token", "private-lease", "private-vault", "private-retired"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("event leaked %s", secret)
		}
	}
	if !strings.Contains(string(raw), "operator_recovery_required") || !strings.Contains(string(raw), "resource") {
		t.Fatal("missing safe outcome", string(raw))
	}
}
