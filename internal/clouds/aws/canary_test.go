package aws

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/smithy-go"
)

const (
	testListenerARN   = "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/loomplane/50dc6c495c0c9188/f2f7dc8efc522ab2"
	testBaselineTG    = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/lp-api-1/6d0ecf831eec9f09"
	testCandidateTG   = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/lp-api-canary-2/0f5dbd7c3a1e2b44"
	testOtherTG       = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/lp-web-3/9a8b7c6d5e4f3a2b"
	testOtherListener = "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/loomplane/50dc6c495c0c9188/0a1b2c3d4e5f6a7b"
)

// fakeListeners keeps a load balancer's listeners and records what's
// asked of them.
type fakeListeners struct {
	listeners []elbtypes.Listener
	modified  []*elb.ModifyListenerInput
	created   []*elb.CreateListenerInput
}

func (f *fakeListeners) DescribeListeners(_ context.Context, in *elb.DescribeListenersInput, _ ...func(*elb.Options)) (*elb.DescribeListenersOutput, error) {
	if len(in.ListenerArns) == 0 {
		return &elb.DescribeListenersOutput{Listeners: f.listeners}, nil
	}
	var out []elbtypes.Listener
	for _, l := range f.listeners {
		for _, arn := range in.ListenerArns {
			if sdk.ToString(l.ListenerArn) == arn {
				out = append(out, l)
			}
		}
	}
	if len(out) == 0 {
		return nil, &smithy.GenericAPIError{Code: "ListenerNotFound", Message: "no listener"}
	}
	return &elb.DescribeListenersOutput{Listeners: out}, nil
}

func (f *fakeListeners) ModifyListener(_ context.Context, in *elb.ModifyListenerInput, _ ...func(*elb.Options)) (*elb.ModifyListenerOutput, error) {
	f.modified = append(f.modified, in)
	for i, l := range f.listeners {
		if sdk.ToString(l.ListenerArn) == sdk.ToString(in.ListenerArn) {
			f.listeners[i].DefaultActions = in.DefaultActions
		}
	}
	return &elb.ModifyListenerOutput{}, nil
}

func (f *fakeListeners) CreateListener(_ context.Context, in *elb.CreateListenerInput, _ ...func(*elb.Options)) (*elb.CreateListenerOutput, error) {
	f.created = append(f.created, in)
	l := elbtypes.Listener{ListenerArn: sdk.String("arn:new"), Port: in.Port, Protocol: in.Protocol, DefaultActions: in.DefaultActions}
	f.listeners = append(f.listeners, l)
	return &elb.CreateListenerOutput{Listeners: []elbtypes.Listener{l}}, nil
}

func plainForward(arn string, port int32, tg string) elbtypes.Listener {
	return elbtypes.Listener{
		ListenerArn: sdk.String(arn), Port: sdk.Int32(port), Protocol: elbtypes.ProtocolEnumHttp,
		DefaultActions: []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: sdk.String(tg)}},
	}
}

func TestSetListenerWeightsWritesOneForward(t *testing.T) {
	f := &fakeListeners{listeners: []elbtypes.Listener{plainForward(testListenerARN, 80, testBaselineTG)}}
	err := setListenerWeights(t.Context(), f, testListenerARN, []TargetWeight{{testBaselineTG, 95}, {testCandidateTG, 5}})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.modified) != 1 || sdk.ToString(f.modified[0].ListenerArn) != testListenerARN {
		t.Fatalf("modified %+v", f.modified)
	}
	acts := f.modified[0].DefaultActions
	if len(acts) != 1 {
		t.Fatalf("default actions %+v", acts)
	}
	a := acts[0]
	if a.Type != elbtypes.ActionTypeEnumForward || a.TargetGroupArn != nil || a.ForwardConfig == nil {
		t.Fatalf("action %+v", a)
	}
	tgs := a.ForwardConfig.TargetGroups
	if len(tgs) != 2 || sdk.ToString(tgs[0].TargetGroupArn) != testBaselineTG || sdk.ToInt32(tgs[0].Weight) != 95 ||
		sdk.ToString(tgs[1].TargetGroupArn) != testCandidateTG || sdk.ToInt32(tgs[1].Weight) != 5 {
		t.Fatalf("target groups %+v", tgs)
	}
	if st := a.ForwardConfig.TargetGroupStickinessConfig; st == nil || st.Enabled == nil || *st.Enabled {
		t.Fatalf("stickiness %+v", st)
	}
}

func TestSetListenerWeightsAtZeroKeepsTheCandidateAttached(t *testing.T) {
	f := &fakeListeners{listeners: []elbtypes.Listener{plainForward(testListenerARN, 80, testBaselineTG)}}
	if err := setListenerWeights(t.Context(), f, testListenerARN, []TargetWeight{{testBaselineTG, 100}, {testCandidateTG, 0}}); err != nil {
		t.Fatal(err)
	}
	tgs := f.modified[0].DefaultActions[0].ForwardConfig.TargetGroups
	if len(tgs) != 2 || sdk.ToString(tgs[1].TargetGroupArn) != testCandidateTG || tgs[1].Weight == nil || *tgs[1].Weight != 0 {
		t.Fatalf("target groups %+v", tgs)
	}
}

func TestSetListenerWeightsForTheBaselineAloneIsAPlainForward(t *testing.T) {
	f := &fakeListeners{listeners: []elbtypes.Listener{plainForward(testListenerARN, 80, testBaselineTG)}}
	if err := setListenerWeights(t.Context(), f, testListenerARN, []TargetWeight{{testBaselineTG, 100}}); err != nil {
		t.Fatal(err)
	}
	a := f.modified[0].DefaultActions[0]
	if a.Type != elbtypes.ActionTypeEnumForward || sdk.ToString(a.TargetGroupArn) != testBaselineTG || a.ForwardConfig != nil {
		t.Fatalf("action %+v", a)
	}
}

func TestSetListenerWeightsRefusesWhatALBWouldnt(t *testing.T) {
	for name, w := range map[string][]TargetWeight{
		"nothing":        nil,
		"no target":      {{"", 100}},
		"negative":       {{testBaselineTG, -1}, {testCandidateTG, 101}},
		"over 999":       {{testBaselineTG, 1000}},
		"the same twice": {{testBaselineTG, 50}, {testBaselineTG, 50}},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeListeners{listeners: []elbtypes.Listener{plainForward(testListenerARN, 80, testBaselineTG)}}
			if err := setListenerWeights(t.Context(), f, testListenerARN, w); err == nil || len(f.modified) != 0 {
				t.Fatalf("err %v, modified %+v", err, f.modified)
			}
		})
	}
}

func TestListenerWeightsReadsAPlainForward(t *testing.T) {
	f := &fakeListeners{listeners: []elbtypes.Listener{plainForward(testListenerARN, 80, testBaselineTG)}}
	got, err := listenerWeights(t.Context(), f, testListenerARN)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (TargetWeight{testBaselineTG, 100}) {
		t.Fatalf("weights %+v", got)
	}
}

func TestListenerWeightsReadsAWeightedForward(t *testing.T) {
	f := &fakeListeners{listeners: []elbtypes.Listener{plainForward(testListenerARN, 80, testBaselineTG)}}
	if err := setListenerWeights(t.Context(), f, testListenerARN, []TargetWeight{{testBaselineTG, 75}, {testCandidateTG, 25}}); err != nil {
		t.Fatal(err)
	}
	got, err := listenerWeights(t.Context(), f, testListenerARN)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (TargetWeight{testBaselineTG, 75}) || got[1] != (TargetWeight{testCandidateTG, 25}) {
		t.Fatalf("weights %+v", got)
	}
}

func TestListenerWeightsReadsOneWeightedTargetAsAll(t *testing.T) {
	// ALB describes a plain forward with a forward config holding the one
	// target group at weight 1.
	l := plainForward(testListenerARN, 80, "")
	l.DefaultActions[0].TargetGroupArn = nil
	l.DefaultActions[0].ForwardConfig = &elbtypes.ForwardActionConfig{TargetGroups: []elbtypes.TargetGroupTuple{
		{TargetGroupArn: sdk.String(testBaselineTG), Weight: sdk.Int32(1)},
	}}
	f := &fakeListeners{listeners: []elbtypes.Listener{l}}
	got, err := listenerWeights(t.Context(), f, testListenerARN)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (TargetWeight{testBaselineTG, 100}) {
		t.Fatalf("weights %+v", got)
	}
}

// fakeTargetHealth answers DescribeTargetHealth with each poll's targets in
// turn, then the last forever.
type fakeTargetHealth struct {
	polls [][]elbtypes.TargetHealthDescription
	asked int
}

func (f *fakeTargetHealth) DescribeTargetHealth(_ context.Context, _ *elb.DescribeTargetHealthInput, _ ...func(*elb.Options)) (*elb.DescribeTargetHealthOutput, error) {
	i := min(f.asked, len(f.polls)-1)
	f.asked++
	return &elb.DescribeTargetHealthOutput{TargetHealthDescriptions: f.polls[i]}, nil
}

func target(ip string, state elbtypes.TargetHealthStateEnum, reason elbtypes.TargetHealthReasonEnum, why string) elbtypes.TargetHealthDescription {
	d := elbtypes.TargetHealthDescription{
		Target:       &elbtypes.TargetDescription{Id: sdk.String(ip), Port: sdk.Int32(8080)},
		TargetHealth: &elbtypes.TargetHealth{State: state, Reason: reason},
	}
	if why != "" {
		d.TargetHealth.Description = sdk.String(why)
	}
	return d
}

func noWait(context.Context) error { return nil }

func TestWaitTargetsHealthyFailsOnUnhealthy(t *testing.T) {
	f := &fakeTargetHealth{polls: [][]elbtypes.TargetHealthDescription{
		{target("10.0.1.5", elbtypes.TargetHealthStateEnumInitial, elbtypes.TargetHealthReasonEnumRegistrationInProgress, "")},
		{target("10.0.1.5", elbtypes.TargetHealthStateEnumUnhealthy, elbtypes.TargetHealthReasonEnumResponseCodeMismatch, "Health checks failed with these codes: [502]")},
	}}
	err := waitTargetsHealthy(t.Context(), f, testCandidateTG, 4, noWait)
	if err == nil {
		t.Fatal("unhealthy targets passed")
	}
	for _, want := range []string{"10.0.1.5:8080", "unhealthy", "Target.ResponseCodeMismatch", "[502]", testCandidateTG} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q doesn't say %q", err, want)
		}
	}
	if f.asked != 4 {
		t.Fatalf("polled %d times, want the whole budget of 4", f.asked)
	}
}

func TestWaitTargetsHealthyWaitsForEveryTargetAndIgnoresDraining(t *testing.T) {
	f := &fakeTargetHealth{polls: [][]elbtypes.TargetHealthDescription{
		{},
		{target("10.0.1.5", elbtypes.TargetHealthStateEnumInitial, elbtypes.TargetHealthReasonEnumInitialHealthChecking, "")},
		{
			target("10.0.1.5", elbtypes.TargetHealthStateEnumHealthy, "", ""),
			target("10.0.1.6", elbtypes.TargetHealthStateEnumDraining, elbtypes.TargetHealthReasonEnumDeregistrationInProgress, ""),
		},
	}}
	if err := waitTargetsHealthy(t.Context(), f, testBaselineTG, 10, noWait); err != nil {
		t.Fatal(err)
	}
	if f.asked != 3 {
		t.Fatalf("polled %d times", f.asked)
	}
}

func TestWaitTargetsHealthyNeedsATarget(t *testing.T) {
	f := &fakeTargetHealth{polls: [][]elbtypes.TargetHealthDescription{{}}}
	err := waitTargetsHealthy(t.Context(), f, testBaselineTG, 2, noWait)
	if err == nil || !strings.Contains(err.Error(), "no targets") {
		t.Fatalf("err %v", err)
	}
}

// fakeECSServices keeps services by name, and records updates and
// deletes. A deleted service drains for draining polls, then is
// inactive.
type fakeECSServices struct {
	services map[string]string // name to status
	draining int
	updated  []*ecs.UpdateServiceInput
	deleted  []*ecs.DeleteServiceInput
	err      error
}

func (f *fakeECSServices) DescribeServices(_ context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := &ecs.DescribeServicesOutput{}
	for _, name := range in.Services {
		status, ok := f.services[name]
		if !ok {
			out.Failures = append(out.Failures, ecstypes.Failure{Arn: sdk.String(name), Reason: sdk.String("MISSING")})
			continue
		}
		if status == "DRAINING" {
			if f.draining > 0 {
				f.draining--
			} else {
				f.services[name] = "INACTIVE"
			}
		}
		out.Services = append(out.Services, ecstypes.Service{ServiceName: sdk.String(name), Status: sdk.String(status)})
	}
	return out, nil
}

func (f *fakeECSServices) UpdateService(_ context.Context, in *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
	f.updated = append(f.updated, in)
	return &ecs.UpdateServiceOutput{}, nil
}

func (f *fakeECSServices) DeleteService(_ context.Context, in *ecs.DeleteServiceInput, _ ...func(*ecs.Options)) (*ecs.DeleteServiceOutput, error) {
	f.deleted = append(f.deleted, in)
	f.services[sdk.ToString(in.Service)] = "DRAINING"
	return &ecs.DeleteServiceOutput{}, nil
}

func TestRemoveServiceIsIdempotent(t *testing.T) {
	t.Run("a missing service is removed", func(t *testing.T) {
		f := &fakeECSServices{services: map[string]string{}}
		if err := removeService(t.Context(), f, "loomplane", "api-canary", 3, noWait); err != nil {
			t.Fatal(err)
		}
		if len(f.updated)+len(f.deleted) != 0 {
			t.Fatalf("updated %+v, deleted %+v", f.updated, f.deleted)
		}
	})
	t.Run("an inactive service is removed", func(t *testing.T) {
		f := &fakeECSServices{services: map[string]string{"api-canary": "INACTIVE"}}
		if err := removeService(t.Context(), f, "loomplane", "api-canary", 3, noWait); err != nil {
			t.Fatal(err)
		}
		if len(f.updated)+len(f.deleted) != 0 {
			t.Fatalf("updated %+v, deleted %+v", f.updated, f.deleted)
		}
	})
	t.Run("an active one scales to nothing, is deleted and drains", func(t *testing.T) {
		f := &fakeECSServices{services: map[string]string{"api-canary": "ACTIVE"}, draining: 2}
		if err := removeService(t.Context(), f, "loomplane", "api-canary", 5, noWait); err != nil {
			t.Fatal(err)
		}
		if len(f.updated) != 1 || sdk.ToInt32(f.updated[0].DesiredCount) != 0 || sdk.ToString(f.updated[0].Service) != "api-canary" ||
			sdk.ToString(f.updated[0].Cluster) != "loomplane" {
			t.Fatalf("updated %+v", f.updated)
		}
		if len(f.deleted) != 1 || !sdk.ToBool(f.deleted[0].Force) || sdk.ToString(f.deleted[0].Service) != "api-canary" {
			t.Fatalf("deleted %+v", f.deleted)
		}
		if f.services["api-canary"] != "INACTIVE" {
			t.Fatalf("status %s", f.services["api-canary"])
		}
		// Again: nothing more to do.
		if err := removeService(t.Context(), f, "loomplane", "api-canary", 5, noWait); err != nil {
			t.Fatal(err)
		}
		if len(f.updated) != 1 || len(f.deleted) != 1 {
			t.Fatalf("removed twice: updated %d, deleted %d", len(f.updated), len(f.deleted))
		}
	})
	t.Run("a service that never stops draining is an error", func(t *testing.T) {
		f := &fakeECSServices{services: map[string]string{"api-canary": "ACTIVE"}, draining: 100}
		err := removeService(t.Context(), f, "loomplane", "api-canary", 3, noWait)
		if err == nil || !strings.Contains(err.Error(), "draining") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("a failed read is an error", func(t *testing.T) {
		f := &fakeECSServices{err: errors.New("throttled")}
		if err := removeService(t.Context(), f, "loomplane", "api-canary", 3, noWait); err == nil {
			t.Fatal("no error")
		}
	})
}

// fakeTargetGroupDeleter deletes target groups, failing with err.
type fakeTargetGroupDeleter struct {
	deleted []string
	err     error
}

func (f *fakeTargetGroupDeleter) DeleteTargetGroup(_ context.Context, in *elb.DeleteTargetGroupInput, _ ...func(*elb.Options)) (*elb.DeleteTargetGroupOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.deleted = append(f.deleted, sdk.ToString(in.TargetGroupArn))
	return &elb.DeleteTargetGroupOutput{}, nil
}

func TestRemoveTargetGroupIsIdempotent(t *testing.T) {
	f := &fakeTargetGroupDeleter{}
	if err := removeTargetGroup(t.Context(), f, testCandidateTG); err != nil || len(f.deleted) != 1 {
		t.Fatalf("err %v, deleted %v", err, f.deleted)
	}
	gone := &fakeTargetGroupDeleter{err: &smithy.GenericAPIError{Code: "TargetGroupNotFound", Message: "gone"}}
	if err := removeTargetGroup(t.Context(), gone, testCandidateTG); err != nil {
		t.Fatalf("a missing target group: %v", err)
	}
	inUse := &fakeTargetGroupDeleter{err: &smithy.GenericAPIError{Code: "ResourceInUse", Message: "in use by a listener"}}
	if err := removeTargetGroup(t.Context(), inUse, testCandidateTG); err == nil {
		t.Fatal("a target group in use was removed")
	}
}
