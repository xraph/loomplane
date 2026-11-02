// Package agentops gives the built-in agent Loomplane's operations as
// in-process cortex tools. Each operation is a definition from
// pkg/agentops (its name, description, input and risk) and a handler that
// calls the same service the matching HTTP route calls, as the agent
// principal, so every service checks both the agent and the person it acts
// for. Nothing here reads a store a service already guards.
package agentops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/engine"
	"github.com/xraph/cortex/llm"
	"github.com/xraph/cortex/tool"

	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/page"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

// Op is one operation the agent can call.
type Op struct {
	Def defs.Definition
	// Permission is what the person must hold to be shown the operation.
	// The zero value means security.AgentUse.
	Permission security.Permission
	// Tool is what cortex advertises to the model.
	Tool llm.Tool

	handler engine.ToolHandler // nil for an external operation
	risk    func(ctx context.Context, call Call, args json.RawMessage) (defs.Risk, error)
	ttl     time.Duration // an external operation's wait for a person
}

// External reports whether a person answers the operation, outside the run.
func (o Op) External() bool { return o.Def.External }

// opConfig is what an Option sets.
type opConfig struct {
	risk func(ctx context.Context, call Call, args json.RawMessage) (defs.Risk, error)
}

// Option changes how Register builds an operation.
type Option func(*opConfig)

// RiskFrom computes the operation's risk from its input, for a definition
// built with defs.FromInput. The input is decoded as the handler's is.
func RiskFrom[In any](fn func(ctx context.Context, call Call, in In) (defs.Risk, error)) Option {
	return func(c *opConfig) {
		c.risk = func(ctx context.Context, call Call, args json.RawMessage) (defs.Risk, error) {
			var in In
			if len(args) > 0 {
				if err := json.Unmarshal(args, &in); err != nil {
					return "", fmt.Errorf("invalid arguments: %w", err)
				}
			}
			r, err := fn(ctx, call, in)
			if err != nil {
				return "", err
			}
			if !r.Valid() {
				return "", fmt.Errorf("risk %q isn't a risk class", r)
			}
			return r, nil
		}
	}
}

// Registry holds the agent's operations by name. Register them all before
// EngineOptions builds cortex's tools: cortex fixes its tools when it
// builds its engine.
type Registry struct {
	mu  sync.RWMutex
	ops map[string]Op
	// audit records each call; Builtins points it at Deps.Audit.
	audit func() projects.Auditor
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{ops: map[string]Op{}}
}

// SetAuditor makes every call record agent.tool.call through the auditor
// fn returns at call time. Builtins sets it to Deps.Audit.
func (r *Registry) SetAuditor(fn func() projects.Auditor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.audit = fn
}

func (r *Registry) auditor() projects.Auditor {
	r.mu.RLock()
	fn := r.audit
	r.mu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// add stores op, refusing a name already taken.
func (r *Registry) add(op Op) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.ops[op.Def.Name]; dup {
		panic(fmt.Sprintf("agentops: %s is registered twice", op.Def.Name))
	}
	r.ops[op.Def.Name] = op
}

// Register adds an operation run in-process by fn. It panics on a name
// already registered, an In other than def's input type, an external
// definition (use RegisterExternal), and a definition whose risk comes from
// its input without a RiskFrom, or the other way round.
//
// The handler cortex runs decodes and validates the arguments, reads the
// Call from the invocation (CallFrom), marks the context with the agent's
// call for the audit log, runs fn and records agent.tool.call. A service
// refusing the call (security.ErrForbidden) comes back to the model as
// "not allowed: <permission>", a result rather than a failed call.
func Register[In, Out any](r *Registry, def defs.Definition, perm security.Permission, fn func(ctx context.Context, call Call, in In) (Out, error), opts ...Option) {
	name := def.Name
	if def.External {
		panic(fmt.Sprintf("agentops: %s is external; register it with RegisterExternal", name))
	}
	if got, want := reflect.TypeFor[In](), def.InputType(); got != want {
		panic(fmt.Sprintf("agentops: %s's handler takes %v, its definition %v", name, got, want))
	}
	var cfg opConfig
	for _, o := range opts {
		o(&cfg)
	}
	switch {
	case def.RiskFromInput && cfg.risk == nil:
		panic(fmt.Sprintf("agentops: %s computes its risk from its input; give it RiskFrom", name))
	case !def.RiskFromInput && cfg.risk != nil:
		panic(fmt.Sprintf("agentops: %s has a fixed risk; RiskFrom is for definitions built with FromInput", name))
	}
	risk := cfg.risk
	if risk == nil {
		static := def.Risk
		risk = func(context.Context, Call, json.RawMessage) (defs.Risk, error) { return static, nil }
	}
	op := Op{Def: def, Permission: perm, risk: risk}
	op.Tool, op.handler = tool.New(name, def.Description, func(ctx context.Context, inv cortex.Invocation, in In) (string, error) {
		return r.run(ctx, inv, op, func(ctx context.Context, call Call) (any, error) {
			return fn(ctx, call, in)
		})
	})
	r.add(op)
}

// RegisterExternal adds an operation a person answers through
// agentrequests: cortex pauses the run on it until the result is handed
// back, or ttl passes.
func RegisterExternal(r *Registry, def defs.Definition, ttl time.Duration) {
	if !def.External {
		panic(fmt.Sprintf("agentops: %s isn't external; register it with Register", def.Name))
	}
	params, err := def.Schema()
	if err != nil {
		panic(fmt.Sprintf("agentops: %v", err))
	}
	static := def.Risk
	r.add(Op{
		Def: def, ttl: ttl,
		Tool: llm.Tool{Name: def.Name, Description: def.Description, Parameters: params},
		risk: func(context.Context, Call, json.RawMessage) (defs.Risk, error) { return static, nil },
	})
}

// Lookup returns the operation called name.
func (r *Registry) Lookup(name string) (Op, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	op, ok := r.ops[name]
	return op, ok
}

// Ops returns every operation, sorted by name.
func (r *Registry) Ops() []Op {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Op, 0, len(r.ops))
	for _, op := range r.ops {
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Def.Name < out[j].Def.Name })
	return out
}

// Risk is the risk of calling name with args: the definition's, or the one
// its RiskFrom computes. An unknown name is an error.
func (r *Registry) Risk(ctx context.Context, call Call, name string, args json.RawMessage) (defs.Risk, error) {
	op, ok := r.Lookup(name)
	if !ok {
		return "", fmt.Errorf("agentops: unknown operation %q", name)
	}
	return op.risk(ctx, call, args)
}

// EngineOptions give cortex every operation: a tool with its handler for
// each in-process one, marked retry-safe when it only reads, and an
// external tool, with its own wait, for each a person answers.
func (r *Registry) EngineOptions() []engine.Option {
	ops := r.Ops()
	out := make([]engine.Option, 0, len(ops)+1)
	var safe []string
	for _, op := range ops {
		if op.External() {
			out = append(out, engine.WithExternalTool(op.Tool, engine.ExternalTTL(op.ttl)))
			continue
		}
		out = append(out, engine.WithTool(op.Tool, op.handler))
		if op.Def.ReadOnly {
			safe = append(safe, op.Def.Name)
		}
	}
	if len(safe) > 0 {
		out = append(out, engine.WithToolRetrySafe(safe...))
	}
	return out
}

// Invoke runs the call inv names with the handler cortex runs for it. An
// external operation has no handler: a person answers it.
func (r *Registry) Invoke(ctx context.Context, inv cortex.Invocation) (string, error) {
	op, ok := r.Lookup(inv.Call.Name)
	if !ok {
		return "", fmt.Errorf("agentops: unknown operation %q", inv.Call.Name)
	}
	if op.handler == nil {
		return "", fmt.Errorf("agentops: a person answers %s, outside the run", op.Def.Name)
	}
	return op.handler(ctx, inv)
}

// run is what every in-process handler does around its operation.
func (r *Registry) run(ctx context.Context, inv cortex.Invocation, op Op, fn func(ctx context.Context, call Call) (any, error)) (string, error) {
	call, err := CallFrom(inv)
	if err != nil {
		return "", err
	}
	call.TaskID = TaskIDFrom(ctx)
	ctx = security.WithAgentCall(ctx, security.AgentCall{TaskID: call.TaskID, RunID: call.RunID, Tool: op.Def.Name, CallID: call.CallID})
	state := &callState{}
	ctx = context.WithValue(ctx, callStateKey{}, state)

	risk, riskErr := op.risk(ctx, call, json.RawMessage(inv.Call.Arguments))
	if riskErr != nil {
		r.record(ctx, call, op, state, "", riskErr)
		return "", riskErr
	}
	out, err := fn(ctx, call)
	r.record(ctx, call, op, state, risk, err)
	if err != nil {
		if errors.Is(err, security.ErrForbidden) {
			return notAllowed(err, op.Permission), nil
		}
		return "", err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("%s: encode the result: %w", op.Def.Name, err)
	}
	return string(raw), nil
}

// record writes the call's agent.tool.call audit entry: the tool, its risk
// and how it went, never its arguments.
func (r *Registry) record(ctx context.Context, call Call, op Op, state *callState, risk defs.Risk, err error) {
	a := r.auditor()
	if a == nil {
		return
	}
	kind := ErrorKind(err)
	outcome := "success"
	switch kind {
	case "success":
	case "forbidden":
		outcome = "denied"
	default:
		outcome = "failure"
	}
	severity := "info"
	if kind == "forbidden" {
		severity = "warning"
	}
	a.Record(context.WithoutCancel(ctx), projects.AuditEvent{
		Action: "agent.tool.call", Severity: severity, Outcome: outcome,
		ProjectID: state.project(), Principal: call.Principal,
		Attributes: map[string]any{"tool": op.Def.Name, "risk": string(risk), "outcome": kind},
	})
}

// notAllowed is the model's answer to a call a service refused.
func notAllowed(err error, fallback security.Permission) string {
	if perm := refusedPermission(err.Error()); perm != "" {
		return "not allowed: " + perm
	}
	if fallback != (security.Permission{}) {
		return "not allowed: " + fallback.Name()
	}
	return "not allowed: " + err.Error()
}

// refusedPermission reads the permission an Authorizer refusal names: its
// text carries "(resource:action)".
func refusedPermission(msg string) string {
	for rest := msg; ; {
		open := strings.IndexByte(rest, '(')
		if open < 0 {
			return ""
		}
		rest = rest[open+1:]
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			return ""
		}
		candidate := rest[:end]
		if res, action, ok := strings.Cut(candidate, ":"); ok && isWord(res) && isWord(action) {
			return candidate
		}
	}
}

func isWord(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(c rune) bool { return (c < 'a' || c > 'z') && c != '_' })
}

// Deps are the services the built-in operations call. Each field is the
// part of a Loomplane service the operations use; the real service
// satisfies it. Builtins reads them at call time, so they may be filled
// after it runs.
type Deps struct {
	Projects     ProjectService
	Environments VariableService
	Discovery    DiscoveryService
	GitHub       GitHubService
	Manifests    ManifestService
	Plans        PlanService
	Deployments  DeploymentService
	Logs         LogStore
	Authz        Authorizer
	Safety       *agentsafety.Scanner
	Audit        projects.Auditor
	// Health reads a binding's traffic from its cloud's monitoring. It
	// reads without a principal, so binding_metrics checks
	// infrastructure:read on the project itself first.
	Health releasehealth.Reader
	// Bindings looks a binding up by ID in an organization, without a
	// principal: *planning.PgStore.
	Bindings BindingLookup
	Watches  WatchService
}

// BindingLookup is *planning.PgStore's binding read.
type BindingLookup interface {
	GetBinding(ctx context.Context, orgID, id string) (*planning.Binding, error)
}

// WatchService is *releasewatch.Service.
type WatchService interface {
	ForDeployment(ctx context.Context, p security.Principal, deploymentID string) (*releasewatch.Watch, error)
}

// ProjectService is *projects.Service.
type ProjectService interface {
	List(ctx context.Context, p security.Principal) ([]*projects.Project, error)
	Create(ctx context.Context, p security.Principal, in projects.CreateProjectInput) (*projects.Project, error)
	Environments(ctx context.Context, p security.Principal, projectID string) ([]*projects.Environment, error)
	CreateEnvironment(ctx context.Context, p security.Principal, projectID string, in projects.CreateEnvironmentInput) (*projects.Environment, error)
}

// VariableService is *environments.Service.
type VariableService interface {
	ListProject(ctx context.Context, p security.Principal, projectID string) ([]*environments.Variable, error)
	CreateProject(ctx context.Context, p security.Principal, projectID string, in environments.CreateVariableInput) (*environments.Variable, error)
}

// DiscoveryService is *discovery.Service.
type DiscoveryService interface {
	Start(ctx context.Context, p security.Principal, projectID string, in discovery.StartAnalysisInput) ([]*discovery.Analysis, error)
	Get(ctx context.Context, p security.Principal, id string) (*discovery.Analysis, error)
	AddSource(ctx context.Context, p security.Principal, projectID string, in discovery.AddSourceInput) (*discovery.Source, error)
}

// GitHubService is *repositories.Connections.
type GitHubService interface {
	Status(ctx context.Context, p security.Principal) (*repositories.GitHubStatus, error)
	Repositories(ctx context.Context, p security.Principal) ([]repositories.ConnectedRepository, error)
}

// ManifestService is *manifests.Service.
type ManifestService interface {
	Current(ctx context.Context, p security.Principal, projectID string) (*manifests.Revision, error)
	Save(ctx context.Context, p security.Principal, projectID string, in manifests.SaveInput) (*manifests.Revision, bool, error)
}

// PlanService is *planning.Service.
type PlanService interface {
	Create(ctx context.Context, p security.Principal, projectID string, in planning.CreatePlanInput) (*planning.Plan, error)
	List(ctx context.Context, p security.Principal, projectID, environment string, before page.Cursor, limit int) ([]*planning.Plan, page.Cursor, error)
	Get(ctx context.Context, p security.Principal, id string) (*planning.Plan, error)
	Reject(ctx context.Context, p security.Principal, id string, in planning.DecideInput) (*planning.Plan, error)
	Bindings(ctx context.Context, p security.Principal, projectID, environment string) ([]*planning.Binding, error)
	Deployed(ctx context.Context, p security.Principal, projectID, environment string) ([]*planning.DeployedRepository, error)
	DraftManifest(ctx context.Context, p security.Principal, projectID string) (*planning.ManifestDraft, error)
	RollbackTarget(ctx context.Context, p security.Principal, projectID, environment, deploymentID string) (*planning.RollbackTarget, error)
	CreateRollback(ctx context.Context, p security.Principal, projectID string, in planning.RollbackInput) (*planning.Plan, error)
}

// DeploymentService is *deployments.Service.
type DeploymentService interface {
	List(ctx context.Context, p security.Principal, projectID string, before page.Cursor, limit int) ([]*deployments.Deployment, page.Cursor, error)
	Get(ctx context.Context, p security.Principal, id string) (*deployments.Deployment, error)
	ForPlan(ctx context.Context, p security.Principal, planID string) (*deployments.Deployment, error)
	Retry(ctx context.Context, p security.Principal, id string) (*deployments.Deployment, error)
	Canary(ctx context.Context, p security.Principal, deploymentID string) (*deployments.CanaryReport, error)
	RecentCanaries(ctx context.Context, p security.Principal, projectID, environmentID string, limit int) ([]deployments.CanaryRun, error)
}

// LogStore is *realtime.PgStore. It reads without a principal, so
// deployment_logs reads the deployment through DeploymentService first.
type LogStore interface {
	Logs(ctx context.Context, org, deployment, after string, limit int) (realtime.LogPage, error)
}

// Authorizer is *security.Authorizer.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
	Allowed(ctx context.Context, p security.Principal, perm security.Permission) (bool, error)
}
