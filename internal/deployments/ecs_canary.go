package deployments

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/ecs"
	"github.com/xraph/loomplane/internal/infrastructure"
)

// An ECS canary runs the candidate as a second ECS service beside the
// instance's own, the baseline, named after it with "-canary", in a
// target group of its own on the baseline's listener. The listener's
// default forward splits traffic between the two groups by weight, so
// each revision, as releasehealth reads it, is a target group's ARN. The
// candidate joins Service Connect as a client only: calls from the
// environment's other services stay on the baseline.

var _ Weighted = (*ECS)(nil)

// What an ECS canary keeps in its target's refs.
const (
	ecsRefCluster   = "cluster"
	ecsRefListener  = "listener"
	ecsRefService   = "service"
	ecsRefCandidate = "candidate_service"
)

// candidateCleanup bounds cleaning up a candidate that failed to start.
// It runs even when what failed was the call's own context, so a failed
// start leaves no candidate in the dark.
const candidateCleanup = 15 * time.Minute

// canaryService names a service's candidate.
func canaryService(service string) string { return service + "-canary" }

// canaryTargetGroup names the target group of the candidate of the
// instance called name, in AWS's 32 characters, apart from the
// baseline's.
func canaryTargetGroup(name string) string { return shortName(name + "-canary") }

// ecsCanaryCloud is what an ECS canary does in AWS. *aws.Session is one.
type ecsCanaryCloud interface {
	SetListenerWeights(ctx context.Context, listener string, weights []aws.TargetWeight) error
	EnsureTargetGroup(ctx context.Context, name, vpc string, port int32) (string, error)
	WaitServiceStable(ctx context.Context, cluster, name string) error
	WaitTargetsHealthy(ctx context.Context, targetGroup string) error
	ServiceImage(ctx context.Context, cluster, name string) (string, error)
	RemoveService(ctx context.Context, cluster, name string) error
	RemoveTargetGroup(ctx context.Context, targetGroup string) error
}

// ecsCanary is one service's canary in AWS: the listener, the baseline's
// target group and service, and the candidate's service.
type ecsCanary struct {
	cloud     ecsCanaryCloud
	cluster   string
	listener  string
	baseline  string
	service   string
	candidate string
}

// target is the canary's target with its candidate in candidateGroup.
func (c *ecsCanary) target(candidateGroup, image string) CanaryTarget {
	return CanaryTarget{Baseline: c.baseline, Candidate: candidateGroup, Image: image, Refs: map[string]string{
		ecsRefCluster: c.cluster, ecsRefListener: c.listener, ecsRefService: c.service, ecsRefCandidate: c.candidate,
	}}
}

// ecsCanaryFor reads the canary a target names.
func ecsCanaryFor(cloud ecsCanaryCloud, t CanaryTarget) (*ecsCanary, error) {
	c := &ecsCanary{
		cloud: cloud, cluster: t.Refs[ecsRefCluster], listener: t.Refs[ecsRefListener], baseline: t.Baseline,
		service: t.Refs[ecsRefService], candidate: t.Refs[ecsRefCandidate],
	}
	for what, v := range map[string]string{
		"cluster": c.cluster, "listener": c.listener, "baseline target group": c.baseline,
		"service": c.service, "candidate service": c.candidate, "candidate target group": t.Candidate,
	} {
		if v == "" {
			return nil, fmt.Errorf("the canary's ECS target names no %s", what)
		}
	}
	return c, nil
}

// alone gives the baseline all of the listener's traffic.
func (c *ecsCanary) alone() []aws.TargetWeight {
	return []aws.TargetWeight{{TargetGroup: c.baseline, Weight: 100}}
}

// split gives the candidate's group percent of the traffic and the
// baseline the rest.
func (c *ecsCanary) split(candidateGroup string, percent int) []aws.TargetWeight {
	p := int32(percent) //nolint:gosec // G115: a percentage, checked by the caller
	return []aws.TargetWeight{{TargetGroup: c.baseline, Weight: 100 - p}, {TargetGroup: candidateGroup, Weight: p}}
}

// start starts the candidate dark and returns its target group. Whatever
// an earlier attempt left goes first, then the group named group is
// attached to the listener at 0%, so it's in use before ECS registers
// the candidate in it, run starts the candidate in it, and its targets
// have to pass their health checks. A start that fails removes what it
// made, and says what may be left when it can't.
func (c *ecsCanary) start(ctx context.Context, group, vpc string, port int32, run func(context.Context, string) error) (string, error) {
	if err := c.cloud.SetListenerWeights(ctx, c.listener, c.alone()); err != nil {
		return "", fmt.Errorf("give the baseline all of the traffic: %w", err)
	}
	if err := c.cloud.RemoveService(ctx, c.cluster, c.candidate); err != nil {
		return "", fmt.Errorf("remove an earlier candidate: %w", err)
	}
	tg, err := c.cloud.EnsureTargetGroup(ctx, group, vpc, port)
	if err != nil {
		return "", err
	}
	if err = c.startIn(ctx, tg, run); err == nil {
		return tg, nil
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), candidateCleanup)
	defer cancel()
	if cerr := c.remove(cleanup, tg); cerr != nil {
		return "", fmt.Errorf("%w. Cleaning up failed too, so ECS service %s in cluster %s and target group %s may be left: %w",
			err, c.candidate, c.cluster, tg, cerr)
	}
	return "", err
}

func (c *ecsCanary) startIn(ctx context.Context, tg string, run func(context.Context, string) error) error {
	if err := c.cloud.SetListenerWeights(ctx, c.listener, c.split(tg, 0)); err != nil {
		return fmt.Errorf("attach the candidate's target group at 0%%: %w", err)
	}
	if err := run(ctx, tg); err != nil {
		return fmt.Errorf("start the candidate: %w", err)
	}
	if err := c.cloud.WaitTargetsHealthy(ctx, tg); err != nil {
		return fmt.Errorf("the candidate isn't healthy: %w", err)
	}
	return nil
}

// weigh gives the candidate percent of the traffic. At 0 it stays
// attached, unless its group is gone already, as a revert retried after
// it removed the group finds it: then the baseline gets the traffic
// alone.
func (c *ecsCanary) weigh(ctx context.Context, candidateGroup string, percent int) error {
	if percent < 0 || percent > 100 {
		return fmt.Errorf("a canary can't give its candidate %d%% of the traffic", percent)
	}
	err := c.cloud.SetListenerWeights(ctx, c.listener, c.split(candidateGroup, percent))
	if err != nil && percent == 0 && aws.IsNotFound(err) {
		err = c.cloud.SetListenerWeights(ctx, c.listener, c.alone())
	}
	if err != nil {
		return fmt.Errorf("give the candidate %d%% of the traffic: %w", percent, err)
	}
	return nil
}

// promote rolls the baseline to image while the candidate holds all of
// the traffic, waits until the baseline is stable and its targets pass
// their health checks, then gives it the traffic back and removes the
// candidate. Until the baseline passes, the candidate keeps the traffic.
// The candidate goes only after that, so a promote that finds it gone
// finds the baseline running image already, and only finishes the
// cleanup.
func (c *ecsCanary) promote(ctx context.Context, candidateGroup, image string, roll func(context.Context) error) error {
	up, err := c.cloud.ServiceImage(ctx, c.cluster, c.candidate)
	if err != nil {
		return err
	}
	if up == "" {
		runs, rerr := c.cloud.ServiceImage(ctx, c.cluster, c.service)
		if rerr != nil {
			return rerr
		}
		if runs != image {
			if werr := c.cloud.SetListenerWeights(ctx, c.listener, c.alone()); werr != nil {
				return fmt.Errorf("the candidate is gone, the baseline doesn't run %s, and the baseline couldn't get the traffic back: %w", image, werr)
			}
			return fmt.Errorf("the candidate went before the baseline ran %s, so nothing was promoted, and the baseline has all of the traffic", image)
		}
		return c.remove(ctx, candidateGroup)
	}
	if err = c.cloud.SetListenerWeights(ctx, c.listener, c.split(candidateGroup, 100)); err != nil {
		return fmt.Errorf("give the candidate all of the traffic: %w", err)
	}
	if err = roll(ctx); err != nil {
		return fmt.Errorf("roll the baseline to %s, so the candidate keeps all of the traffic: %w", image, err)
	}
	if err = c.cloud.WaitServiceStable(ctx, c.cluster, c.service); err != nil {
		return fmt.Errorf("the baseline didn't settle on %s, so the candidate keeps all of the traffic: %w", image, err)
	}
	if err = c.cloud.WaitTargetsHealthy(ctx, c.baseline); err != nil {
		return fmt.Errorf("the baseline runs %s but fails its health checks, so the candidate keeps all of the traffic: %w", image, err)
	}
	return c.remove(ctx, candidateGroup)
}

// remove gives the baseline all of the traffic, then removes the
// candidate's service and its target group. Each is safe to repeat.
func (c *ecsCanary) remove(ctx context.Context, candidateGroup string) error {
	if err := c.cloud.SetListenerWeights(ctx, c.listener, c.alone()); err != nil {
		return fmt.Errorf("give the baseline all of the traffic: %w", err)
	}
	if err := c.cloud.RemoveService(ctx, c.cluster, c.candidate); err != nil {
		return fmt.Errorf("remove the candidate: %w", err)
	}
	if err := c.cloud.RemoveTargetGroup(ctx, candidateGroup); err != nil {
		return fmt.Errorf("remove the candidate's target group: %w", err)
	}
	return nil
}

// ApplyCandidate implements Weighted. It builds the image as Apply does,
// sets up what the service runs with as Apply does, then starts the
// candidate beside the baseline with the baseline's desired count, roles
// and network, and its secrets under a path of its own, so a baseline
// task that restarts meanwhile still reads its own.
func (e *ECS) ApplyCandidate(ctx context.Context, job *Job) (*CanaryTarget, error) {
	a := job.Action
	if a.Service == nil {
		return nil, fmt.Errorf("%w: ecs runs services, not %s", ErrNoExecutor, a.Subject)
	}
	inst, err := boundInstance(infrastructure.WithPrincipal(ctx, principal(job)), e.Ctrl, job, e.provider(job))
	switch {
	case err != nil:
		return nil, err
	case inst == nil:
		return nil, fmt.Errorf("%w: %s has nothing running on ECS to ramp a canary from", ErrNoExecutor, a.Name)
	case inst.Labels[ecs.LabelTargetGroup] == "" || inst.Labels[ecs.LabelListener] == "":
		return nil, fmt.Errorf("%w: %s can't ramp a canary on ECS: it isn't behind a load balancer", ErrNoExecutor, a.Name)
	}
	s, err := e.session(job, job.Credential)
	if err != nil {
		return nil, err
	}
	ctx, image, err := e.image(ctx, s, job)
	if err != nil {
		return nil, err
	}
	job.Progress(StepDeploying, "Starting the candidate on "+image)
	p, err := e.prepare(ctx, s, job, "loomplane/"+canaryService(instanceSlug(job)))
	if err != nil {
		return nil, err
	}
	if p.inst == nil || p.inst.ID != inst.ID || p.labels[ecs.LabelTargetGroup] == "" || p.labels[ecs.LabelListener] == "" {
		return nil, fmt.Errorf("%w: %s can't ramp a canary on ECS: it isn't behind a load balancer", ErrNoExecutor, a.Name)
	}
	c := &ecsCanary{
		cloud: s, cluster: p.labels[ecs.LabelCluster], listener: p.labels[ecs.LabelListener],
		baseline: p.labels[ecs.LabelTargetGroup], service: ecs.ServiceName(inst.ID),
	}
	c.candidate = canaryService(c.service)
	port := int32(a.Service.Port) //nolint:gosec // G115: a port, validated by the manifest
	tg, err := c.start(ctx, canaryTargetGroup(strings.ToLower(p.name)), p.vpc, port, func(ctx context.Context, tg string) error {
		labels := maps.Clone(p.labels)
		labels[ecs.LabelTargetGroup] = tg
		// As many tasks as the baseline runs, so the two compare fairly.
		if d := inst.Labels[ecs.LabelDesired]; d != "" {
			labels[ecs.LabelDesired] = d
		}
		// A client of Service Connect only: the name stays the baseline's.
		delete(labels, ecs.LabelAlias)
		delete(labels, ecs.LabelListener)
		spec := p.spec
		spec.Image = image
		return ecs.Run(ctx, s, c.candidate, spec, labels)
	})
	if err != nil {
		return nil, err
	}
	t := c.target(tg, image)
	return &t, nil
}

// SetWeights implements Weighted.
func (e *ECS) SetWeights(ctx context.Context, job *Job, t CanaryTarget, candidatePercent int) error {
	s, err := e.session(job, job.Credential)
	if err != nil {
		return err
	}
	c, err := ecsCanaryFor(s, t)
	if err != nil {
		return err
	}
	return c.weigh(ctx, t.Candidate, candidatePercent)
}

// Promote implements Weighted. The baseline's own service becomes the
// release: Ctrl Plane rolls it to the candidate's image, as Apply would,
// while the candidate holds all of the traffic. Then the baseline gets
// the traffic back and the candidate goes, so the binding keeps naming
// the same instance.
func (e *ECS) Promote(ctx context.Context, job *Job, t CanaryTarget) (*Outcome, error) {
	if job.Action.Service == nil {
		return nil, fmt.Errorf("%w: ecs runs services, not %s", ErrNoExecutor, job.Action.Subject)
	}
	s, err := e.session(job, job.Credential)
	if err != nil {
		return nil, err
	}
	c, err := ecsCanaryFor(s, t)
	if err != nil {
		return nil, err
	}
	p, err := e.prepare(ctx, s, job, "loomplane/"+instanceSlug(job))
	if err != nil {
		return nil, err
	}
	if p.inst == nil || ecs.ServiceName(p.inst.ID) != c.service {
		return nil, fmt.Errorf("%s's candidate ran beside ECS service %s, which isn't what the binding names now", job.Action.Name, c.service)
	}
	if err := c.promote(ctx, t.Candidate, t.Image, func(ctx context.Context) error {
		_, derr := e.deploy(ctx, job, p, t.Image)
		return derr
	}); err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: p.inst.ID.String(), URL: p.url, Image: t.Image, Revision: t.Image}, nil
}

// Revert implements Weighted. The baseline's service never changed, so
// giving it the traffic back and removing the candidate is all there is.
func (e *ECS) Revert(ctx context.Context, job *Job, t CanaryTarget) error {
	s, err := e.session(job, job.Credential)
	if err != nil {
		return err
	}
	c, err := ecsCanaryFor(s, t)
	if err != nil {
		return err
	}
	return c.remove(ctx, t.Candidate)
}
