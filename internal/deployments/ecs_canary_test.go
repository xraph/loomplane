package deployments

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/ecs"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

const (
	testBaselineGroup = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/lp-api-production-1/6d0ecf831eec9f09"
	testListener      = "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/loomplane/50dc6c495c0c9188/f2f7dc8efc522ab2"
	testBaselineImage = "123456789012.dkr.ecr.us-east-1.amazonaws.com/loomplane/api:aaaaaaaaaaaa"
	testCandidateImg  = "123456789012.dkr.ecr.us-east-1.amazonaws.com/loomplane/api:bbbbbbbbbbbb"
)

// fakeCanaryCloud keeps what an ECS canary changes in AWS: one listener's
// weights, target groups, services and the images they run, and which
// target groups pass their health checks. fail makes the next call of a
// method fail with its error.
type fakeCanaryCloud struct {
	weights  []aws.TargetWeight
	groups   map[string]bool
	services map[string]string // name to image
	healthy  map[string]bool   // target group to whether its targets pass
	fail     map[string]error
	calls    []string
	// atRun is the listener's weights when ECS first used the candidate's
	// target group.
	atRun []aws.TargetWeight
}

func newFakeCanaryCloud() *fakeCanaryCloud {
	return &fakeCanaryCloud{
		weights:  []aws.TargetWeight{{TargetGroup: testBaselineGroup, Weight: 100}},
		groups:   map[string]bool{testBaselineGroup: true},
		services: map[string]string{"inst-api": testBaselineImage},
		healthy:  map[string]bool{testBaselineGroup: true},
		fail:     map[string]error{},
	}
}

func (f *fakeCanaryCloud) call(name string) error {
	f.calls = append(f.calls, name)
	if err := f.fail[name]; err != nil {
		delete(f.fail, name)
		return err
	}
	return nil
}

func (f *fakeCanaryCloud) SetListenerWeights(_ context.Context, listener string, w []aws.TargetWeight) error {
	if err := f.call("SetListenerWeights"); err != nil {
		return err
	}
	if listener != testListener {
		return &smithy.GenericAPIError{Code: "ListenerNotFound", Message: listener}
	}
	for _, tw := range w {
		if !f.groups[tw.TargetGroup] {
			return &smithy.GenericAPIError{Code: "TargetGroupNotFound", Message: tw.TargetGroup}
		}
	}
	f.weights = slices.Clone(w)
	return nil
}

func (f *fakeCanaryCloud) EnsureTargetGroup(_ context.Context, name, _ string, _ int32) (string, error) {
	if err := f.call("EnsureTargetGroup"); err != nil {
		return "", err
	}
	arn := "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/" + name + "/0f5dbd7c3a1e2b44"
	f.groups[arn] = true
	return arn, nil
}

func (f *fakeCanaryCloud) WaitServiceStable(_ context.Context, _, name string) error {
	if err := f.call("WaitServiceStable"); err != nil {
		return err
	}
	if f.services[name] == "" {
		return fmt.Errorf("ecs has no service %s", name)
	}
	return nil
}

func (f *fakeCanaryCloud) WaitTargetsHealthy(_ context.Context, tg string) error {
	if err := f.call("WaitTargetsHealthy"); err != nil {
		return err
	}
	if !f.healthy[tg] {
		return fmt.Errorf("the targets in %s didn't pass their health checks: 10.0.1.5:8080 is unhealthy", tg)
	}
	return nil
}

func (f *fakeCanaryCloud) ServiceImage(_ context.Context, _, name string) (string, error) {
	if err := f.call("ServiceImage"); err != nil {
		return "", err
	}
	return f.services[name], nil
}

func (f *fakeCanaryCloud) RemoveService(_ context.Context, _, name string) error {
	if err := f.call("RemoveService"); err != nil {
		return err
	}
	delete(f.services, name)
	return nil
}

func (f *fakeCanaryCloud) RemoveTargetGroup(_ context.Context, tg string) error {
	if err := f.call("RemoveTargetGroup"); err != nil {
		return err
	}
	for _, w := range f.weights {
		if w.TargetGroup == tg {
			return &smithy.GenericAPIError{Code: "ResourceInUse", Message: tg + " is in use by a listener"}
		}
	}
	delete(f.groups, tg)
	return nil
}

func testECSCanary(f *fakeCanaryCloud) *ecsCanary {
	return &ecsCanary{
		cloud: f, cluster: ecsCluster, listener: testListener, baseline: testBaselineGroup,
		service: "inst-api", candidate: canaryService("inst-api"),
	}
}

// startCandidate runs the candidate the way ECS would: its service comes
// up, and its targets pass when healthy says so.
func (f *fakeCanaryCloud) startCandidate(healthy bool) func(context.Context, string) error {
	return func(_ context.Context, tg string) error {
		if err := f.call("RunCandidate"); err != nil {
			return err
		}
		if f.atRun == nil {
			f.atRun = slices.Clone(f.weights)
		}
		f.services[canaryService("inst-api")] = testCandidateImg
		f.healthy[tg] = healthy
		return nil
	}
}

func candidateGroupName() string { return canaryTargetGroup("api-production-x1") }

// assertBaselineAlone checks that the canary left nothing behind: the
// listener forwards to the baseline alone, and no candidate service or
// target group is left.
func (f *fakeCanaryCloud) assertBaselineAlone(t *testing.T) {
	t.Helper()
	if len(f.weights) != 1 || f.weights[0] != (aws.TargetWeight{TargetGroup: testBaselineGroup, Weight: 100}) {
		t.Fatalf("weights %+v", f.weights)
	}
	if _, ok := f.services[canaryService("inst-api")]; ok {
		t.Fatalf("the candidate service is left: %+v", f.services)
	}
	for g := range f.groups {
		if g != testBaselineGroup {
			t.Fatalf("target group %s is left", g)
		}
	}
}

func TestECSCanaryNames(t *testing.T) {
	if got := canaryService("loomplane-abc"); got != "loomplane-abc-canary" {
		t.Fatalf("canaryService = %q", got)
	}
	for _, name := range []string{"api-production-x1", "a-very-long-service-name-in-production-abcdefgh"} {
		got := canaryTargetGroup(name)
		baseline := shortName(name)
		if len(got) > 32 || got == baseline || strings.Contains(got, "--") {
			t.Fatalf("canaryTargetGroup(%q) = %q beside the baseline's %q", name, got, baseline)
		}
	}
}

func TestECSCanaryStartsTheCandidateDark(t *testing.T) {
	f := newFakeCanaryCloud()
	c := testECSCanary(f)
	tg, err := c.start(t.Context(), candidateGroupName(), "vpc-1", 8080, f.startCandidate(true))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tg, candidateGroupName()) || tg == testBaselineGroup {
		t.Fatalf("candidate group %s", tg)
	}
	want := []aws.TargetWeight{{TargetGroup: testBaselineGroup, Weight: 100}, {TargetGroup: tg, Weight: 0}}
	if !slices.Equal(f.weights, want) {
		t.Fatalf("weights %+v", f.weights)
	}
	// The group is attached before ECS registers the candidate in it.
	if !slices.Equal(f.atRun, want) {
		t.Fatalf("weights when the candidate started %+v", f.atRun)
	}
	if f.services[canaryService("inst-api")] != testCandidateImg || f.services["inst-api"] != testBaselineImage {
		t.Fatalf("services %+v", f.services)
	}
	if !slices.Contains(f.calls, "WaitTargetsHealthy") {
		t.Fatalf("never waited for health checks: %v", f.calls)
	}
}

func TestECSCanaryStartReplacesAnEarlierCandidate(t *testing.T) {
	f := newFakeCanaryCloud()
	c := testECSCanary(f)
	first, err := c.start(t.Context(), candidateGroupName(), "vpc-1", 8080, f.startCandidate(true))
	if err != nil {
		t.Fatal(err)
	}
	// An earlier attempt got as far as a split before it was lost.
	if err = c.weigh(t.Context(), first, 50); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	second, err := c.start(t.Context(), candidateGroupName(), "vpc-1", 8080, f.startCandidate(true))
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("a second candidate group %s beside %s", second, first)
	}
	want := []aws.TargetWeight{{TargetGroup: testBaselineGroup, Weight: 100}, {TargetGroup: second, Weight: 0}}
	if !slices.Equal(f.weights, want) {
		t.Fatalf("weights %+v", f.weights)
	}
	// Traffic goes back to the baseline before the old candidate goes,
	// and the old candidate goes before the new one starts.
	order := []string{"SetListenerWeights", "RemoveService", "EnsureTargetGroup", "SetListenerWeights", "RunCandidate"}
	if !slices.Equal(f.calls[:len(order)], order) {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestECSCanaryAFailedStartCleansUp(t *testing.T) {
	for name, tc := range map[string]struct {
		healthy bool
		setup   func(f *fakeCanaryCloud, c *ecsCanary)
		says    string
	}{
		"the service doesn't start": {true, func(f *fakeCanaryCloud, _ *ecsCanary) {
			f.fail["RunCandidate"] = errors.New("ecs couldn't roll out the candidate")
		}, "couldn't roll out"},
		"its targets fail": {false, func(*fakeCanaryCloud, *ecsCanary) {}, "didn't pass their health checks"},
		// The first write puts the baseline alone; the second, attaching
		// the candidate's group, fails.
		"the group can't be attached": {true, func(f *fakeCanaryCloud, c *ecsCanary) {
			c.cloud = &failNthSplit{fakeCanaryCloud: f, n: 2, calls: new(int)}
		}, "attach"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeCanaryCloud()
			c := testECSCanary(f)
			tc.setup(f, c)
			_, err := c.start(t.Context(), candidateGroupName(), "vpc-1", 8080, f.startCandidate(tc.healthy))
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("err %v", err)
			}
			f.assertBaselineAlone(t)
		})
	}
}

// failNthSplit fails the nth SetListenerWeights.
type failNthSplit struct {
	*fakeCanaryCloud
	n     int
	calls *int
}

func (f *failNthSplit) SetListenerWeights(ctx context.Context, listener string, w []aws.TargetWeight) error {
	*f.calls++
	if *f.calls == f.n {
		f.fakeCanaryCloud.calls = append(f.fakeCanaryCloud.calls, "SetListenerWeights")
		return errors.New("throttled")
	}
	return f.fakeCanaryCloud.SetListenerWeights(ctx, listener, w)
}

func TestECSCanaryAFailedCleanupSaysWhatMayBeLeft(t *testing.T) {
	f := newFakeCanaryCloud()
	f.fail["RunCandidate"] = errors.New("ecs couldn't roll out the candidate")
	// The first removal starts clean; the second is the cleanup's.
	c := testECSCanary(f)
	c.cloud = &failingRemoval{fakeCanaryCloud: f, after: 1, removals: new(int)}
	_, err := c.start(t.Context(), candidateGroupName(), "vpc-1", 8080, f.startCandidate(true))
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"couldn't roll out", "access denied", canaryService("inst-api"), candidateGroupName(), "may be left"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q doesn't say %q", err, want)
		}
	}
	// The listener still went back to the baseline alone.
	if len(f.weights) != 1 || f.weights[0].TargetGroup != testBaselineGroup {
		t.Fatalf("weights %+v", f.weights)
	}
}

// failingRemoval fails every RemoveService after the first after.
type failingRemoval struct {
	*fakeCanaryCloud
	after    int
	removals *int
}

func (f *failingRemoval) RemoveService(ctx context.Context, cluster, name string) error {
	*f.removals++
	if *f.removals > f.after {
		return errors.New("access denied")
	}
	return f.fakeCanaryCloud.RemoveService(ctx, cluster, name)
}

func startedCanary(t *testing.T) (*fakeCanaryCloud, *ecsCanary, string) {
	t.Helper()
	f := newFakeCanaryCloud()
	c := testECSCanary(f)
	tg, err := c.start(t.Context(), candidateGroupName(), "vpc-1", 8080, f.startCandidate(true))
	if err != nil {
		t.Fatal(err)
	}
	return f, c, tg
}

func TestECSCanaryWeighs(t *testing.T) {
	f, c, tg := startedCanary(t)
	for _, p := range []int{5, 25, 25, 100, 0} {
		if err := c.weigh(t.Context(), tg, p); err != nil {
			t.Fatal(err)
		}
		want := []aws.TargetWeight{{TargetGroup: testBaselineGroup, Weight: int32(100 - p)}, {TargetGroup: tg, Weight: int32(p)}}
		if !slices.Equal(f.weights, want) {
			t.Fatalf("at %d%%: %+v", p, f.weights)
		}
	}
	for _, p := range []int{-1, 101} {
		if err := c.weigh(t.Context(), tg, p); err == nil {
			t.Fatalf("weighed %d%%", p)
		}
	}
}

func TestECSCanaryWeighsZeroOnceTheCandidateIsGone(t *testing.T) {
	f, c, tg := startedCanary(t)
	if err := c.remove(t.Context(), tg); err != nil {
		t.Fatal(err)
	}
	// A revert retried after its target group went still starts with 0%.
	if err := c.weigh(t.Context(), tg, 0); err != nil {
		t.Fatal(err)
	}
	f.assertBaselineAlone(t)
	if err := c.weigh(t.Context(), tg, 5); err == nil {
		t.Fatal("sent traffic to a target group that's gone")
	}
	f.assertBaselineAlone(t)
}

func TestECSCanaryRevertIsSafeToRepeat(t *testing.T) {
	f, c, tg := startedCanary(t)
	if err := c.weigh(t.Context(), tg, 25); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := c.remove(t.Context(), tg); err != nil {
			t.Fatal(err)
		}
		f.assertBaselineAlone(t)
	}
}

func TestECSCanaryRevertResumesAfterAPartialRun(t *testing.T) {
	for _, step := range []string{"SetListenerWeights", "RemoveService", "RemoveTargetGroup"} {
		t.Run(step, func(t *testing.T) {
			f, c, tg := startedCanary(t)
			if err := c.weigh(t.Context(), tg, 25); err != nil {
				t.Fatal(err)
			}
			f.fail[step] = errors.New("throttled")
			if err := c.remove(t.Context(), tg); err == nil {
				t.Fatal("no error")
			}
			// The runner retries a revert as weights to 0, then Revert.
			if err := c.weigh(t.Context(), tg, 0); err != nil {
				t.Fatal(err)
			}
			if err := c.remove(t.Context(), tg); err != nil {
				t.Fatal(err)
			}
			f.assertBaselineAlone(t)
		})
	}
}

// roll is the Ctrl Plane roll of the baseline to the candidate's image:
// it records the listener's weights while it runs.
func (f *fakeCanaryCloud) roll(during *[][]aws.TargetWeight) func(context.Context) error {
	return func(context.Context) error {
		if err := f.call("Roll"); err != nil {
			return err
		}
		*during = append(*during, slices.Clone(f.weights))
		f.services["inst-api"] = testCandidateImg
		return nil
	}
}

func TestECSCanaryPromoteRollsTheBaselineWhileTheCandidateHoldsEverything(t *testing.T) {
	f, c, tg := startedCanary(t)
	if err := c.weigh(t.Context(), tg, 50); err != nil {
		t.Fatal(err)
	}
	var during [][]aws.TargetWeight
	for range 2 {
		if err := c.promote(t.Context(), tg, testCandidateImg, f.roll(&during)); err != nil {
			t.Fatal(err)
		}
		f.assertBaselineAlone(t)
	}
	if len(during) != 1 {
		t.Fatalf("rolled %d times", len(during))
	}
	want := []aws.TargetWeight{{TargetGroup: testBaselineGroup, Weight: 0}, {TargetGroup: tg, Weight: 100}}
	if !slices.Equal(during[0], want) {
		t.Fatalf("weights while the baseline rolled %+v", during[0])
	}
	if f.services["inst-api"] != testCandidateImg {
		t.Fatalf("the baseline runs %s", f.services["inst-api"])
	}
}

func TestECSCanaryPromoteWaitsForAHealthyBaseline(t *testing.T) {
	f, c, tg := startedCanary(t)
	var during [][]aws.TargetWeight
	f.healthy[testBaselineGroup] = false
	err := c.promote(t.Context(), tg, testCandidateImg, f.roll(&during))
	if err == nil || !strings.Contains(err.Error(), "health checks") || !strings.Contains(err.Error(), "candidate keeps all of the traffic") {
		t.Fatalf("err %v", err)
	}
	// Traffic stays on the candidate, which is healthy.
	want := []aws.TargetWeight{{TargetGroup: testBaselineGroup, Weight: 0}, {TargetGroup: tg, Weight: 100}}
	if !slices.Equal(f.weights, want) || f.services[canaryService("inst-api")] == "" || !f.groups[tg] {
		t.Fatalf("weights %+v, services %+v, groups %+v", f.weights, f.services, f.groups)
	}
	// Once the baseline passes, a retry finishes the promote.
	f.healthy[testBaselineGroup] = true
	if err := c.promote(t.Context(), tg, testCandidateImg, f.roll(&during)); err != nil {
		t.Fatal(err)
	}
	f.assertBaselineAlone(t)
}

func TestECSCanaryPromoteWaitsForAStableBaseline(t *testing.T) {
	f, c, tg := startedCanary(t)
	var during [][]aws.TargetWeight
	f.fail["WaitServiceStable"] = errors.New("ecs couldn't roll out inst-api: tasks failed to start")
	err := c.promote(t.Context(), tg, testCandidateImg, f.roll(&during))
	if err == nil || !strings.Contains(err.Error(), "tasks failed to start") {
		t.Fatalf("err %v", err)
	}
	want := []aws.TargetWeight{{TargetGroup: testBaselineGroup, Weight: 0}, {TargetGroup: tg, Weight: 100}}
	if !slices.Equal(f.weights, want) {
		t.Fatalf("weights %+v", f.weights)
	}
}

func TestECSCanaryAFailedRollLeavesTheCandidateHoldingEverything(t *testing.T) {
	f, c, tg := startedCanary(t)
	var during [][]aws.TargetWeight
	f.fail["Roll"] = errors.New("deploy to ecs: the rollout failed")
	if err := c.promote(t.Context(), tg, testCandidateImg, f.roll(&during)); err == nil {
		t.Fatal("no error")
	}
	want := []aws.TargetWeight{{TargetGroup: testBaselineGroup, Weight: 0}, {TargetGroup: tg, Weight: 100}}
	if !slices.Equal(f.weights, want) || f.services[canaryService("inst-api")] == "" {
		t.Fatalf("weights %+v, services %+v", f.weights, f.services)
	}
}

func TestECSCanaryPromoteResumesAfterAPartialRun(t *testing.T) {
	for _, step := range []string{"SetListenerWeights", "RemoveService", "RemoveTargetGroup"} {
		t.Run(step, func(t *testing.T) {
			f, c, tg := startedCanary(t)
			var during [][]aws.TargetWeight
			// The first write gives the candidate everything; fail the one
			// that gives it back to the baseline, or what follows.
			if step == "SetListenerWeights" {
				c.cloud = &failNthSplit{fakeCanaryCloud: f, n: 2, calls: new(int)}
			} else {
				f.fail[step] = errors.New("throttled")
			}
			if err := c.promote(t.Context(), tg, testCandidateImg, f.roll(&during)); err == nil {
				t.Fatal("no error")
			}
			c.cloud = f
			if err := c.promote(t.Context(), tg, testCandidateImg, f.roll(&during)); err != nil {
				t.Fatal(err)
			}
			f.assertBaselineAlone(t)
			if f.services["inst-api"] != testCandidateImg {
				t.Fatalf("the baseline runs %s", f.services["inst-api"])
			}
			// Each roll ran with the candidate holding everything.
			for _, w := range during {
				if len(w) != 2 || w[1].TargetGroup != tg || w[1].Weight != 100 {
					t.Fatalf("a roll ran at %+v", w)
				}
			}
		})
	}
}

func TestECSCanaryPromoteRefusesWhenTheCandidateWentBeforeTheBaselineRolled(t *testing.T) {
	f, c, tg := startedCanary(t)
	if err := c.weigh(t.Context(), tg, 50); err != nil {
		t.Fatal(err)
	}
	delete(f.services, canaryService("inst-api"))
	var during [][]aws.TargetWeight
	err := c.promote(t.Context(), tg, testCandidateImg, f.roll(&during))
	if err == nil || !strings.Contains(err.Error(), "nothing was promoted") {
		t.Fatalf("err %v", err)
	}
	if len(during) != 0 {
		t.Fatal("rolled the baseline")
	}
	// Traffic went back to what still runs.
	if len(f.weights) != 1 || f.weights[0].TargetGroup != testBaselineGroup {
		t.Fatalf("weights %+v", f.weights)
	}
}

func TestECSCanaryTargetRoundTrips(t *testing.T) {
	c := testECSCanary(newFakeCanaryCloud())
	tgt := c.target("arn:candidate", testCandidateImg)
	if tgt.Baseline != testBaselineGroup || tgt.Candidate != "arn:candidate" || tgt.Image != testCandidateImg {
		t.Fatalf("target %+v", tgt)
	}
	back, err := ecsCanaryFor(c.cloud, tgt)
	if err != nil {
		t.Fatal(err)
	}
	if back.cluster != c.cluster || back.listener != c.listener || back.baseline != c.baseline ||
		back.service != c.service || back.candidate != c.candidate {
		t.Fatalf("read back %+v from %+v", back, c)
	}
	delete(tgt.Refs, ecsRefListener)
	if _, err := ecsCanaryFor(c.cloud, tgt); err == nil {
		t.Fatal("a target without its listener")
	}
}

// ECS refuses a canary for a service whose baseline has no load balancer
// before it builds anything.
func TestECSCanaryNeedsALoadBalancer(t *testing.T) {
	const region = "us-east-1"
	inst := ctrlInstance("api-production-x1", ecs.Key("conn_1", region))
	inst.Labels = map[string]string{ecs.LabelCluster: ecsCluster}
	e := &ECS{Ctrl: CtrlPlane{Providers: cpprovider.NewRegistry(), Instances: &fakeInstances{byID: map[id.ID]*instance.Instance{inst.ID: inst}}}}
	job := &Job{
		Action:     planning.Action{Name: "api", Service: &planning.ServiceConfig{Port: 8080, Public: true}},
		Deployment: &Deployment{ProjectID: "proj_01h2xcejqtf2nbrexx3vqjhp41", Environment: "production"},
		Connection: &connections.Connection{ID: "conn_1", Details: map[string]string{"region": region}},
		Binding:    &planning.Binding{ExternalID: inst.ID.String()},
		Progress:   func(string, string) {},
	}
	_, err := e.ApplyCandidate(t.Context(), job)
	if !errors.Is(err, ErrNoExecutor) || !strings.Contains(err.Error(), "it isn't behind a load balancer") {
		t.Fatalf("err %v", err)
	}
	job.Binding = nil
	if _, err = e.ApplyCandidate(t.Context(), job); !errors.Is(err, ErrNoExecutor) {
		t.Fatalf("without a baseline: %v", err)
	}
}
