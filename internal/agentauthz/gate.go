package agentauthz

import (
	"context"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// PromotionRequest is a production plan the agent wants to approve without
// a person.
type PromotionRequest struct {
	// Principal is the agent, acting for the person who asked for the
	// release.
	Principal     security.Principal
	ProjectID     string
	EnvironmentID string
	Plan          *planning.Plan
	TaskID, RunID string
}

// GateDecision is a promotion gate's answer.
type GateDecision struct {
	Allowed bool
	// Failed are the gates that failed, in words a person reads; empty
	// when allowed.
	Failed []string
}

// PromotionGate decides, from evidence Loomplane computes, whether the
// agent may promote a release to production without a person. It's asked
// only after the checks no gate can lift: a person asked for the release,
// the project's autonomous promotion is on, and the plan destroys nothing.
type PromotionGate interface {
	Evaluate(ctx context.Context, req PromotionRequest) (GateDecision, error)
}

// AskFirst is the gate until release preparation brings the real one: it
// never allows, so every production promotion goes to a person.
type AskFirst struct{}

// Evaluate refuses every promotion.
func (AskFirst) Evaluate(context.Context, PromotionRequest) (GateDecision, error) {
	return GateDecision{Failed: []string{"autonomous promotion isn't available yet"}}, nil
}
