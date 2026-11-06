package aiagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/agentchat"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/security"
)

func testFlag(id string) *agentchat.Flag {
	at := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	return &agentchat.Flag{ID: id, OrgID: "org_1", ConversationID: "ses_1", TaskID: "atask_1", ReportedBy: "usr_ada",
		Reason: "offensive", Note: "rude", Question: "why?", Answer: "because", CreatedAt: at, UpdatedAt: at}
}

func (f *fakeChat) Flag(_ context.Context, _ security.Principal, id, taskID string, in agentchat.FlagInput) (*agentchat.Flag, error) {
	f.mu.Lock()
	f.flag = in
	f.mu.Unlock()
	if err := f.record("flag " + id + " " + taskID); err != nil {
		return nil, err
	}
	fl := testFlag("aflag_1")
	fl.ConversationID, fl.TaskID, fl.Reason, fl.Note = id, taskID, string(in.Reason), in.Note
	return fl, nil
}

func (f *fakeChat) Flags(_ context.Context, status string) ([]agentchat.Flag, error) {
	f.mu.Lock()
	f.status = status
	f.mu.Unlock()
	if err := f.record("flags"); err != nil {
		return nil, err
	}
	return []agentchat.Flag{*testFlag("aflag_2"), *testFlag("aflag_1")}, nil
}

func (f *fakeChat) DismissFlag(_ context.Context, id, by string) (*agentchat.Flag, error) {
	f.mu.Lock()
	f.by = by
	f.mu.Unlock()
	if err := f.record("dismiss " + id); err != nil {
		return nil, err
	}
	fl := testFlag(id)
	at := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	fl.DismissedBy, fl.DismissedAt = by, &at
	return fl, nil
}

const flagPath = "/api/v1/agent/conversations/ses_1/turns/atask_1/flag"

func TestFlagRouteFlagsTheTurn(t *testing.T) {
	r, chat, _ := chatRig(t, true)
	out := serve(t, r, http.MethodPost, flagPath, `{"reason":"inaccurate","note":"wrong region"}`, asAda())
	var got agentchat.Flag
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || out.Code != http.StatusOK {
		t.Fatalf("flag: %d %s", out.Code, out.Body.String())
	}
	if chat.last() != "flag ses_1 atask_1" || chat.flag != (agentchat.FlagInput{Reason: agentchat.FlagInaccurate, Note: "wrong region"}) {
		t.Fatalf("called %q with %+v", chat.last(), chat.flag)
	}
	// The reporter sees their whole flag back.
	if got.ID != "aflag_1" || got.Reason != "inaccurate" || got.Note != "wrong region" || got.Question != "why?" || got.Answer != "because" {
		t.Fatalf("flag %+v", got)
	}
	if out = serve(t, r, http.MethodPost, flagPath, `{"reason":`, asAda()); out.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", out.Code)
	}
	if out = serve(t, r, http.MethodPost, flagPath, `{"reason":"offensive"}`, nil); out.Code != http.StatusUnauthorized {
		t.Fatalf("no principal: %d", out.Code)
	}
}

// A conversation the caller can't read is the same 404 as one that isn't
// there, in any workspace: the service answers ErrNotFound for both, and
// the response says nothing more.
func TestFlagRouteErrors(t *testing.T) {
	r, chat, _ := chatRig(t, true)
	for _, c := range []struct {
		name   string
		err    error
		status int
		says   string
	}{
		{"someone else's conversation, or none", agentchat.ErrNotFound, http.StatusNotFound, agentchat.ErrNotFound.Error()},
		{"a task of another conversation", agentchat.ErrNotFound, http.StatusNotFound, agentchat.ErrNotFound.Error()},
		{"not a person", fmt.Errorf("%w: only a person chats with the agent", security.ErrForbidden), http.StatusForbidden, ""},
		{"still running", agentchat.ErrUnfinished, http.StatusConflict, agentchat.ErrUnfinished.Error()},
		{"a bad reason", fmt.Errorf("%w: pick a reason: offensive, harmful, inaccurate or other", agentchat.ErrBadFlag),
			http.StatusUnprocessableEntity, "pick a reason: offensive, harmful, inaccurate or other"},
		{"no such flag", agentchat.ErrFlagNotFound, http.StatusNotFound, agentchat.ErrFlagNotFound.Error()},
	} {
		chat.err = c.err
		out := serve(t, r, http.MethodPost, flagPath, `{"reason":"other"}`, asAda())
		if out.Code != c.status || !strings.Contains(out.Body.String(), c.says) {
			t.Errorf("%s: %d %s, want %d", c.name, out.Code, out.Body.String(), c.status)
		}
		if strings.Contains(out.Body.String(), "ses_1") || strings.Contains(out.Body.String(), "atask_1") {
			t.Errorf("%s: the refusal names the conversation: %s", c.name, out.Body.String())
		}
	}
}

const platformOwnerID = "usr_owner"

// testIsOwner answers ok for the owner whoever they came as, an agent
// acting for them included: the routes' gate must turn those away itself.
func testIsOwner(ctx context.Context) (string, bool) {
	p, ok := security.PrincipalFrom(ctx)
	if !ok {
		return "", false
	}
	if p.UserID == platformOwnerID || (p.OnBehalfOf != nil && p.OnBehalfOf.UserID == platformOwnerID) {
		return platformOwnerID, true
	}
	return "", false
}

func platformFlagRig(t *testing.T) (forge.Router, *fakeChat) {
	t.Helper()
	r := forge.NewRouter()
	chat := &fakeChat{}
	if err := mountPlatformFlagRoutes(r, nil, testIsOwner, chat); err != nil {
		t.Fatal(err)
	}
	return r, chat
}

func asOwner() *security.Principal {
	return &security.Principal{OrgID: "org_9", UserID: platformOwnerID}
}

var platformFlagRoutes = []struct{ method, path string }{
	{http.MethodGet, "/api/v1/platform/agent/flags"},
	{http.MethodGet, "/api/v1/platform/agent/flags?status=all"},
	{http.MethodPost, "/api/v1/platform/agent/flags/aflag_1/dismiss"},
}

// Only the platform owner, signed in themselves, reaches the flags. A
// workspace admin, an API key, the agent, a read-only session or anyone
// acting for the owner gets the 404 an unknown route gives, and the flags
// are never read.
func TestPlatformFlagsAreTheOwnersAlone(t *testing.T) {
	owner := *asOwner()
	for _, rt := range platformFlagRoutes {
		for _, c := range []struct {
			name string
			p    *security.Principal
		}{
			{"anonymous", nil},
			{"a workspace admin", &security.Principal{OrgID: "org_1", UserID: security.RoleAdmin}},
			{"a workspace owner", &security.Principal{OrgID: "org_1", UserID: security.RoleOwner}},
			{"the owner's API key", &security.Principal{OrgID: "org_9", UserID: platformOwnerID, KeyID: "key_1"}},
			{"the agent for the owner", &security.Principal{OrgID: "org_9", UserID: security.AgentSubject("org_9"), Kind: security.KindAgent, OnBehalfOf: &owner}},
			{"the owner read-only", &security.Principal{OrgID: "org_9", UserID: platformOwnerID, ReadOnly: true}},
			{"acting for the owner", &security.Principal{OrgID: "org_9", UserID: platformOwnerID, OnBehalfOf: &owner}},
		} {
			r, chat := platformFlagRig(t)
			out := serve(t, r, rt.method, rt.path, "", c.p)
			if out.Code != http.StatusNotFound || len(chat.calls) != 0 {
				t.Errorf("%s %s as %s: %d %s, calls %q", rt.method, rt.path, c.name, out.Code, out.Body.String(), chat.calls)
			}
			if strings.Contains(out.Body.String(), "flag") {
				t.Errorf("%s %s as %s: the 404 mentions flags: %s", rt.method, rt.path, c.name, out.Body.String())
			}
		}
		r, _ := platformFlagRig(t)
		if out := serve(t, r, rt.method, rt.path, "", asOwner()); out.Code != http.StatusOK {
			t.Errorf("%s %s as the owner: %d %s", rt.method, rt.path, out.Code, out.Body.String())
		}
	}
}

func TestPlatformFlagsList(t *testing.T) {
	r, chat := platformFlagRig(t)
	out := serve(t, r, http.MethodGet, "/api/v1/platform/agent/flags", "", asOwner())
	var list FlagList
	if err := json.Unmarshal(out.Body.Bytes(), &list); err != nil || out.Code != http.StatusOK {
		t.Fatalf("list: %d %s", out.Code, out.Body.String())
	}
	if chat.status != "" || len(list.Flags) != 2 || list.Flags[0].ID != "aflag_2" || list.Flags[0].Note != "rude" || list.Flags[0].Answer != "because" {
		t.Fatalf("status %q, flags %+v", chat.status, list.Flags)
	}
	if out = serve(t, r, http.MethodGet, "/api/v1/platform/agent/flags?status=dismissed", "", asOwner()); out.Code != http.StatusOK || chat.status != "dismissed" {
		t.Fatalf("dismissed: %d, asked for %q", out.Code, chat.status)
	}
	chat.err = fmt.Errorf("%w: status is open, dismissed or all", agentchat.ErrBadFlag)
	out = serve(t, r, http.MethodGet, "/api/v1/platform/agent/flags?status=closed", "", asOwner())
	if out.Code != http.StatusUnprocessableEntity || !strings.Contains(out.Body.String(), "status is open, dismissed or all") {
		t.Fatalf("bad status: %d %s", out.Code, out.Body.String())
	}
	chat.err = errors.New("read agent answer flags: connection refused on 10.0.0.4")
	if out = serve(t, r, http.MethodGet, "/api/v1/platform/agent/flags", "", asOwner()); out.Code != http.StatusInternalServerError ||
		strings.Contains(out.Body.String(), "10.0.0.4") {
		t.Fatalf("store failure: %d %s", out.Code, out.Body.String())
	}
}

func TestPlatformFlagsDismiss(t *testing.T) {
	r, chat := platformFlagRig(t)
	out := serve(t, r, http.MethodPost, "/api/v1/platform/agent/flags/aflag_7/dismiss", "", asOwner())
	var got agentchat.Flag
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || out.Code != http.StatusOK {
		t.Fatalf("dismiss: %d %s", out.Code, out.Body.String())
	}
	if chat.last() != "dismiss aflag_7" || chat.by != platformOwnerID || got.DismissedBy != platformOwnerID || got.DismissedAt == nil {
		t.Fatalf("called %q by %q, got %+v", chat.last(), chat.by, got)
	}
	chat.err = agentchat.ErrFlagNotFound
	if out = serve(t, r, http.MethodPost, "/api/v1/platform/agent/flags/aflag_0/dismiss", "", asOwner()); out.Code != http.StatusNotFound {
		t.Fatalf("no such flag: %d %s", out.Code, out.Body.String())
	}
}

// The flag routes are named for the client, and the platform routes sit
// behind the guard, which runs before the owner gate.
func TestFlagRoutesRegistered(t *testing.T) {
	guarded := 0
	guard := []forge.Middleware{func(forge.Handler) forge.Handler {
		return func(forge.Context) error {
			guarded++
			return forge.NewHTTPError(http.StatusTeapot, "the guard")
		}
	}}
	r := forge.NewRouter()
	chat := &fakeChat{}
	if err := mountChatRoutes(r, nil, &agentscope.Binder{Tenants: tenants{on: true}}, chat, &fakeTasks{}); err != nil {
		t.Fatal(err)
	}
	if err := mountPlatformFlagRoutes(r, guard, testIsOwner, chat); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"agentConversations.flag":    "POST /api/v1/agent/conversations/:id/turns/:task/flag",
		"platformAgentFlags.list":    "GET /api/v1/platform/agent/flags",
		"platformAgentFlags.dismiss": "POST /api/v1/platform/agent/flags/:id/dismiss",
	}
	got := map[string]string{}
	for _, info := range r.Routes() {
		if _, ours := want[info.Name]; ours {
			got[info.Name] = info.Method + " " + info.Path
		}
	}
	for name, route := range want {
		if got[name] != route {
			t.Errorf("%s is %q, want %q", name, got[name], route)
		}
	}
	if out := serve(t, r, http.MethodGet, "/api/v1/platform/agent/flags", "", asOwner()); out.Code != http.StatusTeapot || guarded != 1 {
		t.Fatalf("the platform routes skipped the guard: %d, guarded %d", out.Code, guarded)
	}
}

// The flag route is in the OpenAPI spec for the clients. The platform
// routes stay out of it, as the platform owner's billing routes do.
func TestPlatformFlagsStayOutOfOpenAPI(t *testing.T) {
	r := forge.NewRouter(forge.WithOpenAPI(forge.OpenAPIConfig{Title: "Loomplane", Version: "1", SpecEnabled: true}))
	chat := &fakeChat{}
	if err := mountChatRoutes(r, nil, &agentscope.Binder{Tenants: tenants{on: true}}, chat, &fakeTasks{}); err != nil {
		t.Fatal(err)
	}
	if err := mountPlatformFlagRoutes(r, nil, testIsOwner, chat); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(r.OpenAPISpec())
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	if !strings.Contains(spec, "/api/v1/agent/conversations/{id}/turns/{task}/flag") || !strings.Contains(spec, "agentConversations.flag") {
		t.Fatal("the flag route is missing from the OpenAPI spec")
	}
	if strings.Contains(spec, "/api/v1/platform/agent") || strings.Contains(spec, "platformAgentFlags") {
		t.Fatal("the platform flag routes are in the OpenAPI spec")
	}
}
