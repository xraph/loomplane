package aws

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/xraph/loomplane/internal/clouds/progress"
)

// How many polls a canary waits for targets to pass their health checks,
// and for a removed service to finish draining. A poll is five seconds
// against AWS, so each is ten minutes: an ALB's default health check
// takes two and a half to call a target healthy, and its default
// deregistration delay is five.
const (
	healthPolls = 120
	drainPolls  = 120
)

// TargetWeight is the share of a listener's traffic one target group
// gets. ALB splits a forward by weight, from 0 to 999; a canary uses
// percentages, so the weights it writes add up to 100.
type TargetWeight struct {
	TargetGroup string
	Weight      int32
}

// listenerAPI is the part of ELB that reads, opens and changes listeners.
type listenerAPI interface {
	DescribeListeners(context.Context, *elb.DescribeListenersInput, ...func(*elb.Options)) (*elb.DescribeListenersOutput, error)
	CreateListener(context.Context, *elb.CreateListenerInput, ...func(*elb.Options)) (*elb.CreateListenerOutput, error)
	ModifyListener(context.Context, *elb.ModifyListenerInput, ...func(*elb.Options)) (*elb.ModifyListenerOutput, error)
}

// targetHealthAPI is the part of ELB that reads how a target group's
// targets answer their health checks.
type targetHealthAPI interface {
	DescribeTargetHealth(context.Context, *elb.DescribeTargetHealthInput, ...func(*elb.Options)) (*elb.DescribeTargetHealthOutput, error)
}

// targetGroupDeleter is the part of ELB that deletes target groups.
type targetGroupDeleter interface {
	DeleteTargetGroup(context.Context, *elb.DeleteTargetGroupInput, ...func(*elb.Options)) (*elb.DeleteTargetGroupOutput, error)
}

// ecsServiceAPI is the part of ECS that reads, scales and deletes
// services.
type ecsServiceAPI interface {
	DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	UpdateService(context.Context, *ecs.UpdateServiceInput, ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error)
	DeleteService(context.Context, *ecs.DeleteServiceInput, ...func(*ecs.Options)) (*ecs.DeleteServiceOutput, error)
}

// forwardsTo reports whether a listener's default forward sends traffic,
// at any weight, to targetGroup.
func forwardsTo(l elbtypes.Listener, targetGroup string) bool {
	for _, w := range forwardWeights(l.DefaultActions) {
		if w.TargetGroup == targetGroup {
			return true
		}
	}
	return false
}

// forwardWeights reads the first forward among actions. A plain forward,
// or a weighted one with a single target group, sends that group
// everything.
func forwardWeights(actions []elbtypes.Action) []TargetWeight {
	for _, a := range actions {
		if a.Type != elbtypes.ActionTypeEnumForward {
			continue
		}
		if a.ForwardConfig != nil && len(a.ForwardConfig.TargetGroups) > 0 {
			tgs := a.ForwardConfig.TargetGroups
			if len(tgs) == 1 {
				return []TargetWeight{{TargetGroup: sdk.ToString(tgs[0].TargetGroupArn), Weight: 100}}
			}
			out := make([]TargetWeight, 0, len(tgs))
			for _, tg := range tgs {
				out = append(out, TargetWeight{TargetGroup: sdk.ToString(tg.TargetGroupArn), Weight: sdk.ToInt32(tg.Weight)})
			}
			return out
		}
		if arn := sdk.ToString(a.TargetGroupArn); arn != "" {
			return []TargetWeight{{TargetGroup: arn, Weight: 100}}
		}
	}
	return nil
}

// ListenerWeights reads how a listener splits its traffic between target
// groups.
func (s *Session) ListenerWeights(ctx context.Context, listener string) ([]TargetWeight, error) {
	return listenerWeights(ctx, elb.NewFromConfig(s.cfg), listener)
}

func listenerWeights(ctx context.Context, api listenerAPI, listener string) ([]TargetWeight, error) {
	got, err := api.DescribeListeners(ctx, &elb.DescribeListenersInput{ListenerArns: []string{listener}})
	if err != nil {
		return nil, fmt.Errorf("read listener %s: %w", listener, err)
	}
	if len(got.Listeners) == 0 {
		return nil, fmt.Errorf("read listener %s: %w", listener, errNotFound)
	}
	return forwardWeights(got.Listeners[0].DefaultActions), nil
}

// SetListenerWeights makes a listener's default action one forward that
// splits its traffic between target groups by weight, without
// stickiness, so each request is split afresh. A target group at weight
// 0 stays attached: it takes no traffic, and its targets are still
// health checked. One target group alone gets a plain forward, the shape
// EnsureListener opens a listener with.
//
// It assumes what's true of every listener Loomplane opens: the listener
// is one service's alone, and its default action carries all of its
// traffic, since Loomplane adds no rules. A listener shared by several
// services, or one with rules, would need the split on each rule.
func (s *Session) SetListenerWeights(ctx context.Context, listener string, weights []TargetWeight) error {
	return setListenerWeights(ctx, elb.NewFromConfig(s.cfg), listener, weights)
}

func setListenerWeights(ctx context.Context, api listenerAPI, listener string, weights []TargetWeight) error {
	if len(weights) == 0 {
		return fmt.Errorf("listener %s needs a target group to forward to", listener)
	}
	seen := map[string]bool{}
	tgs := make([]elbtypes.TargetGroupTuple, 0, len(weights))
	for _, w := range weights {
		switch {
		case w.TargetGroup == "":
			return fmt.Errorf("listener %s can't forward to a target group with no ARN", listener)
		case w.Weight < 0 || w.Weight > 999:
			return fmt.Errorf("listener %s can't give %s a weight of %d: ALB takes 0 to 999", listener, w.TargetGroup, w.Weight)
		case seen[w.TargetGroup]:
			return fmt.Errorf("listener %s can't forward to %s twice", listener, w.TargetGroup)
		}
		seen[w.TargetGroup] = true
		tgs = append(tgs, elbtypes.TargetGroupTuple{TargetGroupArn: sdk.String(w.TargetGroup), Weight: sdk.Int32(w.Weight)})
	}
	action := elbtypes.Action{Type: elbtypes.ActionTypeEnumForward}
	if len(weights) == 1 {
		action.TargetGroupArn = sdk.String(weights[0].TargetGroup)
	} else {
		action.ForwardConfig = &elbtypes.ForwardActionConfig{
			TargetGroups:                tgs,
			TargetGroupStickinessConfig: &elbtypes.TargetGroupStickinessConfig{Enabled: sdk.Bool(false)},
		}
	}
	if _, err := api.ModifyListener(ctx, &elb.ModifyListenerInput{
		ListenerArn: sdk.String(listener), DefaultActions: []elbtypes.Action{action},
	}); err != nil {
		return fmt.Errorf("split listener %s's traffic: %w", listener, err)
	}
	return nil
}

// WaitTargetsHealthy polls until every target in a target group that
// isn't draining passes its health checks, and there's at least one.
// Targets still failing after about ten minutes are an error that says
// why ALB marked each one as it did.
func (s *Session) WaitTargetsHealthy(ctx context.Context, targetGroup string) error {
	return waitTargetsHealthy(ctx, elb.NewFromConfig(s.cfg), targetGroup, healthPolls, s.wait)
}

func waitTargetsHealthy(ctx context.Context, api targetHealthAPI, targetGroup string, polls int, wait func(context.Context) error) error {
	var why string
	for i := 1; ; i++ {
		got, err := api.DescribeTargetHealth(ctx, &elb.DescribeTargetHealthInput{TargetGroupArn: sdk.String(targetGroup)})
		if err != nil {
			return fmt.Errorf("read the health of target group %s: %w", targetGroup, err)
		}
		var healthy bool
		if healthy, why = targetsHealthy(got.TargetHealthDescriptions); healthy {
			return nil
		}
		if i >= polls {
			return fmt.Errorf("the targets in %s didn't pass their health checks: %s", targetGroup, why)
		}
		progress.Report(ctx, "Waiting for health checks: "+why)
		if err := wait(ctx); err != nil {
			return err
		}
	}
}

// targetsHealthy reports whether every target that isn't draining is
// healthy, and there's at least one, and if not, why.
func targetsHealthy(targets []elbtypes.TargetHealthDescription) (ok bool, why string) {
	var healthy int
	var failing []string
	for _, t := range targets {
		h := t.TargetHealth
		if h == nil {
			continue
		}
		switch h.State {
		case elbtypes.TargetHealthStateEnumDraining:
			continue
		case elbtypes.TargetHealthStateEnumHealthy:
			healthy++
			continue
		}
		name := "a target"
		if t.Target != nil {
			name = sdk.ToString(t.Target.Id)
			if t.Target.Port != nil {
				name += fmt.Sprintf(":%d", *t.Target.Port)
			}
		}
		line := name + " is " + string(h.State)
		if h.Reason != "" {
			line += " (" + string(h.Reason) + ")"
		}
		if d := sdk.ToString(h.Description); d != "" {
			line += ": " + d
		}
		failing = append(failing, line)
	}
	switch {
	case len(failing) > 0:
		return false, strings.Join(failing, "; ")
	case healthy == 0:
		return false, "no targets are registered yet"
	}
	return true, ""
}

// RemoveService scales a service to nothing, deletes it and waits until
// it has drained, so a service by the same name can start again. One
// that's missing or inactive already is removed.
func (s *Session) RemoveService(ctx context.Context, cluster, name string) error {
	return removeService(ctx, ecs.NewFromConfig(s.cfg), cluster, name, drainPolls, s.wait)
}

func removeService(ctx context.Context, api ecsServiceAPI, cluster, name string, polls int, wait func(context.Context) error) error {
	status, err := serviceStatus(ctx, api, cluster, name)
	if err != nil {
		return err
	}
	if status == "ACTIVE" {
		if _, uerr := api.UpdateService(ctx, &ecs.UpdateServiceInput{
			Cluster: sdk.String(cluster), Service: sdk.String(name), DesiredCount: sdk.Int32(0),
		}); uerr != nil && !gone(uerr) {
			return fmt.Errorf("scale service %s to nothing: %w", name, uerr)
		}
		if _, derr := api.DeleteService(ctx, &ecs.DeleteServiceInput{
			Cluster: sdk.String(cluster), Service: sdk.String(name), Force: sdk.Bool(true),
		}); derr != nil && !gone(derr) {
			return fmt.Errorf("delete service %s: %w", name, derr)
		}
	}
	for left := polls; status != "" && status != "INACTIVE"; left-- {
		if left == 0 {
			return fmt.Errorf("ecs is still draining service %s", name)
		}
		progress.Report(ctx, "Waiting for "+name+" to drain")
		if werr := wait(ctx); werr != nil {
			return werr
		}
		if status, err = serviceStatus(ctx, api, cluster, name); err != nil {
			return err
		}
	}
	return nil
}

// serviceStatus is a service's status in ECS, or "" when it's missing.
func serviceStatus(ctx context.Context, api ecsServiceAPI, cluster, name string) (string, error) {
	got, err := api.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: sdk.String(cluster), Services: []string{name}})
	if err != nil {
		if gone(err) {
			return "", nil
		}
		return "", fmt.Errorf("read service %s: %w", name, err)
	}
	if len(got.Services) == 0 {
		return "", nil
	}
	return sdk.ToString(got.Services[0].Status), nil
}

// gone reports whether err is ECS saying a service, or its cluster, is
// already gone.
func gone(err error) bool {
	return IsNotFound(err) || strings.Contains(err.Error(), "ServiceNotActive")
}

// RemoveTargetGroup deletes a target group. One that's gone already is
// removed. One a listener still forwards to stays, and is an error.
func (s *Session) RemoveTargetGroup(ctx context.Context, targetGroup string) error {
	return removeTargetGroup(ctx, elb.NewFromConfig(s.cfg), targetGroup)
}

func removeTargetGroup(ctx context.Context, api targetGroupDeleter, targetGroup string) error {
	_, err := api.DeleteTargetGroup(ctx, &elb.DeleteTargetGroupInput{TargetGroupArn: sdk.String(targetGroup)})
	if err != nil && !IsNotFound(err) {
		return fmt.Errorf("delete target group %s: %w", targetGroup, err)
	}
	return nil
}

// ServiceTask is what an ECS service runs: its status, how many tasks
// it's meant to run, and its task's main container.
type ServiceTask struct {
	Status  string
	Desired int32
	Image   string
	Env     map[string]string
	// Secrets maps each secret variable to the secret it's read from.
	Secrets map[string]string
}

// Serving reports whether the service is active and meant to run tasks.
// One scaled to nothing is being removed, and nil is gone.
func (t *ServiceTask) Serving() bool {
	return t != nil && t.Status == "ACTIVE" && t.Desired > 0
}

// serviceTaskAPI is the part of ECS that reads a service and its task.
type serviceTaskAPI interface {
	DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	DescribeTaskDefinition(context.Context, *ecs.DescribeTaskDefinitionInput, ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error)
}

// ServiceTask reads what a service runs, or nil when it's missing. The
// task of a service that isn't active isn't read.
func (s *Session) ServiceTask(ctx context.Context, cluster, name string) (*ServiceTask, error) {
	return serviceTask(ctx, ecs.NewFromConfig(s.cfg), cluster, name)
}

func serviceTask(ctx context.Context, api serviceTaskAPI, cluster, name string) (*ServiceTask, error) {
	got, err := api.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: sdk.String(cluster), Services: []string{name}})
	if err != nil {
		if gone(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read service %s: %w", name, err)
	}
	if len(got.Services) == 0 {
		return nil, nil
	}
	svc := got.Services[0]
	t := &ServiceTask{Status: sdk.ToString(svc.Status), Desired: svc.DesiredCount}
	if t.Status != "ACTIVE" || svc.TaskDefinition == nil {
		return t, nil
	}
	td, err := api.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: svc.TaskDefinition})
	if err != nil {
		return nil, fmt.Errorf("read the task of service %s: %w", name, err)
	}
	for _, c := range td.TaskDefinition.ContainerDefinitions {
		if sdk.ToString(c.Name) != "main" {
			continue
		}
		t.Image, t.Env, t.Secrets = sdk.ToString(c.Image), map[string]string{}, map[string]string{}
		for _, kv := range c.Environment {
			t.Env[sdk.ToString(kv.Name)] = sdk.ToString(kv.Value)
		}
		for _, sec := range c.Secrets {
			t.Secrets[sdk.ToString(sec.Name)] = sdk.ToString(sec.ValueFrom)
		}
	}
	return t, nil
}

// WaitServiceStable polls until a service's newest deployment has
// finished rolling out, and fails if ECS gave up on it.
func (s *Session) WaitServiceStable(ctx context.Context, cluster, name string) error {
	return s.waitStable(ctx, cluster, name)
}
