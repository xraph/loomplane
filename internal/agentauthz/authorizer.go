// Package agentauthz decides, per operation and risk class, what the AI
// agent may do alone and what needs a person. It's the agent's
// cortex.ToolAuthorizer: cortex asks it which tools the model sees and
// whether each call may run, pauses the run for a person, or refuses.
//
// The table is in code and reads only Loomplane's own records (the
// operation's risk, the plan, the project's autonomy switch), so nothing
// the model writes moves a call past it. Shield's tool-call scan runs
// after it and can only restrict (Layered). Every service an allowed call
// reaches still checks both the agent and the person it acts for.
package agentauthz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/llm"
	shieldsafety "github.com/xraph/cortex/safety/shield"
	shieldengine "github.com/xraph/shield/engine"
	"github.com/xraph/shield/presets"

	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

// ErrNeedsPerson refuses a call only a person may make. The model reads
// why, and asks one.
var ErrNeedsPerson = errors.New("a person has to do this")

// errNotAnOperation refuses a tool that isn't one of the agent's
// operations.
var errNotAnOperation = errors.New("that isn't one of the agent's operations")

// Why approve_plan pauses for a person.
const (
	reasonDestroy = "Removes or replaces production data or resources, which always needs a person"
	reasonBackup  = "Sets a default backup schedule, which a person who manages backups approves"
	reasonGate    = "Needs a person: "
)

// Why a promotion fails the checks no gate can lift.
const (
	failedUnrequested = "nobody asked for this release"
	failedAutonomyOff = "autonomous promotion is off for this project"
	failedUnchecked   = "the promotion checks couldn't run"
)

// The operations the table names.
const (
	approvePlan     = "approve_plan"
	retryDeployment = "retry_deployment"
	setVariable     = "set_variable"
)

// PlanReader is the planning service's plan read: *planning.Service.
type PlanReader interface {
	Get(ctx context.Context, p security.Principal, id string) (*planning.Plan, error)
}

// ProjectReader is the projects service's reads: *projects.Service.
type ProjectReader interface {
	Get(ctx context.Context, p security.Principal, id string) (*projects.Project, error)
	Environments(ctx context.Context, p security.Principal, projectID string) ([]*projects.Environment, error)
}

// Authorizer is the agent's risk table, as a cortex.ToolAuthorizer. Every
// read goes through a service as the agent principal, so it sees only
// what the agent and the person it acts for may see.
type Authorizer struct {
	Registry *agentops.Registry
	Plans    PlanReader
	Projects ProjectReader
	Authz    *security.Authorizer
	// ApprovalWindow is how long a paused approval waits:
	// planning.approval_window.
	ApprovalWindow time.Duration
	// Requested, when set, reads another stored record that names plan as
	// a release the run's person asked for, such as the agent task they
	// started for it. Nil means only the plan's own creator counts. An
	// error counts as not requested.
	Requested func(ctx context.Context, call agentops.Call, plan *planning.Plan) (bool, error)

	mu   sync.RWMutex
	gate PromotionGate
}

// SetGate installs the promotion gate. Nil puts AskFirst back.
func (a *Authorizer) SetGate(g PromotionGate) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.gate = g
}

// Gate is the promotion gate in use: AskFirst until SetGate installs one.
func (a *Authorizer) Gate() PromotionGate {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.gate == nil {
		return AskFirst{}
	}
	return a.gate
}

// Visible is what the model is shown: the agent's operations the principal
// may use. Acting for a person, an operation the agent or the person lacks
// the permission for is hidden, and a read-only principal sees only
// reads. A principal that isn't the agent sees nothing, and neither does
// a tool that isn't an operation.
func (a *Authorizer) Visible(ctx context.Context, s cortex.Subject, tools []llm.Tool) []llm.Tool {
	p, ok := s.Principal.(security.Principal)
	if !ok || !p.IsAgent() || a.Registry == nil || a.Authz == nil {
		return nil
	}
	allowed := map[security.Permission]bool{}
	may := func(perm security.Permission) bool {
		if perm == (security.Permission{}) {
			perm = security.AgentUse
		}
		ok, seen := allowed[perm]
		if !seen {
			var err error
			ok, err = a.Authz.Allowed(ctx, p, perm)
			ok = ok && err == nil
			allowed[perm] = ok
		}
		return ok
	}
	out := make([]llm.Tool, 0, len(tools))
	for _, t := range tools {
		op, ok := a.Registry.Lookup(t.Name)
		if !ok || (p.ReadOnly && op.Def.Risk.ChangesSomething()) || !may(op.Permission) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// Authorize decides one call, by its risk:
//
//   - read, change and deploy_nonproduction run; the services check the
//     agent and the person.
//   - promote_production, which only approve_plan has, runs when Promote
//     allows it, and otherwise pauses for a person for ApprovalWindow.
//   - destroy_production, which only approve_plan has, always pauses for
//     a person.
//   - secret_or_external runs for the requests to a person, which cortex
//     suspends, once agentops.ValidateRequest passes (a team named needs a
//     reason); anything else (a secret set_variable) is ErrNeedsPerson,
//     naming the request to make instead.
//
// A production retry_deployment is ErrNeedsPerson, and a plan that sets a
// default backup schedule always pauses for a person. An unknown tool, a
// principal that isn't the workspace's agent, a read-only principal
// changing something and a risk that can't be read are refused.
func (a *Authorizer) Authorize(ctx context.Context, s cortex.Subject, tc llm.ToolCall) error {
	note := noteFrom(ctx)
	op, ok := a.Registry.Lookup(tc.Name)
	if !ok {
		return fmt.Errorf("%s: %w", tc.Name, errNotAnOperation)
	}
	call, err := agentops.CallFrom(cortex.Invocation{Subject: s, Call: tc})
	if err != nil {
		return err
	}
	call.TaskID = agentops.TaskIDFrom(ctx)
	risk, projectID, err := a.Registry.Assess(ctx, call, tc.Name, json.RawMessage(tc.Arguments))
	note.set(risk, projectID)
	if err != nil {
		return err
	}
	if call.Principal.ReadOnly && risk.ChangesSomething() {
		return fmt.Errorf("%w (%s): the person the agent acts for can only read", security.ErrForbidden, tc.Name)
	}
	switch risk {
	case defs.RiskRead, defs.RiskChange, defs.RiskDeployNonProduction:
		if tc.Name == approvePlan {
			return a.approval(ctx, call, tc, risk)
		}
		return nil
	case defs.RiskPromoteProduction, defs.RiskDestroyProduction:
		switch tc.Name {
		case approvePlan:
			return a.approval(ctx, call, tc, risk)
		case retryDeployment:
			return fmt.Errorf("%w: retry production deployments from the dashboard or ask a person", ErrNeedsPerson)
		}
		return fmt.Errorf("%w: %s changes production", ErrNeedsPerson, tc.Name)
	case defs.RiskSecretOrExternal:
		if op.External() {
			return agentops.ValidateRequest(tc.Name, json.RawMessage(tc.Arguments))
		}
		if tc.Name == setVariable {
			return fmt.Errorf("%w: a secret's value is typed into Loomplane by a person: call request_secret instead", ErrNeedsPerson)
		}
		return fmt.Errorf("%w: %s touches a secret or an outside system: ask a person", ErrNeedsPerson, tc.Name)
	}
	return fmt.Errorf("%s: risk %q: %w", tc.Name, risk, errNotAnOperation)
}

// approval decides approve_plan once its risk is known.
func (a *Authorizer) approval(ctx context.Context, call agentops.Call, tc llm.ToolCall, risk defs.Risk) error {
	var in defs.ApproveInput
	if err := json.Unmarshal([]byte(tc.Arguments), &in); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	plan, err := a.Plans.Get(ctx, call.Principal, in.PlanID)
	if err != nil {
		return err
	}
	// A plan someone already decided is only reported: the handler
	// approves nothing that isn't ready. One nobody decided and nobody can
	// (superseded, expired, still planning) is refused, not left waiting
	// for a person.
	if plan.Status != planning.StatusReady {
		if plan.DecidedBy != "" {
			return nil
		}
		return fmt.Errorf("plan %s is %s: %w", plan.ID, plan.Status, planning.ErrNotReady)
	}
	if agentops.SetsBackupSchedule(plan) {
		return a.pause(reasonBackup)
	}
	switch risk {
	case defs.RiskDestroyProduction:
		return a.pause(reasonDestroy)
	case defs.RiskPromoteProduction:
		allowed, failed, err := a.Promote(ctx, call, plan)
		if err != nil {
			failed = []string{failedUnchecked}
		}
		if err != nil || !allowed {
			return a.pause(reasonGate + strings.Join(failed, "; "))
		}
	}
	return nil
}

// requested reports whether a stored record names plan as a release the
// run's person asked for: the plan's creator is that person, or Requested
// finds another record that says so. The agent acting alone has no person
// and never has a request. Nothing the model passes counts.
func (a *Authorizer) requested(ctx context.Context, call agentops.Call, plan *planning.Plan) bool {
	person, ok := call.Principal.Person()
	if !ok || person.UserID == "" {
		return false
	}
	if plan.CreatedBy == person.UserID {
		return true
	}
	if a.Requested == nil {
		return false
	}
	asked, err := a.Requested(ctx, call, plan)
	return err == nil && asked
}

func (a *Authorizer) pause(reason string) error {
	return cortex.ApprovalRequired{Reason: reason, TTL: a.ApprovalWindow}
}

// Promote decides whether the agent may approve a production plan alone.
// Before any gate, the release must be one the run's person asked for, by
// a stored record (requested), and the project's autonomous promotion
// must be on, read from the project now, never kept. Then the gate in use
// decides. A gate's error is returned and treated as not allowed. It's
// agentops.Deps.Promote, so approve_plan's handler asks the same question
// just before it approves.
func (a *Authorizer) Promote(ctx context.Context, call agentops.Call, plan *planning.Plan) (allowed bool, failed []string, err error) {
	if !a.requested(ctx, call, plan) {
		return false, []string{failedUnrequested}, nil
	}
	project, err := a.Projects.Get(ctx, call.Principal, plan.ProjectID)
	if err != nil {
		return false, nil, err
	}
	if !project.AutonomousPromotion {
		return false, []string{failedAutonomyOff}, nil
	}
	d, err := a.Gate().Evaluate(ctx, PromotionRequest{
		Principal: call.Principal, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, Plan: plan,
		TaskID: call.TaskID, RunID: call.RunID,
	})
	if err != nil {
		return false, nil, err
	}
	return d.Allowed, d.Failed, nil
}

// decisionNote is what Authorize learned about a call, for the audit entry
// and shield's risk class: Layered puts one on the context.
type decisionNote struct {
	mu      sync.Mutex
	known   bool
	risk    defs.Risk
	project string
}

type noteKey struct{}

func withNote(ctx context.Context) (context.Context, *decisionNote) {
	n := &decisionNote{}
	return context.WithValue(ctx, noteKey{}, n), n
}

// noteFrom is the context's note, or nil.
func noteFrom(ctx context.Context) *decisionNote {
	if n, ok := ctx.Value(noteKey{}).(*decisionNote); ok {
		return n
	}
	return nil
}

func (n *decisionNote) set(risk defs.Risk, project string) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.known, n.risk, n.project = risk != "", risk, project
}

func (n *decisionNote) get() (risk defs.Risk, project string, known bool) {
	if n == nil {
		return "", "", false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.risk, n.project, n.known
}

// Layered is a with shield's tool-call scan under it, the authorizer cortex
// runs. The table answers first; shield then scans the call's arguments
// under the workspace's infra-agent-tools profile, and the stricter
// answer wins, so shield can deny or ask, never allow what the table
// refused. Only approve_plan may pause the run: an approval shield alone
// asks for on any other call is ErrNeedsPerson, with shield's reason.
//
// Shield is told each call's risk class from the registry, except that the
// requests to a person (external operations) are scanned as change:
// shield's preset asks a person about secret_or_external itself, which
// would refuse every request before cortex could suspend it. Their
// arguments are still scanned for instructions and secrets, and the
// registry's class is what the audit records.
//
// Layered also records agent.tool.call for every call no handler runs:
// a refusal, a pause and a request to a person.
func Layered(a *Authorizer, shield *shieldengine.Engine) cortex.ToolAuthorizer {
	inner := shieldsafety.NewToolAuthorizer(shield, a, shieldsafety.ToolCallOptions{
		RiskClass: a.shieldClass, Profile: presets.InfraAgentTools, ApprovalTTL: a.ApprovalWindow,
		Tenant: cortex.RootTenant, App: agentscope.App,
	})
	return &layered{a: a, inner: inner}
}

type layered struct {
	a     *Authorizer
	inner cortex.ToolAuthorizer
}

func (l *layered) Visible(ctx context.Context, s cortex.Subject, tools []llm.Tool) []llm.Tool {
	return l.inner.Visible(ctx, s, tools)
}

func (l *layered) Authorize(ctx context.Context, s cortex.Subject, tc llm.ToolCall) error {
	ctx, note := withNote(ctx)
	err := l.inner.Authorize(ctx, s, tc)
	if errors.Is(err, cortex.ErrRequiresApproval) && tc.Name != approvePlan {
		reason := strings.TrimPrefix(err.Error(), cortex.ErrRequiresApproval.Error()+": ")
		var ar cortex.ApprovalRequired
		if errors.As(err, &ar) {
			reason = ar.Reason
		}
		err = fmt.Errorf("%w: %s", ErrNeedsPerson, reason)
	}
	l.record(ctx, s, tc, note, err)
	return err
}

// record writes agent.tool.call for a call no handler will record.
func (l *layered) record(ctx context.Context, s cortex.Subject, tc llm.ToolCall, note *decisionNote, err error) {
	decision := agentops.DecisionRefused
	switch {
	case err == nil:
		op, ok := l.a.Registry.Lookup(tc.Name)
		if !ok || !op.External() {
			return // its handler records it
		}
		decision = agentops.DecisionRequested
	case errors.Is(err, cortex.ErrRequiresApproval):
		decision = agentops.DecisionApproval
	}
	risk, project, _ := note.get()
	l.a.Registry.RecordDecision(ctx, cortex.Invocation{Subject: s, Call: tc}, risk, project, decision)
}

// shieldClass is the risk class shield scans a call under: the registry's,
// as Authorize computed it, but change for a request to a person. A class
// that can't be read is "", which shield's profile blocks.
func (a *Authorizer) shieldClass(ctx context.Context, s cortex.Subject, tc llm.ToolCall) string {
	op, ok := a.Registry.Lookup(tc.Name)
	if !ok {
		return ""
	}
	if op.External() {
		return string(defs.RiskChange)
	}
	if risk, _, known := noteFrom(ctx).get(); known {
		return string(risk)
	}
	call, err := agentops.CallFrom(cortex.Invocation{Subject: s, Call: tc})
	if err != nil {
		return ""
	}
	risk, err := a.Registry.Risk(ctx, call, tc.Name, json.RawMessage(tc.Arguments))
	if err != nil {
		return ""
	}
	return string(risk)
}
