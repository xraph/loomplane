package deployments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/cloudrun"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/infrastructure"
)

// A Cloud Run canary runs the candidate as a revision of the instance's
// own service, named so it can be told from the baseline, the revision
// that served before. The service's traffic is split between the two
// revisions by percent, so each revision, as releasehealth reads it, is a
// revision name. Promoting sends everything to the latest revision, which
// is the candidate, so the next ordinary deploy takes all of the traffic
// again. Reverting gives the baseline everything, puts its template back
// and deletes the candidate.
//
// Revisions are immutable and every secret they read is pinned to a
// Secret Manager version, never "latest". The versions a candidate
// stores are new ones, so the baseline, and any instance it starts while
// the canary ramps, keeps reading its own.

var _ Weighted = (*CloudRun)(nil)

// What a Cloud Run canary keeps in its target's refs, beside the secret
// labels the candidate runs with, under cloudrun.LabelSecret.
const (
	runRefRegion  = "region"
	runRefService = "service"
)

// revisionNameLimit is how long Cloud Run lets a revision's name be.
const revisionNameLimit = 63

// canaryRevision names a deployment's candidate in one of its retries:
// the service's name, which Cloud Run wants in front, a hash of the
// deployment and the retry, in lower case and Cloud Run's 63 characters,
// with room for numberedRevision.
func canaryRevision(service, deploymentID string, retry int) string {
	sum := sha256.Sum256([]byte(deploymentID))
	hash := hex.EncodeToString(sum[:4])
	suffix := "-" + strconv.Itoa(retry)
	if room := revisionNameLimit - len("-999") - len(service) - 1 - len(suffix); room < len(hash) {
		hash = hash[:max(room, 1)]
	}
	return strings.ToLower(service + "-" + hash + suffix)
}

// numberedRevision is the nth name to try when an earlier attempt of the
// same retry took name.
func numberedRevision(name string, n int) string {
	if n <= 1 {
		return name
	}
	return name + "-" + strconv.Itoa(n)
}

// baselineRevision is the revision that serves most of a service's
// traffic: the canary's baseline when none was recorded.
func baselineRevision(svc *gcp.RunService) string {
	best, most := "", 0
	shares := revisionShares(svc)
	for _, t := range svc.TrafficStatuses {
		rev := trafficRevision(svc, t)
		if shares[rev] > most {
			best, most = rev, shares[rev]
		}
	}
	if best == "" {
		return svc.LatestReady()
	}
	return best
}

// trafficRevision is the revision a traffic status resolves to.
func trafficRevision(svc *gcp.RunService, t gcp.RunTraffic) string {
	if t.Type == gcp.TrafficLatest && t.Revision == "" {
		return svc.LatestReady()
	}
	return gcp.RevisionName(t.Revision)
}

// revisionShares is each revision's percent of the traffic now.
func revisionShares(svc *gcp.RunService) map[string]int {
	shares := map[string]int{}
	for _, t := range svc.TrafficStatuses {
		shares[trafficRevision(svc, t)] += t.Percent
	}
	return shares
}

// canaryBaseline is what a candidate ramps from: the baseline the canary
// recorded, as a retry ramps from, while it still takes traffic, or else
// the revision that serves most. A retry after a revert that didn't
// finish can find the earlier candidate serving most.
func canaryBaseline(job *Job, svc *gcp.RunService) string {
	if job.Deployment != nil {
		if recorded := job.Deployment.Canary.service(job.Action.ID); recorded != nil && recorded.Baseline != "" && revisionShares(svc)[recorded.Baseline] > 0 {
			return recorded.Baseline
		}
	}
	return baselineRevision(svc)
}

// cloudRunCanaryCloud is what a Cloud Run canary does in Google Cloud.
// *gcp.Session is one.
type cloudRunCanaryCloud interface {
	GetService(ctx context.Context, region, name string) (*gcp.RunService, error)
	GetRevision(ctx context.Context, region, service, revision string) (*gcp.RunRevision, error)
	ApplyCandidateRevision(ctx context.Context, region, name string, svc gcp.RunService) (*gcp.RunService, error)
	ReplaceService(ctx context.Context, region, name string, svc gcp.RunService) (*gcp.RunService, error)
	SetTraffic(ctx context.Context, region, name string, traffic []gcp.RunTraffic) error
	DeleteRevision(ctx context.Context, region, service, revision string) error
}

// cloudRunCanary is one service's canary on Cloud Run: the service and
// its baseline and candidate revisions.
type cloudRunCanary struct {
	cloud     cloudRunCanaryCloud
	region    string
	service   string
	baseline  string
	candidate string
}

// baselineTemplate is the service the baseline's revision was made from,
// to put back as the service's template once the candidate goes.
type baselineTemplate func(context.Context) (*gcp.RunService, error)

// cloudRunCanaryFor reads the canary a target names.
func cloudRunCanaryFor(cloud cloudRunCanaryCloud, t CanaryTarget) (*cloudRunCanary, error) {
	c := &cloudRunCanary{cloud: cloud, region: t.Refs[runRefRegion], service: t.Refs[runRefService], baseline: t.Baseline, candidate: t.Candidate}
	for what, v := range map[string]string{"region": c.region, "service": c.service, "baseline revision": c.baseline, "candidate revision": c.candidate} {
		if v == "" {
			return nil, fmt.Errorf("the canary's Cloud Run target names no %s", what)
		}
	}
	return c, nil
}

// target is the canary's target: its revisions, the image the candidate
// runs and the secret versions it reads.
func (c *cloudRunCanary) target(image string, secrets map[string]string) CanaryTarget {
	refs := map[string]string{runRefRegion: c.region, runRefService: c.service}
	maps.Copy(refs, secrets)
	return CanaryTarget{Baseline: c.baseline, Candidate: c.candidate, Image: image, Refs: refs}
}

// candidateSecrets are the secret labels a target's candidate runs with.
func candidateSecrets(refs map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range refs {
		if strings.HasPrefix(k, cloudrun.LabelSecret) {
			out[k] = v
		}
	}
	return out
}

// alone gives the baseline all of the traffic.
func (c *cloudRunCanary) alone() []gcp.RunTraffic {
	return []gcp.RunTraffic{{Type: gcp.TrafficRevision, Revision: c.baseline, Percent: 100}}
}

// split gives the candidate percent of the traffic and the baseline the
// rest. A revision with none isn't listed.
func (c *cloudRunCanary) split(percent int) []gcp.RunTraffic {
	switch percent {
	case 0:
		return c.alone()
	case 100:
		return []gcp.RunTraffic{{Type: gcp.TrafficRevision, Revision: c.candidate, Percent: 100}}
	}
	return []gcp.RunTraffic{
		{Type: gcp.TrafficRevision, Revision: c.baseline, Percent: 100 - percent},
		{Type: gcp.TrafficRevision, Revision: c.candidate, Percent: percent},
	}
}

// revision reads one of the service's revisions, or nil when it's gone.
func (c *cloudRunCanary) revision(ctx context.Context, name string) (*gcp.RunRevision, error) {
	rev, err := c.cloud.GetRevision(ctx, c.region, c.service, name)
	if gcp.IsNotFound(err) {
		return nil, nil
	}
	return rev, err
}

// freeName is the first name from base that no revision of the service
// has. An attempt that stopped after it started a candidate leaves that
// name taken, and Cloud Run won't make a different revision under it.
func (c *cloudRunCanary) freeName(ctx context.Context, base string) (string, error) {
	const tries = 20
	for n := 1; n <= tries; n++ {
		name := numberedRevision(base, n)
		_, err := c.cloud.GetRevision(ctx, c.region, c.service, name)
		if gcp.IsNotFound(err) {
			return name, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("every revision name from %s to %s is taken", base, numberedRevision(base, tries))
}

// earlier are candidates an earlier attempt left: a revision the template
// names, which only a canary does, and any revision but the baseline that
// takes traffic, which only a canary splits. Neither is the baseline or
// this candidate.
func (c *cloudRunCanary) earlier(svc *gcp.RunService) []string {
	var out []string
	add := func(rev string) {
		if rev != "" && rev != c.baseline && rev != c.candidate && !slices.Contains(out, rev) {
			out = append(out, rev)
		}
	}
	add(svc.Template.Revision)
	for _, t := range svc.TrafficStatuses {
		if t.Percent > 0 {
			add(trafficRevision(svc, t))
		}
	}
	return out
}

// start starts the candidate dark: candidate's template, as the
// candidate's revision, with the baseline pinned to all of the traffic,
// and waits until it's ready. Then whatever an earlier attempt left goes.
// A start that fails removes what it made, and says what may be left when
// it can't.
func (c *cloudRunCanary) start(ctx context.Context, candidate gcp.RunService, restore baselineTemplate) error {
	svc, err := c.cloud.GetService(ctx, c.region, c.service)
	if err != nil {
		return err
	}
	earlier := c.earlier(svc)
	candidate.Template.Revision = c.candidate
	candidate.Traffic = c.alone()
	if err = c.startIn(ctx, candidate, earlier); err == nil {
		return nil
	}
	// Detached, so a start that failed because its context ended still
	// cleans up. When that was the action's deadline, a retry may start
	// while this runs. Each step is safe to repeat and the two converge:
	// the retry's candidate has a name of its own, and this removes only
	// this one.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), candidateCleanup)
	defer cancel()
	if cerr := c.remove(cleanup, restore); cerr != nil {
		return fmt.Errorf("%w. Cleaning up failed too, so revision %s of Cloud Run service %s may be left, and the service may still name it as its template: %w",
			err, c.candidate, c.service, cerr)
	}
	return err
}

func (c *cloudRunCanary) startIn(ctx context.Context, candidate gcp.RunService, earlier []string) error {
	if _, err := c.cloud.ApplyCandidateRevision(ctx, c.region, c.service, candidate); err != nil {
		return fmt.Errorf("start the candidate: %w", err)
	}
	for _, rev := range earlier {
		if err := c.cloud.DeleteRevision(ctx, c.region, c.service, rev); err != nil {
			return fmt.Errorf("delete revision %s, an earlier candidate: %w", rev, err)
		}
	}
	return nil
}

// weigh gives the candidate percent of the traffic. It gives a revision
// none unless it's up, and at 0 the candidate needn't be there at all, as
// a revert retried after it deleted the candidate finds it.
func (c *cloudRunCanary) weigh(ctx context.Context, percent int) error {
	if percent < 0 || percent > 100 {
		return fmt.Errorf("a canary can't give its candidate %d%% of the traffic", percent)
	}
	if percent > 0 {
		rev, err := c.revision(ctx, c.candidate)
		if err != nil {
			return err
		}
		if !rev.Up() {
			return fmt.Errorf("candidate revision %s can't take traffic: %s", c.candidate, rev.Why())
		}
	}
	if err := c.cloud.SetTraffic(ctx, c.region, c.service, c.split(percent)); err != nil {
		return fmt.Errorf("give the candidate %d%% of the traffic: %w", percent, err)
	}
	return nil
}

// runRelease is what a promote makes the service run: the candidate's
// image, its plain variables and the names of its secret ones.
type runRelease struct {
	image   string
	env     map[string]string
	secrets []string
}

// runsOn reports whether a revision runs the release.
func (r runRelease) runsOn(rev *gcp.RunRevision) bool {
	if rev == nil || len(rev.Containers) != 1 || rev.Containers[0].Image != r.image {
		return false
	}
	env, secrets := map[string]string{}, []string{}
	for _, e := range rev.Containers[0].Env {
		if e.ValueSource != nil {
			secrets = append(secrets, e.Name)
		} else {
			env[e.Name] = e.Value
		}
	}
	slices.Sort(secrets)
	return maps.Equal(env, r.env) && slices.Equal(secrets, r.secrets)
}

// promote sends all of the traffic to the service's latest revision, the
// candidate, then records the release. The candidate only takes all of
// the traffic while it's up and it's the latest, so the next ordinary
// deploy, which makes a newer one, takes it all again.
//
// A candidate that isn't up means an earlier promote got as far as it
// could: when what serves all of the traffic is up and runs the release,
// only the record is left. Otherwise the baseline gets the traffic back
// and nothing is promoted.
func (c *cloudRunCanary) promote(ctx context.Context, rel runRelease, record func(context.Context) error) error {
	candidate, err := c.revision(ctx, c.candidate)
	if err != nil {
		return err
	}
	svc, err := c.cloud.GetService(ctx, c.region, c.service)
	if err != nil {
		return err
	}
	if !candidate.Up() {
		serving := baselineRevision(svc)
		if revisionShares(svc)[serving] == 100 {
			rev, rerr := c.revision(ctx, serving)
			if rerr != nil {
				return rerr
			}
			if rev.Up() && rel.runsOn(rev) {
				return record(ctx)
			}
		}
		return c.giveBack(ctx, fmt.Errorf("candidate revision %s can't take all of the traffic (%s), and nothing that serves runs the release, so nothing was promoted", c.candidate, candidate.Why()))
	}
	if latest := svc.LatestReady(); latest != c.candidate {
		return c.giveBack(ctx, fmt.Errorf("the service's latest revision is %s, not candidate %s, so nothing was promoted", latest, c.candidate))
	}
	if err = c.cloud.SetTraffic(ctx, c.region, c.service, []gcp.RunTraffic{{Type: gcp.TrafficLatest, Percent: 100}}); err != nil {
		return fmt.Errorf("give the candidate all of the traffic: %w", err)
	}
	return record(ctx)
}

// giveBack gives the baseline all of the traffic after err, and says
// whether it got it.
func (c *cloudRunCanary) giveBack(ctx context.Context, err error) error {
	if werr := c.cloud.SetTraffic(ctx, c.region, c.service, c.alone()); werr != nil {
		return fmt.Errorf("%w, and the baseline couldn't get all of the traffic back: %w", err, werr)
	}
	return fmt.Errorf("%w. The baseline has all of the traffic", err)
}

// remove gives the baseline all of the traffic, puts the baseline's
// template back while the template still makes the candidate, then
// deletes the candidate. Cloud Run makes the restored template a revision
// of its own, which takes no traffic. Each step is safe to repeat.
func (c *cloudRunCanary) remove(ctx context.Context, restore baselineTemplate) error {
	if err := c.cloud.SetTraffic(ctx, c.region, c.service, c.alone()); err != nil {
		return fmt.Errorf("give the baseline all of the traffic: %w", err)
	}
	svc, err := c.cloud.GetService(ctx, c.region, c.service)
	if err != nil {
		return err
	}
	if svc.Template.Revision == c.candidate || svc.LatestCreated() == c.candidate {
		base, rerr := restore(ctx)
		if rerr != nil {
			return fmt.Errorf("put the baseline's template back: %w", rerr)
		}
		back := *base
		back.Template.Revision, back.Traffic = "", c.alone()
		if _, rerr = c.cloud.ReplaceService(ctx, c.region, c.service, back); rerr != nil {
			return fmt.Errorf("put the baseline's template back: %w", rerr)
		}
	}
	if err := c.cloud.DeleteRevision(ctx, c.region, c.service, c.candidate); err != nil {
		return fmt.Errorf("delete the candidate revision: %w", err)
	}
	return nil
}

// instanceTemplate is the service a Ctrl Plane instance's record makes:
// what its last deploy rolled out.
func instanceTemplate(inst *instance.Instance) (*gcp.RunService, error) {
	if len(inst.Services) != 1 {
		return nil, fmt.Errorf("instance %s has %d services; cloud run runs one", inst.ID, len(inst.Services))
	}
	svc, err := cloudrun.Service(inst.Services[0], inst.Labels)
	if err != nil {
		return nil, err
	}
	return &svc, nil
}

// ApplyCandidate implements Weighted. It checks the service account may
// read and delete revisions, builds the image as Apply does, stores the
// secrets as Apply does, as new versions only the candidate reads, then
// starts the candidate as a revision beside the baseline.
func (c *CloudRun) ApplyCandidate(ctx context.Context, job *Job) (*CanaryTarget, error) {
	a := job.Action
	if a.Service == nil {
		return nil, fmt.Errorf("%w: cloud run runs services, not %s", ErrNoExecutor, a.Subject)
	}
	key := c.provider(job)
	pctx := infrastructure.WithPrincipal(ctx, principal(job))
	inst, err := boundInstance(pctx, c.Ctrl, job, key)
	switch {
	case err != nil:
		return nil, err
	case inst == nil:
		return nil, fmt.Errorf("%w: %s has nothing running on Cloud Run to ramp a canary from", ErrNoExecutor, a.Name)
	}
	if current, _, cerr := currentInstance(pctx, c.Ctrl, job, key); cerr != nil {
		return nil, cerr
	} else if current == nil || current.ID != inst.ID {
		return nil, fmt.Errorf("%s can't ramp a canary on Cloud Run: the binding names Ctrl Plane instance %s, but this release would deploy another", a.Name, inst.ID)
	}
	restore, err := instanceTemplate(inst)
	if err != nil {
		return nil, err
	}
	s, err := c.session(ctx, job)
	if err != nil {
		return nil, err
	}
	if perr := c.canaryPermissions(ctx, s, job); perr != nil {
		return nil, perr
	}
	cr := &cloudRunCanary{cloud: s, region: a.Region, service: cloudrun.ServiceName(inst.ID)}
	svc, err := s.GetService(ctx, cr.region, cr.service)
	if gcp.IsNotFound(err) {
		return nil, fmt.Errorf("%w: %s has no Cloud Run service %s to ramp a canary from", ErrNoExecutor, a.Name, cr.service)
	}
	if err != nil {
		return nil, err
	}
	if cr.baseline = canaryBaseline(job, svc); cr.baseline == "" {
		return nil, fmt.Errorf("cloud run service %s serves no revision to ramp a canary from", cr.service)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	ctx, built, err := c.image(ctx, cancel, s, job, nil)
	if err != nil {
		return nil, err
	}
	image := built.image
	job.Progress(StepDeploying, "Starting the candidate on "+image)
	secrets, err := c.storeSecrets(ctx, s, job)
	if err != nil {
		return nil, err
	}
	sp, labels := spec(job, image, secrets)
	candidate, err := cloudrun.Service(sp, labels)
	if err != nil {
		return nil, err
	}
	if cr.candidate, err = cr.freeName(ctx, canaryRevision(cr.service, job.Deployment.ID, job.Deployment.Retries)); err != nil {
		return nil, err
	}
	if startErr := cr.start(ctx, candidate, func(context.Context) (*gcp.RunService, error) { return restore, nil }); startErr != nil {
		return nil, startErr
	}
	t := cr.target(image, secrets)
	return &t, nil
}

// canaryPermissions refuses a canary whose service account may not read
// or delete revisions. It asks Google now, since the connection's last
// check may be from before canaries, and an account without them is still
// a complete connection.
func (c *CloudRun) canaryPermissions(ctx context.Context, s *gcp.Session, job *Job) error {
	missing, err := s.MissingOf(ctx, gcp.CanaryPermissions)
	if err != nil {
		return fmt.Errorf("check that the service account may split traffic: %w", err)
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%s can't run a canary: the service account of %s isn't allowed %s. Grant them and check the connection again under Integrations, or release this environment direct or blue_green, which don't need them",
		job.Action.Name, job.Connection.Name, strings.Join(missing, ", "))
}

// SetWeights implements Weighted.
func (c *CloudRun) SetWeights(ctx context.Context, job *Job, t CanaryTarget, candidatePercent int) error {
	s, err := c.session(ctx, job)
	if err != nil {
		return err
	}
	cr, err := cloudRunCanaryFor(s, t)
	if err != nil {
		return err
	}
	return cr.weigh(ctx, candidatePercent)
}

// Promote implements Weighted. The candidate becomes what the service
// runs, with all of the traffic, and Ctrl Plane's record of the instance
// becomes the release, without deploying it again, so the binding keeps
// naming the same instance and the next deploy starts from the release.
func (c *CloudRun) Promote(ctx context.Context, job *Job, t CanaryTarget) (*Outcome, error) {
	if job.Action.Service == nil {
		return nil, fmt.Errorf("%w: cloud run runs services, not %s", ErrNoExecutor, job.Action.Subject)
	}
	s, err := c.session(ctx, job)
	if err != nil {
		return nil, err
	}
	cr, err := cloudRunCanaryFor(s, t)
	if err != nil {
		return nil, err
	}
	pctx := infrastructure.WithPrincipal(ctx, principal(job))
	inst, err := boundInstance(pctx, c.Ctrl, job, c.provider(job))
	if err != nil {
		return nil, err
	}
	if inst == nil || cloudrun.ServiceName(inst.ID) != cr.service {
		return nil, fmt.Errorf("%s's candidate ran in Cloud Run service %s, which isn't what the binding names now", job.Action.Name, cr.service)
	}
	secrets := candidateSecrets(t.Refs)
	sp, labels := spec(job, t.Image, secrets)
	rel := runRelease{image: t.Image, env: sp.Env, secrets: slices.Sorted(maps.Keys(secretNames(secrets)))}
	if err := cr.promote(ctx, rel, func(ctx context.Context) error {
		if ierr := cloudrun.SetInvokers(ctx, s, cr.region, cr.service, labels); ierr != nil {
			return ierr
		}
		_, uerr := c.Ctrl.Instances.Update(infrastructure.WithPrincipal(ctx, principal(job)), inst.ID, instance.UpdateRequest{
			Services: []cpprovider.ServiceSpec{sp}, Labels: labels,
		})
		if uerr != nil {
			return fmt.Errorf("record the release in Ctrl Plane: %w", uerr)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	url := endpoint(inst.Endpoints)
	if url == "" && job.Binding != nil {
		url = job.Binding.URL
	}
	return &Outcome{ExternalID: inst.ID.String(), URL: url, Image: t.Image, Revision: t.Image}, nil
}

// secretNames are the variables secret labels are for.
func secretNames(labels map[string]string) map[string]bool {
	out := map[string]bool{}
	for k := range labels {
		out[strings.TrimPrefix(k, cloudrun.LabelSecret)] = true
	}
	return out
}

// Revert implements Weighted. The baseline gets all of the traffic back,
// the service's template goes back to what Ctrl Plane records the
// instance ran, which is the baseline, and the candidate is deleted, so
// the template, the traffic and the binding agree again.
func (c *CloudRun) Revert(ctx context.Context, job *Job, t CanaryTarget) error {
	s, err := c.session(ctx, job)
	if err != nil {
		return err
	}
	cr, err := cloudRunCanaryFor(s, t)
	if err != nil {
		return err
	}
	return cr.remove(ctx, func(ctx context.Context) (*gcp.RunService, error) {
		inst, ierr := boundInstance(infrastructure.WithPrincipal(ctx, principal(job)), c.Ctrl, job, c.provider(job))
		switch {
		case ierr != nil:
			return nil, ierr
		case inst == nil || cloudrun.ServiceName(inst.ID) != cr.service:
			return nil, errors.New("the binding no longer names the instance the candidate ran beside, so Loomplane can't tell what the baseline ran")
		}
		return instanceTemplate(inst)
	})
}
