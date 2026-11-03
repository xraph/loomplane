package agentops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/engine"
	cortexid "github.com/xraph/cortex/id"
	"github.com/xraph/cortex/llm"

	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/security"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

// later are agent definitions this package doesn't serve yet.
var later []string

func panicsWith(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Fatalf("no panic, want one naming %q", want)
		}
		if msg := fmt.Sprint(r); !strings.Contains(msg, want) {
			t.Fatalf("panic %q doesn't name %q", msg, want)
		}
	}()
	fn()
}

func TestRegisterRejectsDuplicatesAndMismatches(t *testing.T) {
	noop := func(context.Context, Call, defs.ProjectInput) (string, error) { return "", nil }
	t.Run("duplicate", func(t *testing.T) {
		r := NewRegistry()
		Register(r, def("get_manifest"), security.ProjectRead, noop)
		panicsWith(t, "get_manifest", func() { Register(r, def("get_manifest"), security.ProjectRead, noop) })
	})
	t.Run("another input type", func(t *testing.T) {
		panicsWith(t, "get_plan", func() { Register(NewRegistry(), def("get_plan"), security.DeploymentRead, noop) })
	})
	t.Run("external through Register", func(t *testing.T) {
		panicsWith(t, "ask_person", func() {
			Register(NewRegistry(), def("ask_person"), security.Permission{}, func(context.Context, Call, defs.AskPersonInput) (string, error) { return "", nil })
		})
	})
	t.Run("in-process through RegisterExternal", func(t *testing.T) {
		panicsWith(t, "get_manifest", func() { RegisterExternal(NewRegistry(), def("get_manifest"), time.Hour) })
	})
	t.Run("risk from the input without RiskFrom", func(t *testing.T) {
		panicsWith(t, "set_variable", func() {
			Register(NewRegistry(), def("set_variable"), security.VariableManage, func(context.Context, Call, defs.SetVariableInput) (string, error) { return "", nil })
		})
	})
	t.Run("RiskFrom on a fixed risk", func(t *testing.T) {
		panicsWith(t, "get_manifest", func() {
			Register(NewRegistry(), def("get_manifest"), security.ProjectRead, noop,
				RiskFrom(func(context.Context, Call, defs.ProjectInput) (defs.Risk, error) { return defs.RiskRead, nil }))
		})
	})
}

func TestEngineOptionsBuildEveryTool(t *testing.T) {
	w := newWorld(t)
	eng, err := engine.New(w.reg.EngineOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]engine.ToolInfo{}
	for _, info := range eng.Tools() {
		if info.Kind != engine.ToolBuiltin {
			seen[info.Def.Name] = info
		}
	}
	ops := w.reg.Ops()
	if len(seen) != len(ops) {
		t.Errorf("cortex has %d host tools, the registry %d operations", len(seen), len(ops))
	}
	for _, op := range ops {
		info, ok := seen[op.Def.Name]
		if !ok {
			t.Errorf("cortex doesn't have %s", op.Def.Name)
			continue
		}
		if info.RetrySafe != op.Def.ReadOnly {
			t.Errorf("%s: retry-safe %v, read-only %v", op.Def.Name, info.RetrySafe, op.Def.ReadOnly)
		}
		wantKind := engine.ToolRegistered
		if op.Def.External {
			wantKind = engine.ToolExternal
		}
		if info.Kind != wantKind {
			t.Errorf("%s: kind %s, want %s", op.Def.Name, info.Kind, wantKind)
		}
	}
}

func TestBuiltinsCoverEveryAgentDefinition(t *testing.T) {
	w := newWorld(t)
	for _, d := range defs.All() {
		if d.Surfaces&defs.SurfaceAgent == 0 {
			continue
		}
		op, ok := w.reg.Lookup(d.Name)
		if slices.Contains(later, d.Name) {
			if ok {
				t.Errorf("%s is registered; take it off later", d.Name)
			}
			continue
		}
		if !ok {
			t.Errorf("%s isn't registered", d.Name)
			continue
		}
		if op.External() != d.External {
			t.Errorf("%s: external %v, its definition %v", d.Name, op.External(), d.External)
		}
		if op.External() && op.ttl != QuestionTTL {
			t.Errorf("%s waits %s, want %s", d.Name, op.ttl, QuestionTTL)
		}
	}
	for _, op := range w.reg.Ops() {
		if d, ok := defs.Lookup(op.Def.Name); !ok || d.Surfaces&defs.SurfaceAgent == 0 {
			t.Errorf("%s is registered but isn't an agent definition", op.Def.Name)
		}
	}
}

func TestCallFromRefusesNonAgentPrincipals(t *testing.T) {
	scope := agentscope.Scope{OrgID: orgA}.Cortex()
	inv := func(p any, s cortex.Scope) cortex.Invocation {
		return cortex.Invocation{Subject: cortex.Subject{Scope: s, Principal: p}, Call: llm.ToolCall{ID: "c1", Name: "whoami"}}
	}
	for name, in := range map[string]cortex.Invocation{
		"a person":                     inv(security.Principal{UserID: ada, OrgID: orgA}, scope),
		"the automation":               inv(security.SystemPrincipal(orgA), scope),
		"a string":                     inv("agent:"+orgA, scope),
		"no principal":                 inv(nil, scope),
		"a pointer":                    inv(new(security.AgentPrincipal(orgA, nil)), scope),
		"another org's agent":          inv(security.AgentPrincipal(orgB, nil), scope),
		"an agent with a forged ID":    inv(security.Principal{Kind: security.KindAgent, UserID: security.AgentSubject(orgB), OrgID: orgA}, scope),
		"for another org's person":     inv(security.Principal{Kind: security.KindAgent, UserID: security.AgentSubject(orgA), OrgID: orgA, OnBehalfOf: person(orgB, bob)}, scope),
		"a scope that isn't Loomplane": inv(security.AgentPrincipal(orgA, nil), cortex.Scope{Levels: []cortex.Level{{Key: "tenant", Value: orgA}}}),
	} {
		if _, err := CallFrom(in); err == nil {
			t.Errorf("%s: CallFrom accepted it", name)
		}
	}
	runID := cortexid.NewAgentRunID()
	call, err := CallFrom(cortex.Invocation{
		Subject: cortex.Subject{Scope: agentscope.Scope{OrgID: orgA, ProjectID: "proj_1"}.Cortex(), Principal: as(orgA, ada), RunID: runID},
		Call:    llm.ToolCall{ID: "c1", Name: "whoami"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if call.Principal.UserID != security.AgentSubject(orgA) || call.Scope.ProjectID != "proj_1" || call.RunID != runID.String() || call.CallID != "c1" {
		t.Fatalf("call %+v", call)
	}
}

func TestToolCallsAreAudited(t *testing.T) {
	w := newWorld(t)
	p := w.project(orgA, ada, "Mono")
	ctx := WithTaskID(t.Context(), "atask_1")
	if _, err := w.call(ctx, as(orgA, ada), agentscope.Scope{OrgID: orgA}, "list_variables", map[string]string{"project": "mono"}); err != nil {
		t.Fatal(err)
	}
	res, err := w.call(ctx, as(orgA, vi), agentscope.Scope{OrgID: orgA}, "set_variable", map[string]any{"project": "mono", "key": "TOKEN", "value": "hunter2"})
	if err != nil || res != "not allowed: variable:manage" {
		t.Fatalf("a viewer's set_variable: %q, %v", res, err)
	}
	events := w.audit.toolCalls()
	if len(events) != 2 {
		t.Fatalf("%d agent.tool.call events, want 2: %+v", len(events), events)
	}
	for i, want := range []struct{ tool, risk, outcome, result string }{
		{"list_variables", "read", "success", "success"},
		{"set_variable", "change", "forbidden", "denied"},
	} {
		e := events[i]
		if e.Attributes["tool"] != want.tool || e.Attributes["risk"] != want.risk || e.Attributes["outcome"] != want.outcome || e.Outcome != want.result {
			t.Errorf("event %d: %+v, want %+v", i, e, want)
		}
		if e.ProjectID != p.ID {
			t.Errorf("event %d names project %q, want %q", i, e.ProjectID, p.ID)
		}
		if !e.Principal.IsAgent() {
			t.Errorf("event %d is recorded as %+v", i, e.Principal)
		}
		raw, _ := json.Marshal(e.Attributes)
		if strings.Contains(string(raw), "hunter2") || strings.Contains(string(raw), "mono") || len(e.Attributes) != 3 {
			t.Errorf("event %d carries more than tool, risk and outcome: %s", i, raw)
		}
	}
	// The services saw the call through the context too.
	for _, c := range w.audit.calls {
		if c.TaskID != "atask_1" || c.RunID == "" || c.CallID == "" || (c.Tool != "list_variables" && c.Tool != "set_variable") {
			t.Errorf("agent call on the context: %+v", c)
		}
	}
}

func TestRiskIsStaticOrComputed(t *testing.T) {
	w := newWorld(t)
	call := Call{Principal: as(orgA, ada), Scope: agentscope.Scope{OrgID: orgA}}
	for name, want := range map[string]defs.Risk{"get_plan": defs.RiskRead, "ask_person": defs.RiskSecretOrExternal, "create_project": defs.RiskChange} {
		if got, err := w.reg.Risk(t.Context(), call, name, nil); err != nil || got != want {
			t.Errorf("%s: %q, %v, want %q", name, got, err, want)
		}
	}
	if got, err := w.reg.Risk(t.Context(), call, "set_variable", json.RawMessage(`{"project":"p","key":"K","value":"v","secret":true}`)); err != nil || got != defs.RiskSecretOrExternal {
		t.Errorf("a secret set_variable: %q, %v", got, err)
	}
	if got, err := w.reg.Risk(t.Context(), call, "set_variable", json.RawMessage(`{"project":"p","key":"K","value":"v"}`)); err != nil || got != defs.RiskChange {
		t.Errorf("a plain set_variable: %q, %v", got, err)
	}
	if _, err := w.reg.Risk(t.Context(), call, "drop_database", nil); err == nil {
		t.Error("an unknown operation has a risk")
	}
}

func TestExternalOperationsHaveNoHandler(t *testing.T) {
	w := newWorld(t)
	_, err := w.call(t.Context(), as(orgA, ada), agentscope.Scope{OrgID: orgA}, "ask_person", map[string]string{"question": "Which region?"})
	if err == nil || errors.Is(err, security.ErrForbidden) {
		t.Fatalf("ask_person ran in-process: %v", err)
	}
}

// A call CallFrom refuses is audited too, under the principal it came with.
func TestRefusedCallsAreAudited(t *testing.T) {
	w := newWorld(t)
	if _, err := w.call(t.Context(), as(orgA, ada), agentscope.Scope{OrgID: orgB}, "list_projects", nil); err == nil {
		t.Fatal("org A's agent ran under org B's scope")
	}
	if _, err := w.call(t.Context(), security.Principal{UserID: ada, OrgID: orgA}, agentscope.Scope{OrgID: orgA}, "list_projects", nil); err == nil {
		t.Fatal("a person called an operation")
	}
	events := w.audit.toolCalls()
	if len(events) != 2 {
		t.Fatalf("%d agent.tool.call events, want 2: %+v", len(events), events)
	}
	for i, e := range events {
		if e.Attributes["tool"] != "list_projects" || e.Attributes["outcome"] != "forbidden" || e.Outcome != "denied" || e.Principal.OrgID != orgA {
			t.Errorf("event %d: %+v", i, e)
		}
	}
}

// A refused call to a tool that doesn't exist is audited as "unknown":
// the model's name for it is unbounded text nobody scanned.
func TestUnknownToolsAreAuditedWithoutTheirName(t *testing.T) {
	w := newWorld(t)
	name := "drop_everything\x1b]8;;https://x\x07" + strings.Repeat("x", 5000)
	w.reg.RecordDecision(t.Context(), cortex.Invocation{
		Subject: cortex.Subject{Scope: agentscope.Scope{OrgID: orgA}.Cortex(), Principal: as(orgA, ada), RunID: cortexid.NewAgentRunID()},
		Call:    llm.ToolCall{ID: "c1", Name: name},
	}, "", "", DecisionRefused)
	events := w.audit.toolCalls()
	if len(events) != 1 || events[0].Attributes["tool"] != "unknown" {
		t.Fatalf("events %+v", events)
	}
}
