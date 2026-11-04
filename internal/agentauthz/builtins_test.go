package agentauthz_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/llm"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/sqlitedriver"
	shieldengine "github.com/xraph/shield/engine"
	"github.com/xraph/shield/presets"
	"github.com/xraph/shield/profile"
	"github.com/xraph/shield/reflex"
	shieldsqlite "github.com/xraph/shield/store/sqlite"

	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/security"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

// knowledgeIn is a Knowledge that's on in orgs and off everywhere else.
func knowledgeIn(orgs ...string) func(context.Context, string) bool {
	return func(_ context.Context, org string) bool {
		for _, o := range orgs {
			if o == org {
				return true
			}
		}
		return false
	}
}

// search is knowledge_search's arguments.
func search(query string) map[string]string { return map[string]string{"query": query} }

// readAskingShield is a shield whose tools profile, in orgA, asks a person
// about every call shield is told reads, and allows everything else.
func readAskingShield(t *testing.T) *shieldengine.Engine {
	t.Helper()
	ctx := context.Background()
	drv := sqlitedriver.New()
	if err := drv.Open(ctx, ":memory:"); err != nil {
		t.Fatal(err)
	}
	db, err := grove.Open(drv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := shieldsqlite.New(db)
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	asks := presets.Bundle{
		Name: "ask-for-every-read",
		Reflexes: []*reflex.Reflex{{
			Name: "approve-read", Description: "Asks a person about every read.",
			Triggers: []reflex.Trigger{{Type: reflex.TriggerOnContext, Pattern: "risk_class=" + presets.RiskRead}},
			Actions:  []reflex.Action{{Type: reflex.ActionEscalate}},
			Enabled:  true,
		}},
		Profiles: []*profile.SafetyProfile{{Name: presets.InfraAgentTools, Reflexes: []string{"approve-read"}, Enabled: true}},
	}
	if _, err = asks.Seed(ctx, st, orgA, agentscope.App); err != nil {
		t.Fatal(err)
	}
	eng, err := shieldengine.New(shieldengine.WithStore(st))
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

// knowledge_search is shown only where the workspace's knowledge is on,
// and only to that workspace's agent; the agent's operations are shown
// either way, so it works without knowledge.
func TestKnowledgeSearchHiddenWhenOff(t *testing.T) {
	w := newWorld(t)
	tools := []llm.Tool{{Name: "list_projects"}, {Name: agentauthz.KnowledgeSearch}}
	sees := func(auth cortex.ToolAuthorizer, p any, org string) (search, ops bool) {
		t.Helper()
		for _, tl := range auth.Visible(t.Context(), cortex.Subject{Scope: agentscope.Scope{OrgID: org}.Cortex(), Principal: p}, tools) {
			switch tl.Name {
			case agentauthz.KnowledgeSearch:
				search = true
			case "list_projects":
				ops = true
			}
		}
		return search, ops
	}

	// No Knowledge: off everywhere.
	if search, ops := sees(w.a, as(orgA, ada), orgA); search || !ops {
		t.Fatalf("with no Knowledge: knowledge_search %v, list_projects %v", search, ops)
	}

	w.a.Knowledge = knowledgeIn(orgA)
	layered := w.shielded()
	for name, auth := range map[string]cortex.ToolAuthorizer{"the table": w.a, "layered": layered} {
		for who, p := range map[string]security.Principal{"an owner's agent": as(orgA, ada), "a viewer's agent": as(orgA, vi), "the agent alone": as(orgA, "")} {
			if search, ops := sees(auth, p, orgA); !search || !ops {
				t.Errorf("%s, knowledge on, %s: knowledge_search %v, list_projects %v", name, who, search, ops)
			}
		}
		if search, ops := sees(auth, as(orgB, bob), orgB); search || !ops {
			t.Errorf("%s, knowledge off: knowledge_search %v, list_projects %v", name, search, ops)
		}
		// Another workspace's agent in orgA's scope, and a person, see none of it.
		if search, _ := sees(auth, as(orgB, bob), orgA); search {
			t.Errorf("%s: another workspace's agent sees knowledge_search", name)
		}
		if search, _ := sees(auth, security.Principal{UserID: ada, OrgID: orgA}, orgA); search {
			t.Errorf("%s: a person sees knowledge_search", name)
		}
	}
	// Like whoami, which needs no permission but agent:use, it's hidden
	// from an agent acting for a read-only key, which can't hold agent:use.
	readOnly := security.Principal{UserID: ada, OrgID: orgA, ReadOnly: true}
	if search, _ := sees(w.a, security.AgentPrincipal(orgA, &readOnly), orgA); search {
		t.Error("an agent acting for a read-only key sees knowledge_search")
	}

	// The same server with knowledge off for orgA too.
	w.a.Knowledge = knowledgeIn()
	if search, ops := sees(layered, as(orgA, ada), orgA); search || !ops {
		t.Fatalf("knowledge turned off: knowledge_search %v, list_projects %v", search, ops)
	}
}

// knowledge_search is a read: allowed where knowledge is on, refused with
// why where it's off, never paused for a person.
func TestKnowledgeSearchIsARead(t *testing.T) {
	if risk, ok := agentauthz.BuiltinRisk(agentauthz.KnowledgeSearch); !ok || risk != defs.RiskRead {
		t.Fatalf("BuiltinRisk(knowledge_search) = %q, %v", risk, ok)
	}
	for _, name := range []string{"list_projects", "agent_send", "drop_database", ""} {
		if risk, ok := agentauthz.BuiltinRisk(name); ok {
			t.Errorf("BuiltinRisk(%q) = %q, true", name, risk)
		}
	}

	w := newWorld(t)
	off := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, agentauthz.ErrKnowledgeOff) || !strings.Contains(err.Error(), "knowledge is off for this workspace") || errors.Is(err, cortex.ErrRequiresApproval) {
			t.Errorf("%s: %v, want knowledge off", name, err)
		}
	}
	off("no Knowledge", w.authorize(w.a, as(orgA, ada), agentauthz.KnowledgeSearch, search("canary")))

	w.a.Knowledge = knowledgeIn(orgA)
	layered := w.shielded()
	readOnly := security.Principal{UserID: ada, OrgID: orgA, ReadOnly: true}
	for name, auth := range map[string]cortex.ToolAuthorizer{"the table": w.a, "layered": layered} {
		for who, p := range map[string]security.Principal{
			"an owner's agent": as(orgA, ada), "a viewer's agent": as(orgA, vi), "the agent alone": as(orgA, ""),
		} {
			if err := w.authorize(auth, p, agentauthz.KnowledgeSearch, search("canary thresholds")); err != nil {
				t.Errorf("%s, knowledge on, %s: %v", name, who, err)
			}
		}
		off(name+", knowledge off", w.authorize(auth, as(orgB, bob), agentauthz.KnowledgeSearch, search("canary thresholds")))

		// Another workspace's agent in orgA's scope, a person, and an agent
		// acting for a read-only key (no agent:use) are refused.
		for who, p := range map[string]any{
			"another workspace's agent": as(orgB, bob), "a person": security.Principal{UserID: ada, OrgID: orgA},
			"a read-only key's agent": security.AgentPrincipal(orgA, &readOnly),
		} {
			err := auth.Authorize(t.Context(), cortex.Subject{Scope: agentscope.Scope{OrgID: orgA}.Cortex(), Principal: p}, toolCall(t, agentauthz.KnowledgeSearch, search("canary")))
			if err == nil || errors.Is(err, cortex.ErrRequiresApproval) {
				t.Errorf("%s, %s: %v", name, who, err)
			}
		}
	}

	// A refusal is in the audit, as a read.
	before := len(w.audit.toolCalls())
	off("layered, knowledge off", w.authorize(layered, as(orgB, bob), agentauthz.KnowledgeSearch, search("canary")))
	events := w.audit.toolCalls()[before:]
	if len(events) != 1 || events[0].Attributes["risk"] != string(defs.RiskRead) || events[0].Attributes["outcome"] != "forbidden" {
		t.Fatalf("audit %+v", events)
	}
}

// Shield scans knowledge_search as a read: its tools profile blocks a call
// it can't class, and a profile that asks about reads asks about it.
func TestShieldSeesKnowledgeSearchAsARead(t *testing.T) {
	w := newWorld(t)
	w.a.Knowledge = knowledgeIn(orgA, orgB)
	call := toolCall(t, agentauthz.KnowledgeSearch, search("canary thresholds"))
	if got := w.a.ShieldClass(t.Context(), subject(as(orgA, ada), orgA), call); got != presets.RiskRead {
		t.Fatalf("shield's class for knowledge_search: %q, want read", got)
	}

	// The preset blocks a call with no class, and allows a read.
	if err := w.authorize(w.shielded(), as(orgA, ada), agentauthz.KnowledgeSearch, search("canary thresholds")); err != nil {
		t.Fatalf("under the preset: %v", err)
	}

	// A profile that asks about reads asks about knowledge_search, as it
	// does about list_projects, and not about a change.
	asks := agentauthz.Layered(w.a, readAskingShield(t))
	for name, args := range map[string]any{agentauthz.KnowledgeSearch: search("canary thresholds"), "list_projects": nil} {
		err := w.authorize(asks, as(orgA, ada), name, args)
		if !errors.Is(err, agentauthz.ErrNeedsPerson) || !strings.Contains(err.Error(), "shield") {
			t.Errorf("%s under a profile that asks about reads: %v", name, err)
		}
	}
	if err := w.authorize(asks, as(orgA, ada), "create_project", map[string]string{"name": "Ledger"}); err != nil {
		t.Errorf("create_project under a profile that asks about reads: %v", err)
	}
}

// Every knowledge_search is in the audit, as the workspace's agent and the
// person it acts for, under its own name: allowed ones too, since no
// handler runs a builtin to record it.
func TestKnowledgeSearchIsAudited(t *testing.T) {
	w := newWorld(t)
	w.a.Knowledge = knowledgeIn(orgA)
	layered := w.shielded()
	for _, c := range []struct {
		name          string
		p             security.Principal
		org, person   string
		decision, out string
	}{
		{"allowed", as(orgA, ada), orgA, ada, "allowed", "success"},
		{"refused", as(orgB, bob), orgB, bob, "forbidden", "denied"},
	} {
		before := len(w.audit.toolCalls())
		err := w.authorize(layered, c.p, agentauthz.KnowledgeSearch, search("canary thresholds"))
		if (err == nil) != (c.decision == "allowed") {
			t.Fatalf("%s: %v", c.name, err)
		}
		events := w.audit.toolCalls()[before:]
		if len(events) != 1 {
			t.Fatalf("%s: audit %+v", c.name, events)
		}
		e := events[0]
		person, ok := e.Principal.Person()
		if e.Attributes["tool"] != agentauthz.KnowledgeSearch || e.Attributes["risk"] != string(defs.RiskRead) ||
			e.Attributes["outcome"] != c.decision || e.Outcome != c.out ||
			!e.Principal.IsAgent() || e.Principal.OrgID != c.org || !ok || person.UserID != c.person {
			t.Errorf("%s: audit %+v", c.name, e)
		}
	}
	// A tool that's neither an operation nor a builtin is still "unknown".
	before := len(w.audit.toolCalls())
	if err := w.authorize(layered, as(orgA, ada), "agent_send", map[string]string{"to": "x"}); err == nil {
		t.Fatal("agent_send was allowed")
	}
	if events := w.audit.toolCalls()[before:]; len(events) != 1 || events[0].Attributes["tool"] != "unknown" {
		t.Fatalf("agent_send audit %+v", events)
	}
}
