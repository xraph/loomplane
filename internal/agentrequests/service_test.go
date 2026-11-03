package agentrequests

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/cortex/suspension"

	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/teams"
)

func TestSuspensionNoticeTwiceCreatesOneRequest(t *testing.T) {
	r := newRig(t)
	n := external(ask(t, "call_ask", map[string]any{"question": "Which region?"}))
	r.suspend(t, r.chat, n)
	r.suspend(t, r.chat, n)
	if got := r.store.count(); got != 1 {
		t.Fatalf("%d requests", got)
	}
	if got := len(r.sent()); got != 1 {
		t.Fatalf("%d notices", got)
	}
	req := r.store.only(t)
	if req.Kind != KindAnswer || req.Status != StatusPending || req.Question != "Which region?" || req.AskedFor != ada ||
		req.TaskID != r.chat.ID || req.RunID != r.chat.RunID || req.ProjectID != projA || !strings.HasPrefix(req.ID, "areq_") {
		t.Fatalf("request %+v", req)
	}
	if !req.ExpiresAt.Equal(*n.ExpiresAt) {
		t.Fatalf("expires %v, want %v", req.ExpiresAt, n.ExpiresAt)
	}
	// The run's event log names the request, once.
	if len(r.runs.events) != 1 || r.runs.events[0] != r.chat.ID+":"+req.ID+":answer" {
		t.Fatalf("events %q", r.runs.events)
	}
}

func TestApprovalNoticeLinksThePlan(t *testing.T) {
	r := newRig(t)
	n := approval(t, "plan_1", "rev1")
	r.suspend(t, r.chat, n)
	req := r.store.only(t)
	if req.Kind != KindApprovePlan || req.Target.PlanID != "plan_1" || req.Target.PlanRevision != "rev1" ||
		req.CheckpointID != n.CheckpointID.String() || req.EnvironmentID != "env_prod" {
		t.Fatalf("request %+v", req)
	}
	if !req.ExpiresAt.Equal(*n.ExpiresAt) {
		t.Fatalf("expires %v, want the approval window's %v", req.ExpiresAt, n.ExpiresAt)
	}
	if !slices.Contains(req.Context, Link{Label: "The plan", Path: "/deployments/plan_1"}) {
		t.Fatalf("context %+v", req.Context)
	}
	// O4: the request's notice goes out beside the plan's own plan.waiting,
	// to the project's route: here the admins.
	sent := r.sent()
	if len(sent) != 1 {
		t.Fatalf("%d notices", len(sent))
	}
	got := sent[0]
	if got.Kind != notifications.KindAgentRequest || got.SubjectID != req.ID || got.Need != "agent:use" || got.ProjectID != projA ||
		got.Path != "/agent/requests?id="+req.ID || got.Title != "The agent needs you: approve a plan" ||
		!slices.Equal(got.Recipients, []string{cy}) {
		t.Fatalf("notice %+v", got)
	}
	if req.RouteSource != "admins" || req.TeamName != teams.AdminsName {
		t.Fatalf("route %+v", req)
	}
}

func TestApprovalOfAnythingElseIsRejected(t *testing.T) {
	r := newRig(t)
	n := approval(t, "plan_1", "rev1")
	n.Pending[0].Name = "create_project"
	r.suspend(t, r.chat, n)
	if r.store.count() != 0 || len(r.sent()) != 0 {
		t.Fatal("a request was made for a checkpoint that isn't a plan approval")
	}
	d := r.resumer.decided(t)
	if len(d) != 1 || d[0].d.Approved || d[0].checkpoint != n.CheckpointID.String() {
		t.Fatalf("decisions %+v", d)
	}
}

func TestQuestionIsOutboundScanned(t *testing.T) {
	r := newRig(t)
	r.suspend(t, r.chat, external(ask(t, "call_1", map[string]any{"question": "Use key sk-live-123?", "options": []string{"yes", "BLOCK this"}})))
	req := r.store.byCall(t, "call_1")
	if req.Question != "Use key [redacted]?" {
		t.Fatalf("question %q", req.Question)
	}
	if !slices.Equal(req.Options, []string{"yes"}) {
		t.Fatalf("options %q", req.Options)
	}
	r.suspend(t, r.chat, external(ask(t, "call_2", map[string]any{"question": "BLOCK me"})))
	blocked := r.store.byCall(t, "call_2")
	if blocked.Question != "The agent has a question for you, but its words didn't pass Loomplane's safety scan." {
		t.Fatalf("blocked question %q", blocked.Question)
	}
	for _, n := range r.sent() {
		if strings.Contains(n.Body, "BLOCK") || strings.Contains(n.Body, "sk-live-123") {
			t.Fatalf("notice body %q", n.Body)
		}
	}
}

func TestScopeIsBoundBeforeAnyScan(t *testing.T) {
	r := newRig(t)
	r.runs.bindErr = errors.New("the agent is off")
	err := r.svc.OnSuspended(context.Background(), r.chat, external(ask(t, "call_ask", map[string]any{"question": "Which region?"})))
	if err == nil {
		t.Fatal("a pause was handled without binding the task")
	}
	if r.store.count() != 0 || len(r.sent()) != 0 {
		t.Fatal("a request was made without a bound scope")
	}
	r.safety.mu.Lock()
	r.safety.unbound = nil // newRig's cleanup would fail on what this test provoked on purpose
	r.safety.mu.Unlock()
}

func TestAnswerResumesWithTheText(t *testing.T) {
	r := newRig(t)
	req := r.askAda(t, map[string]any{"question": "Which colour?"})
	got, err := r.svc.Answer(context.Background(), person(orgA, ada), req.ID, AnswerInput{Text: "blue"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusAnswered || got.Outcome != OutcomeAnswered || got.AnsweredBy != ada || got.Answer != "blue" || got.AnsweredAt == nil {
		t.Fatalf("request %+v", got)
	}
	res := r.resumer.provided(t)
	if len(res) != 1 || res[0].runID != r.chat.RunID || !res[0].bound {
		t.Fatalf("results %+v", res)
	}
	tr := res[0].in.ToolResults
	if len(tr) != 1 || tr[0].ToolCallID != "call_ask" || tr[0].Content != "Ada answered: blue" || tr[0].Error != "" {
		t.Fatalf("tool results %+v", tr)
	}
	// What reaches the model went through the untrusted scan.
	if !slices.Contains(r.safety.scanned, "person_answer: Ada answered: blue") {
		t.Fatalf("scanned %q", r.safety.scanned)
	}
	if !slices.Contains(r.audit.actions(), "agent.request.settle") {
		t.Fatalf("audit %q", r.audit.actions())
	}
}

func TestOnlyTheAskerAnswers(t *testing.T) {
	r := newRig(t)
	req := r.askAda(t, map[string]any{"question": "Which colour?"})
	if _, err := r.svc.Answer(context.Background(), person(orgA, bo), req.ID, AnswerInput{Text: "red"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("Bo: %v", err)
	}
	r.suspend(t, r.background, external(ask(t, "call_bg", map[string]any{"question": "Roll back?"})))
	bg := r.store.byCall(t, "call_bg")
	if bg.AskedFor != "" {
		t.Fatalf("asked for %q", bg.AskedFor)
	}
	if _, err := r.svc.Answer(context.Background(), person(orgA, ada), bg.ID, AnswerInput{Text: "yes"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a developer answered background work: %v", err)
	}
	if _, err := r.svc.Answer(context.Background(), person(orgA, cy), bg.ID, AnswerInput{Text: "yes"}); err != nil {
		t.Fatalf("an admin: %v", err)
	}
	if len(r.resumer.provided(t)) != 1 {
		t.Fatal("a refused answer resumed the run")
	}
}

func TestAnswersAreBounded(t *testing.T) {
	r := newRig(t)
	req := r.askAda(t, map[string]any{"question": "Which colour?"})
	for _, text := range []string{"", "   ", strings.Repeat("a", 4001)} {
		if _, err := r.svc.Answer(context.Background(), person(orgA, ada), req.ID, AnswerInput{Text: text}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%d characters: %v", len(text), err)
		}
	}
}

func secretCall(t *testing.T, in map[string]any) suspension.PendingCall {
	return suspension.PendingCall{ID: "call_secret", Name: "request_secret", Arguments: args(t, in)}
}

func TestProvideSecretSetsItAsThePerson(t *testing.T) {
	r := newRig(t)
	r.suspend(t, r.chat, external(secretCall(t, map[string]any{"project": "checkout-api", "key": "STRIPE_KEY", "environment": "production", "why": "payments need it"})))
	req := r.store.byCall(t, "call_secret")
	if req.Kind != KindProvideSecret || req.Target.Key != "STRIPE_KEY" || req.Target.Environment != "production" || req.ProjectID != projA {
		t.Fatalf("request %+v", req)
	}
	const value = "sk_live_THE_VALUE"
	got, err := r.svc.ProvideSecret(context.Background(), person(orgA, ada), req.ID, SecretInput{Value: value})
	if err != nil {
		t.Fatal(err)
	}
	if got.Answer != "" || got.Outcome != OutcomeProvided || got.Status != StatusAnswered {
		t.Fatalf("request %+v", got)
	}
	if len(r.vars.calls) != 1 {
		t.Fatalf("variable calls %+v", r.vars.calls)
	}
	c := r.vars.calls[0]
	if c.by != person(orgA, ada) || c.project != projA || c.create == nil || !c.create.Secret || c.create.Value != value ||
		c.create.Key != "STRIPE_KEY" || c.create.Environment != "production" {
		t.Fatalf("variable call %+v", c)
	}
	res := r.resumer.provided(t)
	if len(res) != 1 {
		t.Fatalf("results %+v", res)
	}
	content := res[0].in.ToolResults[0].Content
	if content != "Ada set STRIPE_KEY (secret) for production; the value is not shown to you" {
		t.Fatalf("content %q", content)
	}
	assertNoValue(t, r, value)

	// A key that exists is updated, not created again.
	r2 := newRig(t)
	r2.vars.existing = []*environments.Variable{{ID: "var_old", OrgID: orgA, ProjectID: projA, Key: "STRIPE_KEY", Environment: "production", Secret: true}}
	r2.suspend(t, r2.chat, external(secretCall(t, map[string]any{"project": "checkout-api", "key": "STRIPE_KEY", "environment": "production", "why": "x"})))
	req2 := r2.store.byCall(t, "call_secret")
	if _, err = r2.svc.ProvideSecret(context.Background(), person(orgA, ada), req2.ID, SecretInput{Value: value}); err != nil {
		t.Fatal(err)
	}
	if len(r2.vars.calls) != 1 || r2.vars.calls[0].id != "var_old" || r2.vars.calls[0].update == nil ||
		r2.vars.calls[0].update.Value != value || r2.vars.calls[0].update.Secret == nil || !*r2.vars.calls[0].update.Secret {
		t.Fatalf("update %+v", r2.vars.calls)
	}
	assertNoValue(t, r2, value)
}

// assertNoValue checks the secret reached nothing but the variables
// service.
func assertNoValue(t *testing.T, r *rig, value string) {
	t.Helper()
	for _, res := range r.resumer.provided(t) {
		for _, tr := range res.in.ToolResults {
			if strings.Contains(tr.Content+tr.Error, value) {
				t.Fatal("the model got the secret")
			}
		}
	}
	for _, e := range r.audit.events {
		for k, v := range e.Attributes {
			if s, ok := v.(string); ok && strings.Contains(s, value) {
				t.Fatalf("audit attribute %s has the secret", k)
			}
		}
	}
	for _, n := range r.sent() {
		if strings.Contains(n.Title+n.Body, value) {
			t.Fatal("a notice has the secret")
		}
	}
	for _, s := range append(slices.Clone(r.safety.scanned), r.safety.outbound...) {
		if strings.Contains(s, value) {
			t.Fatal("a scan saw the secret")
		}
	}
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	for _, row := range r.store.rows {
		if strings.Contains(row.Answer+row.Question, value) {
			t.Fatal("the request row has the secret")
		}
	}
}

func TestProvideSecretNeedsVariableManage(t *testing.T) {
	r := newRig(t)
	r.suspend(t, r.chat, external(secretCall(t, map[string]any{"project": "checkout-api", "key": "STRIPE_KEY", "why": "x"})))
	req := r.store.byCall(t, "call_secret")
	if _, err := r.svc.ProvideSecret(context.Background(), person(orgA, dee), req.ID, SecretInput{Value: "v"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a viewer: %v", err)
	}
	for _, v := range []string{"", strings.Repeat("x", 65537)} {
		if _, err := r.svc.ProvideSecret(context.Background(), person(orgA, ada), req.ID, SecretInput{Value: v}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%d bytes: %v", len(v), err)
		}
	}
	if len(r.vars.calls) != 0 || len(r.resumer.provided(t)) != 0 {
		t.Fatal("a refused secret was set")
	}
	// A failed write leaves the request pending, so someone can try again.
	r.vars.err = environments.ErrInvalid
	if _, err := r.svc.ProvideSecret(context.Background(), person(orgA, ada), req.ID, SecretInput{Value: "v"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a failed write: %v", err)
	}
	if got := r.store.byCall(t, "call_secret"); got.Status != StatusPending {
		t.Fatalf("after a failed write: %s", got.Status)
	}
}

func TestASecretIsntRequestedOutsideTheRunsProject(t *testing.T) {
	r := newRig(t)
	r.suspend(t, r.chat, external(secretCall(t, map[string]any{"project": "billing", "key": "K", "why": "x"})))
	req := r.store.byCall(t, "call_secret")
	if req.Status != StatusCancelled || len(r.sent()) != 0 {
		t.Fatalf("request %+v, %d notices", req, len(r.sent()))
	}
	res := r.resumer.provided(t)
	if len(res) != 1 || res[0].in.ToolResults[0].Error == "" {
		t.Fatalf("results %+v", res)
	}
}

func TestCompleteChecksGitHubForReal(t *testing.T) {
	r := newRig(t)
	r.suspend(t, r.chat, external(suspension.PendingCall{ID: "call_gh", Name: "request_github_connection",
		Arguments: args(t, map[string]string{"repository": "acme/api", "why": "the code is private"})}))
	req := r.store.byCall(t, "call_gh")
	if req.Kind != KindConnectGitHub || req.Target.Repository != "acme/api" {
		t.Fatalf("request %+v", req)
	}
	r.github.add(orgA, "old", r.now.Add(-time.Hour))
	if _, err := r.svc.Complete(context.Background(), person(orgA, ada), req.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a developer: %v", err)
	}
	if _, err := r.svc.Complete(context.Background(), person(orgA, cy), req.ID); !errors.Is(err, ErrNotDone) {
		t.Fatalf("nothing new installed: %v", err)
	}
	r.advance(time.Minute)
	r.github.add(orgA, "acme", r.clock())
	got, err := r.svc.Complete(context.Background(), person(orgA, cy), req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusAnswered || got.Outcome != OutcomeConnected || got.AnsweredBy != cy {
		t.Fatalf("request %+v", got)
	}
	res := r.resumer.provided(t)
	if len(res) != 1 || res[0].in.ToolResults[0].Content != "GitHub is connected: acme" {
		t.Fatalf("results %+v", res)
	}
}

func TestCompleteChecksTheCloudForReal(t *testing.T) {
	r := newRig(t)
	r.suspend(t, r.chat, external(suspension.PendingCall{ID: "call_cloud", Name: "request_cloud_connection",
		Arguments: args(t, map[string]string{"cloud": "aws", "why": "to deploy"})}))
	req := r.store.byCall(t, "call_cloud")
	if _, err := r.svc.Complete(context.Background(), person(orgA, cy), req.ID); !errors.Is(err, ErrNotDone) {
		t.Fatalf("nothing connected: %v", err)
	}
	r.clouds.conns = append(r.clouds.conns, &connections.Connection{ID: "conn_1", OrgID: orgA, Cloud: "aws", Name: "prod-aws", CreatedBy: cy, CreatedAt: r.now.Add(time.Second)})
	if _, err := r.svc.Complete(context.Background(), person(orgA, cy), req.ID); err != nil {
		t.Fatal(err)
	}
	if c := r.resumer.provided(t)[0].in.ToolResults[0].Content; c != "Cy connected aws as prod-aws" {
		t.Fatalf("content %q", c)
	}
}

func TestDeclineRejectsALinkedPlan(t *testing.T) {
	r := newRig(t)
	n := approval(t, "plan_1", "rev1")
	r.suspend(t, r.chat, n)
	req := r.store.only(t)
	if _, err := r.svc.Decline(context.Background(), person(orgA, ada), req.ID, DeclineInput{Reason: "not today"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a developer: %v", err)
	}
	got, err := r.svc.Decline(context.Background(), person(orgA, cy), req.ID, DeclineInput{Reason: "not today"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.plans.rejected) != 1 || r.plans.rejected[0] != (rejection{by: cy, plan: "plan_1", note: "not today"}) {
		t.Fatalf("rejections %+v", r.plans.rejected)
	}
	if got.Status != StatusDeclined || got.Outcome != OutcomeRejected || got.AnsweredBy != cy {
		t.Fatalf("request %+v", got)
	}
	d := r.resumer.decided(t)
	if len(d) != 1 || d[0].d.Approved || d[0].d.DecidedBy != cy {
		t.Fatalf("decisions %+v", d)
	}

	// An external request's decline goes back to the model as an error.
	req2 := r.askAda(t, map[string]any{"question": "Which colour?"})
	if _, err = r.svc.Decline(context.Background(), person(orgA, ada), req2.ID, DeclineInput{Reason: "ask Bo"}); err != nil {
		t.Fatal(err)
	}
	res := r.resumer.provided(t)
	if len(res) != 1 || res[0].in.ToolResults[0].Error != "Ada declined: ask Bo" {
		t.Fatalf("results %+v", res)
	}
}

func TestPlanApprovalsAreAnsweredThroughThePlan(t *testing.T) {
	r := newRig(t)
	r.suspend(t, r.chat, approval(t, "plan_1", "rev1"))
	req := r.store.only(t)
	if _, err := r.svc.Answer(context.Background(), person(orgA, cy), req.ID, AnswerInput{Text: "approve"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("answer: %v", err)
	}
	if _, err := r.svc.Complete(context.Background(), person(orgA, cy), req.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("complete: %v", err)
	}
	if len(r.resumer.decided(t)) != 0 {
		t.Fatal("a checkpoint was decided outside the plan")
	}
}

func TestSettledRequestsAreFinal(t *testing.T) {
	r := newRig(t)
	req := r.askAda(t, map[string]any{"question": "Which colour?"})
	if _, err := r.svc.Answer(context.Background(), person(orgA, ada), req.ID, AnswerInput{Text: "blue"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Answer(context.Background(), person(orgA, ada), req.ID, AnswerInput{Text: "red"}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second answer: %v", err)
	}
	if _, err := r.svc.Decline(context.Background(), person(orgA, ada), req.ID, DeclineInput{}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("decline after answer: %v", err)
	}
	if len(r.resumer.provided(t)) != 1 {
		t.Fatal("the run was resumed twice")
	}
}

func TestAFailedResumeLeavesTheRequestOpen(t *testing.T) {
	r := newRig(t)
	req := r.askAda(t, map[string]any{"question": "Which colour?"})
	r.resumer.err = errors.New("cortex is down")
	if _, err := r.svc.Answer(context.Background(), person(orgA, ada), req.ID, AnswerInput{Text: "blue"}); err == nil {
		t.Fatal("no error")
	}
	if got := r.store.byCall(t, "call_ask"); got.Status != StatusPending || got.AnsweredBy != "" {
		t.Fatalf("after a failed resume: %+v", got)
	}
	r.resumer.err = nil
	if _, err := r.svc.Answer(context.Background(), person(orgA, ada), req.ID, AnswerInput{Text: "blue"}); err != nil {
		t.Fatal(err)
	}
}

func TestAPauseResumesOnceEveryCallIsSettled(t *testing.T) {
	r := newRig(t)
	r.suspend(t, r.chat, external(
		ask(t, "call_1", map[string]any{"question": "Which region?"}),
		ask(t, "call_2", map[string]any{"question": "Which size?"}),
	))
	one, two := r.store.byCall(t, "call_1"), r.store.byCall(t, "call_2")
	if _, err := r.svc.Answer(context.Background(), person(orgA, ada), one.ID, AnswerInput{Text: "eu"}); err != nil {
		t.Fatal(err)
	}
	if len(r.resumer.provided(t)) != 0 {
		t.Fatal("the run resumed with a call still open")
	}
	if _, err := r.svc.Decline(context.Background(), person(orgA, ada), two.ID, DeclineInput{}); err != nil {
		t.Fatal(err)
	}
	res := r.resumer.provided(t)
	if len(res) != 1 || len(res[0].in.ToolResults) != 2 {
		t.Fatalf("results %+v", res)
	}
	byCall := map[string]string{}
	for _, tr := range res[0].in.ToolResults {
		byCall[tr.ToolCallID] = tr.Content + "|" + tr.Error
	}
	if byCall["call_1"] != "Ada answered: eu|" || byCall["call_2"] != "|Ada declined" {
		t.Fatalf("results %+v", byCall)
	}
}

func TestFinishedRunsCancelPendingRequests(t *testing.T) {
	r := newRig(t)
	r.askAda(t, map[string]any{"question": "Which colour?"})
	r.suspend(t, r.background, external(ask(t, "call_bg", map[string]any{"question": "Roll back?"})))
	if err := r.svc.OnFinished(context.Background(), r.chat); err != nil {
		t.Fatal(err)
	}
	if got := r.store.byCall(t, "call_ask"); got.Status != StatusCancelled || got.Outcome != OutcomeCancelled {
		t.Fatalf("chat request %+v", got)
	}
	if got := r.store.byCall(t, "call_bg"); got.Status != StatusPending {
		t.Fatalf("another task's request %+v", got)
	}
	if len(r.resumer.provided(t)) != 0 || len(r.resumer.decided(t)) != 0 {
		t.Fatal("a finished run was resumed")
	}
}

// ---- teams ----

func TestRequestGoesToTheProjectTeam(t *testing.T) {
	r := newRig(t)
	r.teams.responsible[projA] = "team_pay"
	r.suspend(t, r.background, external(ask(t, "call_bg", map[string]any{"question": "Roll back?"})))
	req := r.store.byCall(t, "call_bg")
	if req.TeamID != "team_pay" || req.TeamName != "Payments" || req.RouteSource != "project" {
		t.Fatalf("route %+v", req)
	}
	sent := r.sent()
	if len(sent) != 1 || !slices.Equal(sent[0].Recipients, []string{dee, eve}) {
		t.Fatalf("notices %+v", sent)
	}
	if !strings.HasSuffix(sent[0].Body, "Sent to Payments, the team responsible for Checkout API.") {
		t.Fatalf("body %q", sent[0].Body)
	}
	// Plan approvals go to the project's team too.
	r.suspend(t, r.chat, approval(t, "plan_1", "rev1"))
	if got := r.store.byCall(t, "call_approve"); got.TeamID != "team_pay" {
		t.Fatalf("approval route %+v", got)
	}
}

func TestNamedTeamNeedsAReasonAndShowsIt(t *testing.T) {
	if err := agentops.ValidateRequest("ask_person", []byte(`{"question":"q","team":"payments"}`)); !errors.Is(err, agentops.ErrTeamWithoutReason) {
		t.Fatalf("a team without a reason: %v", err)
	}
	r := newRig(t)
	r.teams.responsible[projA] = "team_ops"
	r.suspend(t, r.background, external(ask(t, "call_bg", map[string]any{"question": "Refund?", "team": "Payments", "team_reason": "they own refunds"})))
	req := r.store.byCall(t, "call_bg")
	if req.TeamID != "team_pay" || req.RouteSource != "named" || req.TeamReason != "they own refunds" {
		t.Fatalf("route %+v", req)
	}
	sent := r.sent()
	if len(sent) != 1 || !slices.Equal(sent[0].Recipients, []string{dee, eve}) || !strings.HasSuffix(sent[0].Body, "Sent to Payments: they own refunds") {
		t.Fatalf("notices %+v", sent)
	}
	// The reason is the agent's words, so it's outbound-scanned.
	if !slices.Contains(r.safety.outbound, "they own refunds") {
		t.Fatalf("outbound %q", r.safety.outbound)
	}
	// In a chat, the asker hears too.
	r.suspend(t, r.chat, external(ask(t, "call_chat", map[string]any{"question": "Refund?", "team": "Payments", "team_reason": "they own refunds"})))
	sent = r.sent()
	if !slices.Equal(sent[1].Recipients, []string{ada, dee, eve}) {
		t.Fatalf("chat recipients %q", sent[1].Recipients)
	}
}

func TestEmptyTeamFallsBackToAdmins(t *testing.T) {
	r := newRig(t)
	r.teams.responsible[projA] = "team_empty"
	r.suspend(t, r.background, external(ask(t, "call_bg", map[string]any{"question": "Roll back?", "team": "Nobody", "team_reason": "why not"})))
	req := r.store.byCall(t, "call_bg")
	if req.RouteSource != "admins" || req.TeamID != "" || !strings.Contains(req.RouteNote, "Empty has no members") || !strings.Contains(req.RouteNote, "No team named") {
		t.Fatalf("route %+v", req)
	}
	if sent := r.sent(); len(sent) != 1 || !slices.Equal(sent[0].Recipients, []string{cy}) {
		t.Fatalf("notices %+v", sent)
	}
	// In a chat with no team that has members, it goes to the asker alone.
	r.suspend(t, r.chat, external(ask(t, "call_chat", map[string]any{"question": "Which colour?"})))
	chat := r.store.byCall(t, "call_chat")
	if chat.RouteSource != RouteAsker || chat.TeamID != "" {
		t.Fatalf("chat route %+v", chat)
	}
	if sent := r.sent(); !slices.Equal(sent[1].Recipients, []string{ada}) {
		t.Fatalf("chat recipients %q", sent[1].Recipients)
	}
}

func TestAnyTeamMemberAnswersOnce(t *testing.T) {
	r := newRig(t)
	r.teams.responsible[projA] = "team_pay"
	r.suspend(t, r.background, external(ask(t, "call_bg", map[string]any{"question": "Roll back?"})))
	req := r.store.byCall(t, "call_bg")
	if _, err := r.svc.Answer(context.Background(), person(orgA, bo), req.ID, AnswerInput{Text: "no"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("someone outside the team: %v", err)
	}
	// Dee is a viewer, and a member of the team it was sent to.
	got, err := r.svc.Answer(context.Background(), person(orgA, dee), req.ID, AnswerInput{Text: "yes"})
	if err != nil || got.AnsweredBy != dee {
		t.Fatalf("Dee: %+v %v", got, err)
	}
	if _, err = r.svc.Answer(context.Background(), person(orgA, eve), req.ID, AnswerInput{Text: "no"}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("Eve after Dee: %v", err)
	}
	if res := r.resumer.provided(t); len(res) != 1 || res[0].in.ToolResults[0].Content != "Dee answered: yes" {
		t.Fatalf("results %+v", res)
	}
}

func TestListForMe(t *testing.T) {
	r := newRig(t)
	r.teams.responsible[projA] = "team_pay"
	r.askAda(t, map[string]any{"question": "Which colour?"})                                          // Ada's own
	r.suspend(t, r.background, external(ask(t, "call_bg", map[string]any{"question": "Roll back?"}))) // Payments
	other := r.task("atask_c", orgA, "proj_c", "")
	r.suspend(t, other, external(ask(t, "call_c", map[string]any{"question": "Scale?"}))) // the admins

	count := func(p security.Principal, f ListFilter) int {
		t.Helper()
		list, err := r.svc.List(context.Background(), p, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(list.Requests)
	}
	if n := count(person(orgA, bo), ListFilter{}); n != 3 {
		t.Fatalf("everyone: %d", n)
	}
	for user, want := range map[string]int{ada: 1, dee: 1, bo: 0, cy: 1} {
		if n := count(person(orgA, user), ListFilter{For: "me"}); n != want {
			t.Fatalf("%s for me: %d, want %d", user, n, want)
		}
	}
	if _, err := r.svc.List(context.Background(), person(orgA, bo), ListFilter{For: "you"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("for=you: %v", err)
	}
	if _, err := r.svc.List(context.Background(), person(orgA, bo), ListFilter{Status: "done"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("status=done: %v", err)
	}
}

// ---- two workspaces ----

func TestAnotherWorkspaceCantSeeOrAnswer(t *testing.T) {
	r := newRig(t)
	req := r.askAda(t, map[string]any{"question": "Which colour?"})
	r.suspend(t, r.chat, external(secretCall(t, map[string]any{"project": "checkout-api", "key": "K", "why": "x"})))
	secret := r.store.byCall(t, "call_secret")
	bob := person(orgB, bea)
	list, err := r.svc.List(context.Background(), bob, ListFilter{Status: "all"})
	if err != nil || len(list.Requests) != 0 {
		t.Fatalf("org B's list: %+v %v", list, err)
	}
	if _, err = r.svc.Get(context.Background(), bob, req.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get: %v", err)
	}
	if _, err = r.svc.Answer(context.Background(), bob, req.ID, AnswerInput{Text: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("answer: %v", err)
	}
	if _, err = r.svc.ProvideSecret(context.Background(), bob, secret.ID, SecretInput{Value: "v"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("secret: %v", err)
	}
	if _, err = r.svc.Decline(context.Background(), bob, req.ID, DeclineInput{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("decline: %v", err)
	}
	if _, err = r.svc.Complete(context.Background(), bob, req.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("complete: %v", err)
	}
	// A principal claiming org A but a member of org B only is no member.
	if _, err = r.svc.Get(context.Background(), person(orgA, bea), req.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a non-member: %v", err)
	}
	if len(r.resumer.provided(t)) != 0 || len(r.vars.calls) != 0 {
		t.Fatal("org B reached org A's run")
	}
}

// A task from org B handed to org A's handler makes nothing in org A.
func TestATaskIsReadThroughItsOwnWorkspace(t *testing.T) {
	r := newRig(t)
	foreign := r.task("atask_b", orgB, projB, bea)
	r.suspend(t, foreign, external(secretCall(t, map[string]any{"project": "checkout-api", "key": "K", "why": "x"})))
	req := r.store.byCall(t, "call_secret")
	if req.OrgID != orgB || req.ProjectID == projA || req.Status != StatusCancelled {
		t.Fatalf("org B's request named org A's project: %+v", req)
	}
}

// ---- the decider principal ----

func TestDecisionsCarryThePersonForCortex(t *testing.T) {
	r := newRig(t)
	r.suspend(t, r.chat, approval(t, "plan_1", "rev1"))
	req := r.store.only(t)
	if _, err := r.svc.Decline(context.Background(), person(orgA, cy), req.ID, DeclineInput{Reason: "no"}); err != nil {
		t.Fatal(err)
	}
	d := r.resumer.decided(t)
	if len(d) != 1 || !d[0].bound {
		t.Fatalf("decisions %+v", d)
	}
	dec, ok := d[0].principal.(agentauthz.Decider)
	if !ok || dec.Person != person(orgA, cy) {
		t.Fatalf("the decision's principal %#v", d[0].principal)
	}
}

var _ = planning.StatusApproved
var _ = agentsafety.Withheld

// A plan approval for a plan the run can't reach, another workspace's or
// another project's, rejects the checkpoint and asks nobody.
func TestApprovalOfAPlanOutsideTheRunIsRejected(t *testing.T) {
	r := newRig(t)
	r.plans.plans["plan_b"] = &planning.Plan{ID: "plan_b", OrgID: orgB, ProjectID: projB, Status: planning.StatusReady, Revision: "r"}
	r.plans.plans["plan_c"] = &planning.Plan{ID: "plan_c", OrgID: orgA, ProjectID: "proj_c", Status: planning.StatusReady, Revision: "r"}
	for _, planID := range []string{"plan_b", "plan_c", "plan_missing"} {
		n := approval(t, planID, "r")
		r.suspend(t, r.chat, n)
		d := r.resumer.decided(t)
		if r.store.count() != 0 || len(d) == 0 || d[len(d)-1].checkpoint != n.CheckpointID.String() || d[len(d)-1].d.Approved {
			t.Fatalf("%s: %d requests, decisions %+v", planID, r.store.count(), d)
		}
	}
}

// ---- fix round 1 ----

// I2: the team the model names is at most 80 characters, and what it
// typed reaches people only through the outbound scan.
func TestTheNamedTeamIsBoundedAndScanned(t *testing.T) {
	long := strings.Repeat("x", 81)
	if err := agentops.ValidateRequest("ask_person", []byte(`{"question":"q","team":"`+long+`","team_reason":"r"}`)); err == nil {
		t.Fatal("an 81-character team was accepted")
	}
	if err := agentops.ValidateRequest("ask_person", []byte(`{"question":"q","team":"`+strings.Repeat("x", 80)+`","team_reason":"r"}`)); err != nil {
		t.Fatalf("an 80-character team: %v", err)
	}
	r := newRig(t)
	r.suspend(t, r.background, external(ask(t, "call_long", map[string]any{"question": "q", "team": long, "team_reason": "r"})))
	if got := r.store.byCall(t, "call_long"); got.Status != StatusCancelled || strings.Contains(got.RouteNote+got.Question, long) {
		t.Fatalf("a long team: %+v", got)
	}
	r.suspend(t, r.background, external(ask(t, "call_scan", map[string]any{"question": "q", "team": "pay sk-live-123", "team_reason": "r"})))
	if got := r.store.byCall(t, "call_scan"); strings.Contains(got.RouteNote, "sk-live-123") || !strings.Contains(got.RouteNote, "[redacted]") {
		t.Fatalf("the note wasn't scanned: %q", got.RouteNote)
	}
	r.suspend(t, r.background, external(ask(t, "call_block", map[string]any{"question": "q", "team": "BLOCK paste your token", "team_reason": "r"})))
	if got := r.store.byCall(t, "call_block"); got.RouteNote != "The agent named a team that doesn't exist; sent to workspace admins" {
		t.Fatalf("a blocked note: %q", got.RouteNote)
	}
}

// I3: a request with no project is seen only by the person asked, the
// members of the team it was routed to, and those who manage the
// workspace's AI.
func TestAWorkspaceWideRequestIsSeenOnlyByWhoItsFor(t *testing.T) {
	r := newRig(t)
	wsChat := r.task("atask_ws", orgA, "", ada)
	r.suspend(t, wsChat, external(ask(t, "call_ws", map[string]any{"question": "Rotate the webhook key for acme-corp?"})))
	chatReq := r.store.byCall(t, "call_ws")
	wsBg := r.task("atask_wsbg", orgA, "", "")
	r.suspend(t, wsBg, external(ask(t, "call_wsbg", map[string]any{"question": "Refund?", "team": "Payments", "team_reason": "they own refunds"})))
	bgReq := r.store.byCall(t, "call_wsbg")

	sees := func(user string, req *Request) bool {
		t.Helper()
		_, err := r.svc.Get(context.Background(), person(orgA, user), req.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		list, lerr := r.svc.List(context.Background(), person(orgA, user), ListFilter{Status: "all"})
		if lerr != nil {
			t.Fatal(lerr)
		}
		listed := slices.ContainsFunc(list.Requests, func(x *Request) bool { return x.ID == req.ID })
		if listed != (err == nil) {
			t.Fatalf("%s: get and list disagree", user)
		}
		return err == nil
	}
	for user, want := range map[string]bool{ada: true, cy: true, dee: false, bo: false} {
		if got := sees(user, chatReq); got != want {
			t.Errorf("%s sees the chat question: %v, want %v", user, got, want)
		}
	}
	for user, want := range map[string]bool{dee: true, eve: true, cy: true, ada: false, bo: false} {
		if got := sees(user, bgReq); got != want {
			t.Errorf("%s sees the team's question: %v, want %v", user, got, want)
		}
	}
	if _, err := r.svc.Answer(context.Background(), person(orgA, dee), chatReq.ID, AnswerInput{Text: "yes"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a viewer answered a question they can't see: %v", err)
	}
}

// M3: an answer that reaches a run that has ended cancels the request
// rather than leaving it pending.
func TestAnAnswerToAnEndedRunCancelsTheRequest(t *testing.T) {
	r := newRig(t)
	req := r.askAda(t, map[string]any{"question": "Which colour?"})
	r.runs.mu.Lock()
	r.runs.tasks[r.chat.RunID].Status = "cancelled"
	r.runs.mu.Unlock()
	if _, err := r.svc.Answer(context.Background(), person(orgA, ada), req.ID, AnswerInput{Text: "blue"}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("answer to an ended run: %v", err)
	}
	if got := r.store.byCall(t, "call_ask"); got.Status != StatusCancelled {
		t.Fatalf("request %+v", got)
	}
	if len(r.resumer.provided(t)) != 0 {
		t.Fatal("an ended run was resumed")
	}
}

// M4: a pause whose calls were all refused resumes on a repeated notice
// when the first resume failed.
func TestARefusedPauseResumesOnARepeatedNotice(t *testing.T) {
	r := newRig(t)
	n := external(secretCall(t, map[string]any{"project": "billing", "key": "K", "why": "x"}))
	r.resumer.err = errors.New("cortex is down")
	if err := r.svc.OnSuspended(context.Background(), r.chat, n); err == nil {
		t.Fatal("no error")
	}
	r.resumer.err = nil
	r.suspend(t, r.chat, n)
	if res := r.resumer.provided(t); len(res) != 1 || res[0].in.ToolResults[0].Error == "" {
		t.Fatalf("results %+v", res)
	}
}

// M5: a secret written before the resume failed is kept, and the request
// opens again so a person can set it once more; the next value replaces
// it.
func TestASecretWrittenBeforeAFailedResumeLeavesTheRequestOpen(t *testing.T) {
	r := newRig(t)
	r.suspend(t, r.chat, external(secretCall(t, map[string]any{"project": "checkout-api", "key": "K", "why": "x"})))
	req := r.store.byCall(t, "call_secret")
	r.resumer.err = errors.New("cortex is down")
	if _, err := r.svc.ProvideSecret(context.Background(), person(orgA, ada), req.ID, SecretInput{Value: "v1"}); err == nil {
		t.Fatal("no error")
	}
	if got := r.store.byCall(t, "call_secret"); got.Status != StatusPending || len(r.vars.calls) != 1 {
		t.Fatalf("request %+v, %d writes", got, len(r.vars.calls))
	}
}

// M9: a run scoped to an environment asks to approve only its own
// environment's plans.
func TestAnEnvironmentRunApprovesOnlyItsOwnEnvironment(t *testing.T) {
	r := newRig(t)
	staging := r.task("atask_stg", orgA, projA, ada)
	staging.EnvironmentID = "env_stg"
	n := approval(t, "plan_1", "rev1") // production's
	r.suspend(t, staging, n)
	if d := r.resumer.decided(t); r.store.count() != 0 || len(d) != 1 || d[0].d.Approved {
		t.Fatalf("%d requests, decisions %+v", r.store.count(), d)
	}
}

// M10: two people answering at once: exactly one answer counts.
func TestTwoAnswersAtOnceSettleOnce(t *testing.T) {
	for range 20 {
		r := newRig(t)
		r.teams.responsible[projA] = "team_pay"
		r.suspend(t, r.background, external(ask(t, "call_bg", map[string]any{"question": "Roll back?"})))
		req := r.store.byCall(t, "call_bg")
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, user := range []string{dee, eve} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = r.svc.Answer(context.Background(), person(orgA, user), req.ID, AnswerInput{Text: user})
			}()
		}
		wg.Wait()
		ok, refused := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrNotPending):
				refused++
			}
		}
		if ok != 1 || refused != 1 || len(r.resumer.provided(t)) != 1 {
			t.Fatalf("%d answers counted, %d refused, %d resumes", ok, refused, len(r.resumer.provided(t)))
		}
	}
}
