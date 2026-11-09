package planning

import (
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/security"
)

func TestFleetPlanDecisionRequiresTypedAuthority(t *testing.T) {
	for _, approve := range []bool{false, true} {
		for _, kind := range []string{KindInstance, ""} {
			store := &unpinnedStore{plan: &Plan{ID: "plan", OrgID: "org", Status: StatusReady, Revision: "r1", Kind: kind, ManagedInstanceID: "instance"}}
			service := NewService(Deps{Store: store})
			_, err := service.ApplyDecision(t.Context(), "org", "plan", orchestration.Decision{Approve: approve, Revision: "r1", By: "operator"})
			if !errors.Is(err, security.ErrForbidden) || store.settled != "" {
				t.Fatalf("approve=%t kind=%s: err=%v settled=%s", approve, kind, err, store.settled)
			}
		}
	}
}
