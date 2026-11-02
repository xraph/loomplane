package deployments

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
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
	ServiceTask(ctx context.Context, cluster, name string) (*aws.ServiceTask, error)
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
	// Detached, so a start that failed because its context ended still
	// cleans up. When that was the action's deadline, a retry may start
	// while this runs. Each step is safe to repeat and the two converge,
	// though the retry can lose its fresh candidate here and start again.
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

// ecsRelease is what a promote rolls the baseline to: the candidate's
// image, its plain variables and the names of its secret ones. Secret
// values aren't in a task, so a release that changes only those looks
// like the one before it.
type ecsRelease struct {
	image   string
	env     map[string]string
	secrets []string
}

// runsOn reports whether a service's task is the release's.
func (r ecsRelease) runsOn(t *aws.ServiceTask) bool {
	if t == nil || t.Status != "ACTIVE" || t.Image != r.image || !maps.Equal(t.Env, r.env) {
		return false
	}
	return slices.Equal(slices.Sorted(maps.Keys(t.Secrets)), r.secrets)
}

// promote rolls the baseline to the release while the candidate holds
// all of the traffic, waits until the baseline is stable and its targets
// pass their health checks, then gives it the traffic back and removes
// the candidate. Until the baseline passes, the candidate keeps the
// traffic.
//
// The candidate only takes all of the traffic while it's serving and its
// own targets pass. An earlier promote's cleanup scales it to nothing
// before it deletes it, and removes it only once the baseline runs the
// release, so a candidate that isn't serving means the baseline should
// run the release already: then the cleanup is finished, and otherwise
// the baseline gets the traffic back and nothing is promoted. A
// candidate that's serving is used even when the baseline looks rolled,
// since a release that changes only secret values looks the same.
func (c *ecsCanary) promote(ctx context.Context, candidateGroup string, rel ecsRelease, roll func(context.Context) error) error {
	candidate, err := c.cloud.ServiceTask(ctx, c.cluster, c.candidate)
	if err != nil {
		return err
	}
	if !candidate.Serving() {
		baseline, berr := c.cloud.ServiceTask(ctx, c.cluster, c.service)
		if berr != nil {
			return berr
		}
		if !rel.runsOn(baseline) {
			return c.giveBack(ctx, errors.New("the candidate isn't running and the baseline doesn't run the release, so nothing was promoted"))
		}
		if rerr := c.baselineReady(ctx); rerr != nil {
			return c.giveBack(ctx, fmt.Errorf("the baseline runs the release but isn't ready, and the candidate isn't running to hold the traffic: %w", rerr))
		}
		return c.remove(ctx, candidateGroup)
	}
	if err = c.cloud.WaitTargetsHealthy(ctx, candidateGroup); err != nil {
		return c.giveBack(ctx, fmt.Errorf("the candidate fails its health checks, so it can't take all of the traffic and nothing was promoted: %w", err))
	}
	if err = c.cloud.SetListenerWeights(ctx, c.listener, c.split(candidateGroup, 100)); err != nil {
		return fmt.Errorf("give the candidate all of the traffic: %w", err)
	}
	if err = roll(ctx); err != nil {
		return fmt.Errorf("roll the baseline to %s, so the candidate keeps all of the traffic: %w", rel.image, err)
	}
	if err = c.baselineReady(ctx); err != nil {
		return fmt.Errorf("the baseline runs %s but isn't ready, so the candidate keeps all of the traffic: %w", rel.image, err)
	}
	return c.remove(ctx, candidateGroup)
}

// baselineReady waits until the baseline is stable and its targets pass
// their health checks.
func (c *ecsCanary) baselineReady(ctx context.Context) error {
	if err := c.cloud.WaitServiceStable(ctx, c.cluster, c.service); err != nil {
		return err
	}
	return c.cloud.WaitTargetsHealthy(ctx, c.baseline)
}

// giveBack gives the baseline all of the traffic after err, and says
// whether it got it.
func (c *ecsCanary) giveBack(ctx context.Context, err error) error {
	if werr := c.cloud.SetListenerWeights(ctx, c.listener, c.alone()); werr != nil {
		return fmt.Errorf("%w, and the baseline couldn't get all of the traffic back: %w", err, werr)
	}
	return fmt.Errorf("%w. The baseline has all of the traffic", err)
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

// candidateLabels are what the candidate runs with: the release's
// labels, with the candidate's target group, as many tasks as the
// baseline runs so the two compare fairly, no Service Connect name of its
// own (it's a client only, so calls from other services stay on the
// baseline), and no listener.
func candidateLabels(release, baseline map[string]string, targetGroup string) map[string]string {
	labels := maps.Clone(release)
	labels[ecs.LabelTargetGroup] = targetGroup
	if d := baseline[ecs.LabelDesired]; d != "" {
		labels[ecs.LabelDesired] = d
	}
	delete(labels, ecs.LabelAlias)
	delete(labels, ecs.LabelListener)
	return labels
}

// ApplyCandidate implements Weighted. It checks the key may split the
// listener's traffic, builds the image as Apply does, sets up what the
// service runs with as Apply does, then starts the candidate beside the
// baseline, with its secrets under a path of its own, so a baseline task
// that restarts meanwhile still reads its own.
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
	if perr := e.canaryPermissions(ctx, job); perr != nil {
		return nil, perr
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
	p, err := e.prepare(ctx, s, job)
	if err != nil {
		return nil, err
	}
	switch {
	case p.inst == nil || p.inst.ID != inst.ID:
		return nil, fmt.Errorf("%s can't ramp a canary on ECS: the binding names Ctrl Plane instance %s, but this release would deploy another", a.Name, inst.ID)
	case p.labels[ecs.LabelTargetGroup] == "" || p.labels[ecs.LabelListener] == "":
		return nil, fmt.Errorf("%w: %s can't ramp a canary on ECS: it isn't behind a load balancer", ErrNoExecutor, a.Name)
	}
	c := &ecsCanary{
		cloud: s, cluster: p.labels[ecs.LabelCluster], listener: p.labels[ecs.LabelListener],
		baseline: p.labels[ecs.LabelTargetGroup], service: ecs.ServiceName(inst.ID),
	}
	c.candidate = canaryService(c.service)
	port := int32(a.Service.Port) //nolint:gosec // G115: a port, validated by the manifest
	tg, err := c.start(ctx, canaryTargetGroup(strings.ToLower(p.name)), p.vpc, port, func(ctx context.Context, tg string) error {
		labels := candidateLabels(p.labels, inst.Labels, tg)
		if serr := putSecrets(ctx, s, job, "loomplane/"+canaryService(instanceSlug(job)), labels); serr != nil {
			return serr
		}
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

// canaryPermissions refuses a canary whose key lacks what splitting a
// listener needs. It asks IAM's policy simulator now, since the
// connection's last check may be from before canaries, and a key without
// those actions is still a complete connection. A key that may not run
// the simulator goes ahead, as its connection does.
func (e *ECS) canaryPermissions(ctx context.Context, job *Job) error {
	key, err := aws.ParseKey(job.Credential)
	if err != nil {
		return err
	}
	region := job.Connection.Details["region"]
	who := job.Connection.Details["principal"]
	if who == "" {
		id, ierr := e.AWS.Identify(ctx, key, region)
		if ierr != nil {
			return ierr
		}
		who = id.ARN
	}
	missing, checked, err := e.AWS.MissingOf(ctx, key, region, aws.PrincipalARN(who), aws.CanaryActions)
	if err != nil {
		return fmt.Errorf("check that the key may split traffic: %w", err)
	}
	if !checked || len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%s can't run a canary: the key of %s isn't allowed %s. Grant them and check the connection again under Integrations, or release this environment direct or blue_green, which don't need them",
		job.Action.Name, job.Connection.Name, strings.Join(missing, ", "))
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
	p, err := e.prepare(ctx, s, job)
	if err != nil {
		return nil, err
	}
	if p.inst == nil || ecs.ServiceName(p.inst.ID) != c.service {
		return nil, fmt.Errorf("%s's candidate ran beside ECS service %s, which isn't what the binding names now", job.Action.Name, c.service)
	}
	rel := ecsRelease{image: t.Image, env: p.spec.Env, secrets: sortedKeys(job.Secrets)}
	// The release's secret values go in only once the candidate holds the
	// traffic, so a baseline task that restarts before then reads its own.
	if err := c.promote(ctx, t.Candidate, rel, func(ctx context.Context) error {
		if serr := putSecrets(ctx, s, job, "loomplane/"+instanceSlug(job), p.labels); serr != nil {
			return serr
		}
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
