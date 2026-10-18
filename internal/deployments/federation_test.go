package deployments

import (
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
)

func TestFederationActionsNeverReachLegacyRunner(t *testing.T) {
	for _, subject := range []planning.Subject{planning.NetworkSubject, planning.PolicySubject} {
		t.Run(string(subject), func(t *testing.T) {
			plans := &fakePlans{plan: &planning.Plan{Status: planning.StatusApproved, Executable: true, Actions: []planning.Action{{Subject: subject, Provider: "kubernetes", Op: planning.Create}}}}
			runner := NewRunner(RunnerDeps{Plans: plans})
			if _, _, err := runner.Start(t.Context(), "o", "p"); !errors.Is(err, ErrNoExecutor) {
				t.Fatalf("legacy runner accepted federation: %v", err)
			}
		})
	}
}
