package deployments

import (
	"testing"

	"github.com/xraph/loomplane/internal/security"
)

func TestJobPrincipalForAutomationApprovedPlans(t *testing.T) {
	auto := &Job{Deployment: &Deployment{OrgID: "o", StartedBy: "loomplane:o"}}
	if got := principal(auto); got != security.SystemPrincipal("o") {
		t.Fatalf("automation-approved deployment acts as %+v", got)
	}
	person := &Job{Deployment: &Deployment{OrgID: "o", StartedBy: "usr_1"}}
	if got, want := principal(person), (security.Principal{OrgID: "o", UserID: "usr_1"}); got != want {
		t.Fatalf("person-approved deployment acts as %+v, want %+v", got, want)
	}
	// A record naming another organization's automation is a person here,
	// with no roles: it never gains the automation's.
	if got := principal(&Job{Deployment: &Deployment{OrgID: "o", StartedBy: "loomplane:other"}}); got.IsServiceAccount() {
		t.Fatalf("another org's automation acts as %+v", got)
	}
}
