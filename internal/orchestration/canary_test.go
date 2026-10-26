package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/dispatch"
	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/engine"
	"github.com/xraph/dispatch/store/memory"

	"github.com/xraph/loomplane/internal/releasehealth"
)

// commandIDs lists the kind and ID of every command a run scheduled, in
// order.
func commandIDs(t *testing.T, s durable.Store, key durable.Key) []string {
	t.Helper()
	var ids []string
	for after := int64(0); ; {
		history, err := s.ReadHistory(t.Context(), key, after, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if len(history) == 0 {
			return ids
		}
		for _, event := range history {
			after = event.Sequence
			if event.Type != drt.EventCommandScheduled {
				continue
			}
			var c drt.Command
			if err = json.Unmarshal(event.Payload, &c); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, string(c.Kind)+" "+c.ID)
		}
	}
}

// v3DeploymentCommands and v3RetryCommands are what a v3 run scheduled
// before the steps moved into capacityEffects and finishCapacity. A run
// started before the change replays only while these stay the same.
var v3DeploymentCommands = []string{
	"activity plan", "timer deadline", "signal decision-1", "select await-1", "activity decide-1", "activity deploy-start",
	"activity dep-effect-source-0-capacity-0", "activity dep-effect-source-0",
	"activity dep-effect-first-0-capacity-0", "activity dep-effect-first-0",
	"activity dep-effect-second-0-capacity-0", "signal dep-effect-second-0-capacity-0-wake", "timer dep-effect-second-0-capacity-0-timer", "select dep-effect-second-0-capacity-0-await",
	"activity dep-effect-second-0-capacity-1", "activity dep-effect-second-0",
	"activity dep-finish-0-capacity-0", "activity dep-finish-0",
}

var v3RetryCommands = v3DeploymentCommands[6:]

func TestCapacityWorkflowCommandIDsUnchanged(t *testing.T) {
	t.Run("deployment", func(t *testing.T) {
		s := memory.New()
		d := NewDeployments()
		p := &capacityProbe{calls: map[string]int{}}
		d.deployer, d.planner, d.engineOf = p, capacityPlanner{}, func() *engine.Engine { return nil }
		w, err := drt.NewWorker(s, d.CapacityOptions("golden", 1))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(DeploymentInput{OrgID: "org", PlanID: "plan", Window: time.Hour, ReconcileInterval: capacityInterval})
		req := durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: "plan", RunID: runID}, RequestID: "start:plan", WorkflowType: WorkflowDeployment, BuildID: CapacityBuild, Queue: DurableQueue, Input: raw}
		if _, err = w.StartExecution(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		pumpCapacity(t, w)
		dec, _ := json.Marshal(Decision{Approve: true, Revision: "r1", By: "usr"})
		if _, err = w.SignalExecution(t.Context(), durable.SignalRequest{Key: req.Key, RequestID: "approve", BuildID: CapacityBuild, Name: SignalDecision, Input: dec}); err != nil {
			t.Fatal(err)
		}
		pumpCapacity(t, w)
		p.paused = false
		if w, err = drt.NewWorker(s, d.CapacityOptions("golden-restarted", 1)); err != nil {
			t.Fatal(err)
		}
		if _, err = w.SignalExecution(t.Context(), durable.SignalRequest{Key: req.Key, RequestID: "capacity:1", BuildID: CapacityBuild, Name: SignalCapacity, Input: []byte("{}")}); err != nil {
			t.Fatal(err)
		}
		pumpCapacity(t, w)
		if e, _ := s.GetExecution(t.Context(), req.Key); e.State != durable.StateCompleted {
			t.Fatalf("run %s", e.State)
		}
		if got := commandIDs(t, s, req.Key); !slices.Equal(got, v3DeploymentCommands) {
			t.Fatalf("v3 deployment commands changed:\n%#v", got)
		}
	})
	t.Run("retry", func(t *testing.T) {
		s := memory.New()
		d := NewDeployments()
		p := &capacityProbe{calls: map[string]int{}}
		d.deployer = p
		w, err := drt.NewWorker(s, d.CapacityOptions("golden-retry", 1))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(retryInput{OrgID: "org", DeploymentID: "dep", Steps: [][]string{{"first", "second"}}, ReconcileInterval: capacityInterval})
		req := durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: "retry", RunID: runID}, RequestID: "start:retry", WorkflowType: WorkflowRetry, BuildID: CapacityBuild, Queue: DurableQueue, Input: raw}
		if _, err = w.StartExecution(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		pumpCapacity(t, w)
		p.paused = false
		if w, err = drt.NewWorker(s, d.CapacityOptions("golden-restarted", 1)); err != nil {
			t.Fatal(err)
		}
		if _, err = w.SignalExecution(t.Context(), durable.SignalRequest{Key: req.Key, RequestID: "capacity:1", BuildID: CapacityBuild, Name: SignalCapacity, Input: []byte("{}")}); err != nil {
			t.Fatal(err)
		}
		pumpCapacity(t, w)
		if e, _ := s.GetExecution(t.Context(), req.Key); e.State != durable.StateCompleted {
			t.Fatalf("run %s", e.State)
		}
		if got := commandIDs(t, s, req.Key); !slices.Equal(got, v3RetryCommands) {
			t.Fatalf("v3 retry commands changed:\n%#v", got)
		}
	})
}

// canaryProbe is a deployer with a canary. Its log lists every effect in
// the order the workflow asked for it.
type canaryProbe struct {
	steps      [][]string
	ramp       CanaryRamp
	verdicts   map[[2]int]CanaryJudgement // by step and attempt; pass when absent
	failAction string                     // this action fails
	stopAfter  string                     // capacity cancels the deployment once this action ran
	pauseAfter string                     // capacity pauses the deployment once this action ran
	revertErr  error
	promoteErr error
	started    []context.Context
	windows    []releasehealth.Window
	log        []string
}

var weighedAt = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func newCanaryProbe(t *testing.T) *canaryProbe {
	t.Helper()
	wait := canarySettleWait
	canarySettleWait = time.Millisecond
	t.Cleanup(func() { canarySettleWait = wait })
	return &canaryProbe{
		steps: [][]string{{"first", "second"}},
		ramp:  CanaryRamp{Steps: []int{5, 25, 50, 100}, Hold: time.Millisecond, MaxInconclusive: releasehealth.MaxInconclusive},
	}
}

func (p *canaryProbe) Start(ctx context.Context, _, _ string) (string, [][]string, error) {
	p.started = append(p.started, ctx)
	return "dep", p.steps, nil
}
func (p *canaryProbe) FetchSource(context.Context, string, string) error {
	p.log = append(p.log, "source")
	return nil
}
func (p *canaryProbe) Run(_ context.Context, _, _, action string, _ []byte, _ func([]byte) error) error {
	p.log = append(p.log, "run "+action)
	if action == p.failAction {
		// Not retried, so the test needn't wait out the backoff.
		return &drt.ApplicationError{Type: "test", Message: action + ": quota exceeded", NonRetryable: true}
	}
	return nil
}
func (p *canaryProbe) Finish(_ context.Context, _, _, reason string) error {
	p.log = append(p.log, "finish "+reason)
	return nil
}
func (p *canaryProbe) AssessWorkflowCapacity(context.Context, string, string) (CapacityAssessment, error) {
	switch {
	case p.stopAfter != "" && slices.Contains(p.log, "run "+p.stopAfter):
		return CapacityAssessment{Status: "cancelled"}, nil
	case p.pauseAfter != "" && slices.Contains(p.log, "run "+p.pauseAfter):
		return CapacityAssessment{Status: "paused", Revision: 1}, nil
	}
	return CapacityAssessment{Status: "running"}, nil
}
func (p *canaryProbe) CanaryRamp(context.Context, string, string) (CanaryRamp, error) {
	p.log = append(p.log, "ramp")
	return p.ramp, nil
}
func (p *canaryProbe) CanaryWeigh(_ context.Context, _, _ string, step, weight int) (time.Time, error) {
	p.log = append(p.log, fmt.Sprintf("weigh %d %d", step, weight))
	return weighedAt.Add(time.Duration(step) * time.Hour), nil
}
func (p *canaryProbe) CanaryJudge(_ context.Context, _, _ string, step, attempt int, w releasehealth.Window) (CanaryJudgement, error) {
	p.log = append(p.log, fmt.Sprintf("judge %d %d", step, attempt))
	p.windows = append(p.windows, w)
	if j, ok := p.verdicts[[2]int{step, attempt}]; ok {
		return j, nil
	}
	return CanaryJudgement{Verdict: string(releasehealth.Pass)}, nil
}
func (p *canaryProbe) CanaryPromote(context.Context, string, string) error {
	p.log = append(p.log, "promote")
	return p.promoteErr
}
func (p *canaryProbe) CanaryRevert(_ context.Context, _, _, reason string) error {
	p.log = append(p.log, "revert "+reason)
	return p.revertErr
}

// count is how many times the probe logged entry.
func (p *canaryProbe) count(entry string) int {
	n := 0
	for _, e := range p.log {
		if e == entry {
			n++
		}
	}
	return n
}

// pumpCanary runs workflow, activity and timer tasks until none is left
// that could run soon, or until done reports true. Timers in these tests
// are a millisecond, so a short quiet spell means nothing is left.
func pumpCanary(t *testing.T, w *drt.Worker, done func() bool) {
	t.Helper()
	idle := 0
	for range 100000 {
		worked := false
		for _, kind := range []durable.TaskKind{durable.TaskWorkflow, durable.TaskActivity, durable.TaskTimer} {
			ok, err := w.RunOnce(t.Context(), kind)
			if err != nil {
				t.Fatal(err)
			}
			worked = worked || ok
			if ok && done != nil && done() {
				return
			}
		}
		if worked {
			idle = 0
			continue
		}
		if idle++; idle > 30 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the workflow did not quiesce")
}

// canaryPlan is a v4 plan run waiting for its decision.
type canaryPlan struct {
	store durable.Store
	d     *Deployments
	w     *drt.Worker
	key   durable.Key
}

func startCanaryPlan(t *testing.T, p Deployer) *canaryPlan {
	t.Helper()
	c := &canaryPlan{store: memory.New(), d: NewDeployments()}
	c.d.deployer, c.d.planner, c.d.engineOf = p, capacityPlanner{}, func() *engine.Engine { return nil }
	var err error
	if c.w, err = drt.NewWorker(c.store, c.d.CanaryOptions("canary", 1)); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(DeploymentInput{OrgID: "org", PlanID: "plan", Window: time.Hour, ReconcileInterval: capacityInterval})
	c.key = durable.Key{Namespace: DurableNamespace, WorkflowID: "plan", RunID: runID}
	req := durable.StartRequest{Key: c.key, RequestID: "start:plan", WorkflowType: WorkflowDeployment, BuildID: CanaryBuild, Queue: DurableQueue, Input: raw}
	if _, err = c.w.StartExecution(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	pumpCanary(t, c.w, nil)
	return c
}

// approve sends the plan's one decision and runs until done or quiet.
func (c *canaryPlan) approve(t *testing.T, done func() bool) {
	t.Helper()
	dec, _ := json.Marshal(Decision{Approve: true, Revision: "r1", By: "usr"})
	if _, err := c.w.SignalExecution(t.Context(), durable.SignalRequest{Key: c.key, RequestID: "approve", BuildID: CanaryBuild, Name: SignalDecision, Input: dec}); err != nil {
		t.Fatal(err)
	}
	pumpCanary(t, c.w, done)
}

func (c *canaryPlan) execution(t *testing.T) durable.Execution {
	t.Helper()
	e, err := c.store.GetExecution(t.Context(), c.key)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (c *canaryPlan) commands(t *testing.T) []string {
	t.Helper()
	return commandIDs(t, c.store, c.key)
}

// scheduled reports whether the run has scheduled command.
func (c *canaryPlan) scheduled(t *testing.T, command string) func() bool {
	return func() bool { return slices.Contains(c.commands(t), command) }
}

func (c *canaryPlan) outcome(t *testing.T) Outcome {
	t.Helper()
	e := c.execution(t)
	if e.State != durable.StateCompleted {
		t.Fatalf("run %s, want completed", e.State)
	}
	var o Outcome
	if err := json.Unmarshal(e.Output, &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestCanaryWorkflowPromotesAfterEveryStepPasses(t *testing.T) {
	p := newCanaryProbe(t)
	c := startCanaryPlan(t, p)
	c.approve(t, nil)
	want := []string{"source", "run first", "run second", "ramp",
		"weigh 0 5", "judge 0 0", "weigh 1 25", "judge 1 0", "weigh 2 50", "judge 2 0", "weigh 3 100", "judge 3 0",
		"promote", "finish "}
	if !slices.Equal(p.log, want) {
		t.Fatalf("effects %q", p.log)
	}
	if o := c.outcome(t); o.Status != PlanApproved || o.Deployment != "dep" {
		t.Fatalf("outcome %+v", o)
	}
	if len(p.started) != 1 || !IsCanaryContext(p.started[0]) || !IsCapacityContext(p.started[0]) {
		t.Fatal("the deployment didn't start under the canary and capacity marks")
	}
	// The approval loop is v3's, command for command.
	if got := c.commands(t); !slices.Equal(got[:6], v3DeploymentCommands[:6]) {
		t.Fatalf("approval commands %q", got[:6])
	}
}

func TestCanaryWorkflowNeedsNoFurtherDecision(t *testing.T) {
	p := newCanaryProbe(t)
	c := startCanaryPlan(t, p)
	c.approve(t, nil)
	c.outcome(t)
	var receives []string
	for _, command := range c.commands(t) {
		if strings.HasPrefix(command, "signal ") {
			receives = append(receives, command)
		}
	}
	if !slices.Equal(receives, []string{"signal decision-1"}) {
		t.Fatalf("the run waited for %q", receives)
	}
}

func TestCanaryWorkflowRevertsOnFail(t *testing.T) {
	p := newCanaryProbe(t)
	p.verdicts = map[[2]int]CanaryJudgement{{1, 0}: {Verdict: "fail", Reason: "web: 3.2% of requests failed, over 1%"}}
	c := startCanaryPlan(t, p)
	c.approve(t, nil)
	reason := "The canary failed at 25%: web: 3.2% of requests failed, over 1%"
	want := []string{"source", "run first", "run second", "ramp", "weigh 0 5", "judge 0 0", "weigh 1 25", "judge 1 0", "revert " + reason, "finish " + reason}
	if !slices.Equal(p.log, want) {
		t.Fatalf("effects %q", p.log)
	}
	c.outcome(t)
}

func TestCanaryInconclusiveFailsSafeAfterBound(t *testing.T) {
	p := newCanaryProbe(t)
	p.verdicts = map[[2]int]CanaryJudgement{}
	for k := range 4 {
		p.verdicts[[2]int{0, k}] = CanaryJudgement{Verdict: "inconclusive", Reason: "web: 12 requests, under 100"}
	}
	c := startCanaryPlan(t, p)
	c.approve(t, nil)
	var judged []string
	for _, command := range c.commands(t) {
		if strings.Contains(command, "-judge-") {
			judged = append(judged, command)
		}
	}
	if !slices.Equal(judged, []string{"activity dep-canary-judge-0-0", "activity dep-canary-judge-0-1", "activity dep-canary-judge-0-2", "activity dep-canary-judge-0-3"}) {
		t.Fatalf("judgements %q", judged)
	}
	// Each hold judges the window after the one before.
	for k, w := range p.windows {
		start := weighedAt.Add(time.Duration(k) * p.ramp.Hold)
		if !w.Start.Equal(start) || !w.End.Equal(start.Add(p.ramp.Hold)) {
			t.Fatalf("window %d is %v to %v", k, w.Start, w.End)
		}
	}
	reason := "The canary couldn't be judged at 5% after 4 holds: web: 12 requests, under 100"
	if p.count("revert "+reason) != 1 || p.count("finish "+reason) != 1 || p.count("weigh 1 25") != 0 {
		t.Fatalf("effects %q", p.log)
	}
}

func TestCanaryUnavailableMetricsFailSafe(t *testing.T) {
	p := newCanaryProbe(t)
	p.verdicts = map[[2]int]CanaryJudgement{}
	for k := range 4 {
		p.verdicts[[2]int{0, k}] = CanaryJudgement{Verdict: "inconclusive", Reason: "web: the cloud's metrics are unavailable"}
	}
	c := startCanaryPlan(t, p)
	c.approve(t, nil)
	reason := "The canary couldn't be judged at 5% after 4 holds: web: the cloud's metrics are unavailable"
	if p.count("judge 0 3") != 1 || p.count("revert "+reason) != 1 || p.count("finish "+reason) != 1 || p.count("weigh 1 25") != 0 || p.count("promote") != 0 {
		t.Fatalf("effects %q", p.log)
	}
}

func TestCanaryInconclusiveThenPassContinues(t *testing.T) {
	p := newCanaryProbe(t)
	p.verdicts = map[[2]int]CanaryJudgement{{0, 0}: {Verdict: "inconclusive", Reason: "web: 12 requests, under 100"}}
	c := startCanaryPlan(t, p)
	c.approve(t, nil)
	want := []string{"weigh 0 5", "judge 0 0", "judge 0 1", "weigh 1 25"}
	if i := slices.Index(p.log, "weigh 0 5"); i < 0 || !slices.Equal(p.log[i:i+4], want) {
		t.Fatalf("effects %q", p.log)
	}
	if p.count("promote") != 1 || p.count("finish ") != 1 {
		t.Fatalf("effects %q", p.log)
	}
	if !slices.Contains(c.commands(t), "timer dep-canary-hold-0-1") {
		t.Fatal("the inconclusive step didn't hold again")
	}
}

func TestCanaryRevertKeepsTryingThenSaysTrafficMayBeSplit(t *testing.T) {
	p := newCanaryProbe(t)
	p.verdicts = map[[2]int]CanaryJudgement{{0, 0}: {Verdict: "fail", Reason: "web: too slow"}}
	p.revertErr = errors.New("web: the cloud refused the change")
	c := startCanaryPlan(t, p)
	c.approve(t, nil)
	reason := "The canary failed at 5%: web: too slow"
	if n := p.count("revert " + reason); n != canarySettleRounds {
		t.Fatalf("%d reverts, want %d", n, canarySettleRounds)
	}
	var waits int
	for _, command := range c.commands(t) {
		if strings.HasPrefix(command, "timer dep-canary-revert-wait-") {
			waits++
		}
	}
	if waits != canarySettleRounds-1 {
		t.Fatalf("%d waits between %d reverts", waits, canarySettleRounds)
	}
	finish := p.log[len(p.log)-1]
	if !strings.HasPrefix(finish, "finish "+reason+" Traffic may still be split: ") || !strings.Contains(finish, "the cloud refused the change") {
		t.Fatalf("finished with %q", finish)
	}
}

func TestCanaryRevertsDarkCandidatesWhenAnActionFails(t *testing.T) {
	p := newCanaryProbe(t)
	p.failAction = "second"
	c := startCanaryPlan(t, p)
	c.approve(t, nil)
	reason := "second: quota exceeded"
	want := []string{"source", "run first", "run second", "ramp", "revert " + reason, "finish " + reason}
	if !slices.Equal(p.log, want) {
		t.Fatalf("effects %q", p.log)
	}
}

func TestCanaryWorkflowWithoutCanaryMatchesV3(t *testing.T) {
	run := func(options func(*Deployments) drt.Options, build string) (*canaryProbe, []string) {
		p := newCanaryProbe(t)
		p.ramp = CanaryRamp{}
		s := memory.New()
		d := NewDeployments()
		d.deployer = p
		w, err := drt.NewWorker(s, options(d))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(retryInput{OrgID: "org", DeploymentID: "dep", Steps: p.steps, ReconcileInterval: capacityInterval})
		req := durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: "retry", RunID: runID}, RequestID: "start:retry", WorkflowType: WorkflowRetry, BuildID: build, Queue: DurableQueue, Input: raw}
		if _, err = w.StartExecution(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		pumpCanary(t, w, nil)
		return p, commandIDs(t, s, req.Key)
	}
	v3, v3Commands := run(func(d *Deployments) drt.Options { return d.CapacityOptions("v3", 1) }, CapacityBuild)
	v4, v4Commands := run(func(d *Deployments) drt.Options { return d.CanaryOptions("v4", 1) }, CanaryBuild)
	finish := slices.Index(v3.log, "finish ")
	wantLog := slices.Insert(slices.Clone(v3.log), finish, "ramp")
	if !slices.Equal(v4.log, wantLog) {
		t.Fatalf("v4 effects %q, v3 %q", v4.log, v3.log)
	}
	at := slices.Index(v3Commands, "activity dep-finish-0-capacity-0")
	wantCommands := slices.Insert(slices.Clone(v3Commands), at, "activity dep-canary-ramp")
	if !slices.Equal(v4Commands, wantCommands) {
		t.Fatalf("v4 commands %q", v4Commands)
	}
}

func TestCanaryWorkflowReplaysAcrossWorkerRestart(t *testing.T) {
	p := newCanaryProbe(t)
	c := startCanaryPlan(t, p)
	c.approve(t, c.scheduled(t, "timer dep-canary-hold-1-0"))
	if p.count("judge 1 0") != 0 {
		t.Fatal("the pump ran past the step-1 hold")
	}
	var err error
	if c.w, err = drt.NewWorker(c.store, c.d.CanaryOptions("canary-restarted", 1)); err != nil {
		t.Fatal(err)
	}
	pumpCanary(t, c.w, nil)
	want := []string{"source", "run first", "run second", "ramp",
		"weigh 0 5", "judge 0 0", "weigh 1 25", "judge 1 0", "weigh 2 50", "judge 2 0", "weigh 3 100", "judge 3 0",
		"promote", "finish "}
	if !slices.Equal(p.log, want) {
		t.Fatalf("effects %q", p.log)
	}
	c.outcome(t)
}

func TestCanaryRetryRunsTheRampAgain(t *testing.T) {
	p := newCanaryProbe(t)
	s := memory.New()
	d := NewDeployments()
	d.deployer = p
	w, err := drt.NewWorker(s, d.CanaryOptions("retry", 1))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(retryInput{OrgID: "org", DeploymentID: "dep", Steps: p.steps, ReconcileInterval: capacityInterval})
	req := durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: "dep.retry.1", RunID: runID}, RequestID: "start:dep.retry.1", WorkflowType: WorkflowRetry, BuildID: CanaryBuild, Queue: DurableQueue, Input: raw}
	if _, err = w.StartExecution(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	pumpCanary(t, w, nil)
	want := []string{"source", "run first", "run second", "ramp",
		"weigh 0 5", "judge 0 0", "weigh 1 25", "judge 1 0", "weigh 2 50", "judge 2 0", "weigh 3 100", "judge 3 0",
		"promote", "finish "}
	if !slices.Equal(p.log, want) {
		t.Fatalf("effects %q", p.log)
	}
	if e, _ := s.GetExecution(t.Context(), req.Key); e.State != durable.StateCompleted {
		t.Fatalf("retry %s", e.State)
	}
}

func TestCanaryRevertsWhenCapacityStopsTheDeployment(t *testing.T) {
	p := newCanaryProbe(t)
	p.stopAfter = "first"
	c := startCanaryPlan(t, p)
	c.approve(t, nil)
	// Capacity settled the deployment as cancelled, so nothing finishes it.
	want := []string{"source", "run first", "ramp", "revert " + canaryStopped}
	if !slices.Equal(p.log, want) {
		t.Fatalf("effects %q", p.log)
	}
	c.outcome(t)
}

func TestCanaryCancelRevertsCandidates(t *testing.T) {
	cancel := func(t *testing.T, c *canaryPlan) {
		t.Helper()
		if _, err := c.w.RequestCancelExecution(t.Context(), durable.CancelExecutionRequest{Key: c.key, RequestID: "cancel", BuildID: CanaryBuild, Reason: "superseded by plan_2"}); err != nil {
			t.Fatal(err)
		}
		pumpCanary(t, c.w, nil)
		if e := c.execution(t); e.State != durable.StateCancelled {
			t.Fatalf("run %s, want cancelled", e.State)
		}
	}
	reason := canaryStopped + ": superseded by plan_2"
	t.Run("during a hold", func(t *testing.T) {
		p := newCanaryProbe(t)
		c := startCanaryPlan(t, p)
		c.approve(t, c.scheduled(t, "timer dep-canary-hold-1-0"))
		cancel(t, c)
		want := []string{"source", "run first", "run second", "ramp", "weigh 0 5", "judge 0 0", "weigh 1 25", "revert " + reason}
		if !slices.Equal(p.log, want) {
			t.Fatalf("effects %q", p.log)
		}
	})
	t.Run("before the ramp", func(t *testing.T) {
		p := newCanaryProbe(t)
		p.pauseAfter = "first"
		c := startCanaryPlan(t, p)
		c.approve(t, nil)
		cancel(t, c)
		want := []string{"source", "run first", "ramp", "revert " + reason}
		if !slices.Equal(p.log, want) {
			t.Fatalf("effects %q", p.log)
		}
		if !slices.Contains(c.commands(t), "activity dep-canary-cancel-ramp") {
			t.Fatal("the cleanup didn't read the ramp")
		}
	})
	t.Run("after the promotion", func(t *testing.T) {
		p := newCanaryProbe(t)
		p.pauseAfter = "second" // capacity pauses Finish once the ramp promoted
		c := startCanaryPlan(t, p)
		c.approve(t, nil)
		if p.count("promote") != 1 {
			t.Fatalf("effects %q", p.log)
		}
		cancel(t, c)
		if p.count("promote") != 1 || slices.ContainsFunc(p.log, func(e string) bool { return strings.HasPrefix(e, "revert") }) {
			t.Fatalf("effects %q", p.log)
		}
	})
	t.Run("during the promotion", func(t *testing.T) {
		p := newCanaryProbe(t)
		p.promoteErr = errors.New("api: the cloud is busy")
		c := startCanaryPlan(t, p)
		c.approve(t, c.scheduled(t, "timer dep-canary-promote-wait-0"))
		p.promoteErr = nil
		cancel(t, c)
		// Every step passed, so the cleanup finishes the promotion.
		if p.count("promote") != 2 || slices.ContainsFunc(p.log, func(e string) bool { return strings.HasPrefix(e, "revert") }) {
			t.Fatalf("effects %q", p.log)
		}
		if !slices.Contains(c.commands(t), "activity dep-canary-cancel-promote-0") {
			t.Fatal("the cleanup didn't promote")
		}
	})
	t.Run("waiting for approval", func(t *testing.T) {
		p := newCanaryProbe(t)
		c := startCanaryPlan(t, p)
		cancel(t, c)
		if len(p.log) != 0 {
			t.Fatalf("effects %q", p.log)
		}
	})
}

func TestCanaryActivitiesNeedACanaryDeployer(t *testing.T) {
	d := NewDeployments()
	d.deployer = &capacityProbe{calls: map[string]int{}}
	raw, _ := json.Marshal(deployInput{OrgID: "org", DeploymentID: "dep"})
	if _, err := d.canaryRamp(t.Context(), drt.ActivityInfo{}, raw); err == nil || err.Error() != "canary releases are unavailable" {
		t.Fatalf("ramp: %v", err)
	}
}

// routedWorkflows has an engine for DeploymentBuild and a worker for each
// later build, all on one store.
type routedWorkflows struct {
	store  *memory.Store
	d      *Deployments
	w3, w4 *drt.Worker
}

func newRoutedWorkflows(t *testing.T) *routedWorkflows {
	t.Helper()
	r := &routedWorkflows{store: memory.New(), d: NewDeployments()}
	disp, err := dispatch.New(dispatch.WithStore(r.store))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Build(disp, engine.WithDurableWorkflows(r.d.Options("engine", 1)))
	if err != nil {
		t.Fatal(err)
	}
	r.d.Bind(capacityPlanner{}, newCanaryProbe(t), func() *engine.Engine { return eng })
	if r.w3, err = drt.NewWorker(r.store, r.d.CapacityOptions("v3", 1)); err != nil {
		t.Fatal(err)
	}
	if r.w4, err = drt.NewWorker(r.store, r.d.CanaryOptions("v4", 1)); err != nil {
		t.Fatal(err)
	}
	r.d.BindWorker(CanaryBuild, r.w4)
	r.d.BindWorker(CapacityBuild, r.w3)
	return r
}

func planRequest(plan, build string) durable.StartRequest {
	raw, _ := json.Marshal(DeploymentInput{OrgID: "org", PlanID: plan, Window: time.Hour, ReconcileInterval: capacityInterval})
	return durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: plan, RunID: runID}, RequestID: "start:" + plan, WorkflowType: WorkflowDeployment, BuildID: build, Queue: DurableQueue, Input: raw}
}

func TestBuildRoutingStartsSignalsAndCancels(t *testing.T) {
	r := newRoutedWorkflows(t)
	for plan, build := range map[string]string{"plan-2": DeploymentBuild, "plan-3": CapacityBuild, "plan-4": CanaryBuild} {
		if err := r.d.startRun(t.Context(), DeploymentRun{OrgID: "org", PlanID: plan, Request: planRequest(plan, build)}); err != nil {
			t.Fatalf("start %s: %v", build, err)
		}
		e, err := r.store.GetExecution(t.Context(), durable.Key{Namespace: DurableNamespace, WorkflowID: plan, RunID: runID})
		if err != nil || e.BuildID != build {
			t.Fatalf("%s started as %+v, %v", plan, e, err)
		}
	}
	pumpCanary(t, r.w3, nil)
	pumpCanary(t, r.w4, nil)
	for plan, build := range map[string]string{"plan-3": CapacityBuild, "plan-4": CanaryBuild} {
		key := durable.Key{Namespace: DurableNamespace, WorkflowID: plan, RunID: runID}
		if err := r.d.signalRun(t.Context(), durable.SignalRequest{Key: key, RequestID: "wake:" + plan, BuildID: build, Name: SignalCapacity, Input: []byte("{}")}); err != nil {
			t.Fatalf("signal %s: %v", build, err)
		}
		if err := r.d.Cancel(t.Context(), plan, "superseded"); err != nil {
			t.Fatalf("cancel %s: %v", build, err)
		}
	}
	if err := r.d.Cancel(t.Context(), "plan-2", "superseded"); err != nil {
		t.Fatalf("cancel v2: %v", err)
	}
	pumpCanary(t, r.w3, nil)
	pumpCanary(t, r.w4, nil)
	for _, plan := range []string{"plan-3", "plan-4"} {
		e, err := r.store.GetExecution(t.Context(), durable.Key{Namespace: DurableNamespace, WorkflowID: plan, RunID: runID})
		if err != nil || e.State != durable.StateCancelled {
			t.Fatalf("%s after cancel: %+v %v", plan, e, err)
		}
	}
	unknown := DeploymentRun{OrgID: "org", PlanID: "plan-9", Request: planRequest("plan-9", "deployment-v9")}
	if err := r.d.startRun(t.Context(), unknown); err == nil || !strings.Contains(err.Error(), `"deployment-v9"`) {
		t.Fatalf("start on an unknown build: %v", err)
	}
	if err := r.d.signalRun(t.Context(), durable.SignalRequest{Key: unknown.Request.Key, RequestID: "x", BuildID: "deployment-v9", Name: SignalCapacity, Input: []byte("{}")}); err == nil || !strings.Contains(err.Error(), `"deployment-v9"`) {
		t.Fatalf("signal on an unknown build: %v", err)
	}
}

// memRuns is a RunStore in memory.
type memRuns struct {
	runs []DeploymentRun
	deps map[string]string // deployment to plan
}

func (m *memRuns) Save(_ context.Context, r DeploymentRun) (DeploymentRun, error) {
	for _, saved := range m.runs {
		if saved.Request.WorkflowID == r.Request.WorkflowID {
			return saved, nil
		}
	}
	m.runs = append(m.runs, r)
	return r, nil
}
func (*memRuns) AcknowledgeStart(context.Context, string, string) error   { return nil }
func (*memRuns) AttachPlan(context.Context, string, string, string) error { return nil }
func (m *memRuns) ForPlan(_ context.Context, plan string) (DeploymentRun, error) {
	for i := len(m.runs) - 1; i >= 0; i-- {
		if m.runs[i].PlanID == plan {
			return m.runs[i], nil
		}
	}
	return DeploymentRun{}, durable.ErrNotFound
}
func (m *memRuns) ForDeployment(ctx context.Context, _, dep string) (DeploymentRun, error) {
	return m.ForPlan(ctx, m.deps[dep])
}

func TestRunsStartOnTheirBuild(t *testing.T) {
	interval := func(t *testing.T, r DeploymentRun) time.Duration {
		t.Helper()
		var in struct {
			ReconcileInterval time.Duration `json:"reconcile_interval"`
		}
		if err := json.Unmarshal(r.Request.Input, &in); err != nil {
			t.Fatal(err)
		}
		return in.ReconcileInterval
	}
	t.Run("a new plan without a run store starts on the engine's build", func(t *testing.T) {
		r := newRoutedWorkflows(t)
		if err := r.d.Start(t.Context(), "org", "plan", time.Hour); err != nil {
			t.Fatal(err)
		}
		e, err := r.store.GetExecution(t.Context(), durable.Key{Namespace: DurableNamespace, WorkflowID: "plan", RunID: runID})
		if err != nil || e.BuildID != DeploymentBuild {
			t.Fatalf("%+v %v", e, err)
		}
	})
	t.Run("a new plan starts on the current build", func(t *testing.T) {
		r := newRoutedWorkflows(t)
		runs := &memRuns{}
		r.d.BindRunStore(runs)
		if err := r.d.Start(t.Context(), "org", "plan", time.Hour); err != nil {
			t.Fatal(err)
		}
		saved, _ := runs.ForPlan(t.Context(), "plan")
		if saved.Request.BuildID != CurrentBuild || interval(t, saved) != capacityInterval {
			t.Fatalf("started %+v", saved.Request)
		}
	})
	for _, build := range []string{CapacityBuild, CanaryBuild} {
		t.Run("a retry and a resumed release keep "+build, func(t *testing.T) {
			r := newRoutedWorkflows(t)
			runs := &memRuns{deps: map[string]string{"dep": "plan"}}
			r.d.BindRunStore(runs)
			if err := r.d.startRun(t.Context(), DeploymentRun{OrgID: "org", PlanID: "plan", Request: planRequest("plan", build)}); err != nil {
				t.Fatal(err)
			}
			if err := r.d.Retry(t.Context(), "org", "dep", "dep.retry.1", [][]string{{"first"}}); err != nil {
				t.Fatal(err)
			}
			saved, _ := runs.ForPlan(t.Context(), "plan")
			if saved.Request.WorkflowID != "dep.retry.1" || saved.Request.BuildID != build || interval(t, saved) != capacityInterval {
				t.Fatalf("retry %+v", saved.Request)
			}
			if err := r.d.Cancel(t.Context(), "plan", "closed"); err != nil {
				t.Fatal(err)
			}
			pumpCanary(t, r.w3, nil)
			pumpCanary(t, r.w4, nil)
			if err := r.d.ResumeRelease(t.Context(), "org", "plan"); err != nil {
				t.Fatal(err)
			}
			want := "resume-release-v4:plan"
			if build == CapacityBuild {
				want = "resume-release-v3:plan"
			}
			saved, _ = runs.ForPlan(t.Context(), "plan")
			if saved.Request.WorkflowID != want || saved.Request.BuildID != build || interval(t, saved) != capacityInterval {
				t.Fatalf("resumed release %+v", saved.Request)
			}
			if _, err := r.store.GetExecution(t.Context(), saved.Request.Key); err != nil {
				t.Fatalf("the resumed release didn't start: %v", err)
			}
		})
	}
}
