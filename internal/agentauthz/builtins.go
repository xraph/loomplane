package agentauthz

import (
	"context"
	"errors"
	"fmt"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/llm"

	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/security"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

// KnowledgeSearch is cortex's builtin search of the workspace's knowledge
// and the docs. It only reads.
const KnowledgeSearch = "knowledge_search"

// ErrKnowledgeOff refuses knowledge_search in a workspace whose knowledge
// is off, or on a server that can't give it.
var ErrKnowledgeOff = errors.New("knowledge is off for this workspace")

// builtins are the cortex builtins the agent may use, by risk. Cortex
// offers its builtins to every run; any other is hidden and refused like
// a tool that isn't an operation.
var builtins = map[string]defs.Risk{KnowledgeSearch: defs.RiskRead}

// BuiltinRisk is the risk of a cortex builtin the agent may use, and
// whether name is one: knowledge_search reads.
func BuiltinRisk(name string) (defs.Risk, bool) {
	risk, ok := builtins[name]
	return risk, ok
}

// builtinOn reports whether the builtin name is on in orgID: for
// knowledge_search, whether Knowledge says the workspace has it. A nil
// Knowledge means it's off everywhere.
func (a *Authorizer) builtinOn(ctx context.Context, orgID, name string) bool {
	if name == KnowledgeSearch {
		return a.Knowledge != nil && orgID != "" && a.Knowledge(ctx, orgID)
	}
	return false
}

// visibleBuiltin reports whether a run of p in orgID is shown the builtin
// t: it's one the agent may use, on in the workspace, and p is that
// workspace's agent with permission to use it. may checks a permission.
func (a *Authorizer) visibleBuiltin(ctx context.Context, p security.Principal, orgID string, t llm.Tool, may func(security.Permission) bool) bool {
	risk, ok := BuiltinRisk(t.Name)
	if !ok || p.OrgID != orgID || (p.ReadOnly && risk.ChangesSomething()) {
		return false
	}
	return a.builtinOn(ctx, orgID, t.Name) && may(security.AgentUse)
}

// authorizeBuiltin decides a call of a cortex builtin the agent may use.
// No Loomplane service stands behind a builtin to check the principal, so
// this does: a read runs when the principal is the run's workspace's
// agent, it and the person it acts for may use the agent there, and the
// builtin is on in the workspace. Anything else is refused.
func (a *Authorizer) authorizeBuiltin(ctx context.Context, s cortex.Subject, tc llm.ToolCall, risk defs.Risk) error {
	noteFrom(ctx).set(risk, "")
	call, err := agentops.CallFrom(cortex.Invocation{Subject: s, Call: tc})
	if err != nil {
		return err
	}
	if risk != defs.RiskRead {
		return fmt.Errorf("%s: risk %q: %w", tc.Name, risk, errNotAnOperation)
	}
	if a.Authz == nil {
		return fmt.Errorf("%w: %s: no permission check is configured", security.ErrForbidden, tc.Name)
	}
	allowed, err := a.Authz.AllowedOn(ctx, call.Principal, security.AgentUse, call.Scope.ProjectID)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("%w: %s needs %s", security.ErrForbidden, tc.Name, security.AgentUse.Name())
	}
	if !a.builtinOn(ctx, call.Principal.OrgID, tc.Name) {
		return fmt.Errorf("%s: %w", tc.Name, ErrKnowledgeOff)
	}
	return nil
}
