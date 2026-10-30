package security

import (
	"context"
	"strings"
)

const agentPrefix = "agent:"

// AgentActorName is how people see the AI agent in audit, the dashboard and
// the CLI.
const AgentActorName = "Loomplane agent"

// AgentSubject is a workspace's AI agent's subject in an organization. It's
// also the created_by and decided_by of whatever the agent does.
func AgentSubject(orgID string) string { return agentPrefix + orgID }

// IsAgentSubject reports whether id is an organization's AI agent, and
// which organization's.
func IsAgentSubject(id string) (orgID string, ok bool) {
	orgID, ok = strings.CutPrefix(id, agentPrefix)
	if !ok || orgID == "" {
		return "", false
	}
	return orgID, true
}

// AgentPrincipal is the organization's AI agent. With a person it acts for
// that person in a chat, and the Authorizer allows only what both of them
// may do; with nil it acts alone, as a watcher or a job does. It's read-only
// when the person is. The person must be a person: the automation or the
// agent itself panics, since nothing but a request a person made starts a
// chat.
func AgentPrincipal(orgID string, onBehalfOf *Principal) Principal {
	p := Principal{Kind: KindAgent, UserID: AgentSubject(orgID), OrgID: orgID}
	if onBehalfOf != nil {
		if onBehalfOf.Kind != KindUser || onBehalfOf.OnBehalfOf != nil {
			panic("security: the agent acts only for a person")
		}
		person := *onBehalfOf
		p.OnBehalfOf = &person
		p.ReadOnly = person.ReadOnly
	}
	return p
}

// IsAgent reports whether p is the AI agent.
func (p Principal) IsAgent() bool { return p.Kind == KindAgent }

// Person returns the person an agent principal acts for. ok is false for
// the agent acting alone and for every principal that isn't the agent.
func (p Principal) Person() (person Principal, ok bool) {
	if !p.IsAgent() || p.OnBehalfOf == nil {
		return Principal{}, false
	}
	return *p.OnBehalfOf, true
}

type deploymentRunKey struct{}

// WithDeploymentRun marks ctx as the run of an approved deployment. The
// deployments package sets it on each step it carries out and on the
// cleanup of what a failed one left, and nothing else may (a guard test
// enforces it).
func WithDeploymentRun(ctx context.Context) context.Context {
	return context.WithValue(ctx, deploymentRunKey{}, true)
}

// InDeploymentRun reports whether ctx carries WithDeploymentRun's mark.
func InDeploymentRun(ctx context.Context) bool {
	v, ok := ctx.Value(deploymentRunKey{}).(bool)
	return ok && v
}

// deploymentRunOnly reports whether the agent may use perm only in a
// deployment run. A deployment the agent approved runs as the agent alone
// and changes infrastructure; nothing else the agent does may.
func deploymentRunOnly(perm Permission) bool { return perm == InfrastructureModify }

// AgentCall is the agent's tool call an action happens in, for the audit
// log.
type AgentCall struct{ TaskID, RunID, Tool, CallID string }

type agentCallKey struct{}

// WithAgentCall stores c in ctx.
func WithAgentCall(ctx context.Context, c AgentCall) context.Context {
	return context.WithValue(ctx, agentCallKey{}, c)
}

// AgentCallFrom returns the agent call stored in ctx.
func AgentCallFrom(ctx context.Context) (AgentCall, bool) {
	c, ok := ctx.Value(agentCallKey{}).(AgentCall)
	return c, ok
}
