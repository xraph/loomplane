package deployments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/azure"
)

// An Azure canary runs the candidate as a second revision of the app's own
// container app, beside the baseline, the revision that served before.
// The app goes into multiple revisions mode, with its traffic split
// between the two revisions by percent, so each revision, as
// releasehealth reads it, is a revision name. Promoting goes back to
// single revision mode with all of the traffic on the latest revision,
// the candidate, and Container Apps deactivates the baseline. Reverting
// gives the baseline everything, puts its template back on the app and
// deactivates the candidate.
//
// Container Apps keeps secrets on the app, not on a revision, and a
// revision reads a secret's value when a replica starts. Changing a
// secret the baseline reads would hand the release's value to any
// replica the baseline starts mid-canary. So the candidate reads secrets
// of its own, named after its revision suffix, and the baseline's keep
// their values: the candidate's PUT sends them back as ListSecrets read
// them.

var _ Weighted = (*Azure)(nil)

// azureRefApp is the container app's resource ID in a canary target's
// refs.
const azureRefApp = "app"

// azureCandidateRE matches the revision suffix of a canary's candidate.
var azureCandidateRE = regexp.MustCompile(`^c[0-9a-f]{8}-\d+(-\d+)?$`)

// azureSuffix is a revision suffix for a deployment's retry: a letter
// that says what it's for, a hash of the deployment and the retry. It's
// lower case letters, digits and single hyphens, as Container Apps wants,
// and short enough for any app name and numberedRevision.
func azureSuffix(kind, deploymentID string, retry int) string {
	sum := sha256.Sum256([]byte(deploymentID))
	return kind + hex.EncodeToString(sum[:4]) + "-" + strconv.Itoa(retry)
}

// azureCandidateSuffix is the revision suffix of a deployment's candidate
// in one of its retries.
func azureCandidateSuffix(deploymentID string, retry int) string {
	return azureSuffix("c", deploymentID, retry)
}

// azureRestoreSuffix is the revision suffix the baseline's template takes
// when a revert puts it back.
func azureRestoreSuffix(deploymentID string, retry int) string {
	return azureSuffix("r", deploymentID, retry)
}

// azureOwnCandidate reports whether a revision suffix is a candidate this
// deployment started, in any of its retries.
func azureOwnCandidate(job *Job, suffix string) bool {
	for retry := 0; retry <= job.Deployment.Retries; retry++ {
		base := azureCandidateSuffix(job.Deployment.ID, retry)
		if suffix == base || strings.HasPrefix(suffix, base+"-") {
			return true
		}
	}
	return false
}

// azureShares is each revision's percent of an app's traffic now, by
// lower-cased name: the latest ready revision's in single revision mode,
// else as its traffic says.
func azureShares(app *azure.App) map[string]int {
	out := map[string]int{}
	p := app.Properties
	latest := strings.ToLower(p.LatestReadyRevision)
	var traffic []azure.TrafficWeight
	if p.Configuration.Ingress != nil {
		traffic = p.Configuration.Ingress.Traffic
	}
	if !strings.EqualFold(p.Configuration.ActiveRevisionsMode, azure.MultipleRevisions) || len(traffic) == 0 {
		if latest != "" {
			out[latest] = 100
		}
		return out
	}
	for _, t := range traffic {
		rev := strings.ToLower(t.RevisionName)
		if t.LatestRevision || rev == "" {
			rev = latest
		}
		if t.Weight > 0 {
			out[rev] += t.Weight
		}
	}
	return out
}

// azureServing is the one revision that serves all of an app's traffic,
// or "" when none does alone.
func azureServing(app *azure.App) string {
	for rev, share := range azureShares(app) {
		if share == 100 {
			return rev
		}
	}
	return ""
}

// azureLatestOnly reports whether all of an app's traffic follows its
// latest revision, as after a promote or an ordinary deploy.
func azureLatestOnly(app *azure.App) bool {
	conf := app.Properties.Configuration
	if !strings.EqualFold(conf.ActiveRevisionsMode, azure.MultipleRevisions) || conf.Ingress == nil || len(conf.Ingress.Traffic) == 0 {
		return true
	}
	return len(conf.Ingress.Traffic) == 1 && conf.Ingress.Traffic[0].LatestRevision && conf.Ingress.Traffic[0].Weight == 100
}

// azureSettled refuses an app whose traffic another canary left behind:
// split between revisions, or pinned while the template still makes a
// candidate that isn't this deployment's. Either way the revision with
// most of the traffic may be a candidate that failed, and ramping from it
// would give it everything and deactivate the real baseline.
func azureSettled(job *Job, name string, app *azure.App) error {
	shares := azureShares(app)
	serving := make([]string, 0, len(shares))
	for _, rev := range slices.Sorted(maps.Keys(shares)) {
		serving = append(serving, fmt.Sprintf("%s %d%%", rev, shares[rev]))
	}
	settle := "Settle that canary's traffic first: retry or roll back its deployment, or release this environment direct once, which gives the latest revision all of the traffic"
	if len(serving) > 1 {
		return fmt.Errorf("%s can't start a canary: an earlier canary left container app %s's traffic split (%s). %s",
			job.Action.Name, name, strings.Join(serving, ", "), settle)
	}
	if suffix := app.Properties.Template.RevisionSuffix; azureCandidateRE.MatchString(suffix) && !azureLatestOnly(app) && !azureOwnCandidate(job, suffix) {
		return fmt.Errorf("%s can't start a canary: container app %s still makes revision %s, an earlier canary's candidate, while its traffic is pinned (%s). %s",
			job.Action.Name, name, name+"--"+suffix, strings.Join(serving, ", "), settle)
	}
	return nil
}

// azureCanary is one service's canary on Azure Container Apps: the app
// and its baseline and candidate revisions.
type azureCanary struct {
	s *azure.Session
	// app is the container app's resource ID, and name its name.
	app, name string
	baseline  string
	candidate string
	// note shows what the canary couldn't tidy, when set.
	note func(string)
}

// azureCanaryFor reads the canary a target names.
func azureCanaryFor(s *azure.Session, t CanaryTarget, note func(string)) (*azureCanary, error) {
	c := &azureCanary{s: s, app: t.Refs[azureRefApp], baseline: t.Baseline, candidate: t.Candidate, note: note}
	for what, v := range map[string]string{"container app": c.app, "baseline revision": c.baseline, "candidate revision": c.candidate} {
		if v == "" {
			return nil, fmt.Errorf("the canary's Azure target names no %s", what)
		}
	}
	if !s.Owns(c.app) {
		return nil, fmt.Errorf("%s isn't a container app in the connection's subscription", c.app)
	}
	c.name = path.Base(c.app)
	if !strings.HasPrefix(c.candidate, c.name+"--") {
		return nil, fmt.Errorf("candidate revision %s isn't a revision of container app %s", c.candidate, c.name)
	}
	return c, nil
}

// suffix is the candidate's revision suffix, which also names its
// secrets.
func (c *azureCanary) suffix() string { return strings.TrimPrefix(c.candidate, c.name+"--") }

// alone gives the baseline all of the traffic.
func (c *azureCanary) alone() []azure.TrafficWeight {
	return []azure.TrafficWeight{{RevisionName: c.baseline, Weight: 100}}
}

// split gives the candidate percent of the traffic and the baseline the
// rest. A revision with none isn't listed.
func (c *azureCanary) split(percent int) []azure.TrafficWeight {
	switch percent {
	case 0:
		return c.alone()
	case 100:
		return []azure.TrafficWeight{{RevisionName: c.candidate, Weight: 100}}
	}
	return []azure.TrafficWeight{{RevisionName: c.baseline, Weight: 100 - percent}, {RevisionName: c.candidate, Weight: percent}}
}

// revision reads one of the app's revisions, or nil when it's gone.
func (c *azureCanary) revision(ctx context.Context, name string) (*azure.Revision, error) {
	rev, err := c.s.GetRevision(ctx, c.app, name)
	if azure.IsNotFound(err) {
		return nil, nil
	}
	return rev, err
}

// freeSuffix is the first revision suffix from base that no revision of
// the app has. An attempt that stopped after it made its revision leaves
// that suffix taken, and Container Apps won't make another revision under
// it.
func (c *azureCanary) freeSuffix(ctx context.Context, base string) (string, error) {
	const tries = 20
	for n := 1; n <= tries; n++ {
		suffix := numberedRevision(base, n)
		_, err := c.s.GetRevision(ctx, c.app, c.name+"--"+suffix)
		if azure.IsNotFound(err) {
			return suffix, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("every revision suffix from %s to %s is taken", base, numberedRevision(base, tries))
}

// makesCandidate reports whether the app's template still makes the
// candidate.
func (c *azureCanary) makesCandidate(app *azure.App) bool {
	return app.Properties.Template.RevisionSuffix == c.suffix() || strings.EqualFold(app.Properties.LatestRevisionName, c.candidate)
}

// others are the app's active revisions but the baseline and the
// candidate: an earlier attempt's candidate, or the revision a revert's
// restored template made.
func (c *azureCanary) others(ctx context.Context) ([]string, error) {
	active, err := c.s.ActiveRevisions(ctx, c.app)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, rev := range active {
		if !strings.EqualFold(rev.Name, c.baseline) && !strings.EqualFold(rev.Name, c.candidate) {
			out = append(out, rev.Name)
		}
	}
	return out, nil
}

// deactivate deactivates each of revs, and returns those Azure wouldn't.
// They take no traffic, so one left only costs what its replicas do.
func (c *azureCanary) deactivate(ctx context.Context, revs []string) map[string]error {
	left := map[string]error{}
	for _, rev := range revs {
		if err := c.s.DeactivateRevision(ctx, c.app, rev); err != nil {
			left[rev] = err
		}
	}
	return left
}

// noteLeft says which revisions were left active, when it can.
func (c *azureCanary) noteLeft(left map[string]error) {
	if c.note == nil {
		return
	}
	for _, rev := range slices.Sorted(maps.Keys(left)) {
		c.note(fmt.Sprintf("Revision %s is left active, taking no traffic: %v", rev, left[rev]))
	}
}

// start starts the candidate dark: first the baseline is pinned to all of
// the traffic in multiple revisions mode, then whatever else is active
// goes, then the candidate's app is PUT and waited for until it's up. An
// earlier candidate Azure wouldn't deactivate first, because it was the
// latest revision, is tried again once the candidate is. A start that
// fails removes what it made, and says what may be left when it can't.
func (c *azureCanary) start(ctx context.Context, candidate azure.App, restoreBase string) error {
	if err := c.s.SetTraffic(ctx, c.app, azure.MultipleRevisions, c.alone()); err != nil {
		return fmt.Errorf("give the baseline all of the traffic before the candidate starts: %w", err)
	}
	earlier, err := c.others(ctx)
	if err != nil {
		return err
	}
	left := c.deactivate(ctx, earlier)
	// The PUT replaces the app's secrets, so the baseline's, and those of
	// anything Azure wouldn't deactivate, go back as they are.
	if _, err := azureKeepSecrets(ctx, c.s, c.app, &candidate); err != nil {
		return fmt.Errorf("keep the secrets the baseline reads: %w", err)
	}
	if err := c.startIn(ctx, candidate); err != nil {
		// Detached, so a start that failed because its context ended still
		// cleans up. When that was the action's deadline, a retry may start
		// while this runs. The retry's candidate has a suffix of its own,
		// and once it's the template this leaves the traffic alone and only
		// deactivates this candidate.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), candidateCleanup)
		defer cancel()
		if cerr := c.cleanUp(cleanup, restoreBase); cerr != nil {
			return fmt.Errorf("%w. Cleaning up failed too, so revision %s of container app %s may be left active, and the app may still name it as its template: %w",
				err, c.candidate, c.name, cerr)
		}
		return err
	}
	c.noteLeft(c.deactivate(ctx, slices.Sorted(maps.Keys(left))))
	return nil
}

// startIn PUTs the candidate's app and waits until its revision is up.
func (c *azureCanary) startIn(ctx context.Context, candidate azure.App) error {
	if _, err := c.s.PutApp(ctx, c.name, candidate); err != nil {
		return fmt.Errorf("start the candidate: %w", err)
	}
	if _, err := c.s.WaitApp(ctx, c.app); err != nil {
		return fmt.Errorf("start the candidate: %w", err)
	}
	rev, err := c.revision(ctx, c.candidate)
	if err != nil {
		return fmt.Errorf("start the candidate: read whether revision %s is up: %w", c.candidate, err)
	}
	if !rev.Up() {
		return fmt.Errorf("start the candidate: revision %s can't take traffic: %s", c.candidate, rev.Why())
	}
	return nil
}

// cleanUp removes a candidate that failed to start. When the template no
// longer makes it, a retry has started its own candidate, so the traffic
// is the retry's to move and only this candidate is deactivated.
func (c *azureCanary) cleanUp(ctx context.Context, restoreBase string) error {
	app, err := c.s.GetApp(ctx, c.app)
	if err != nil {
		return err
	}
	if c.makesCandidate(app) {
		return c.remove(ctx, restoreBase)
	}
	if err := c.s.DeactivateRevision(ctx, c.app, c.candidate); err != nil {
		return fmt.Errorf("deactivate the candidate revision: %w", err)
	}
	return nil
}

// weigh gives the candidate percent of the traffic. It gives a revision
// none unless it's up, and at 0 the candidate needn't be there at all, as
// a revert retried after it deactivated the candidate finds it.
func (c *azureCanary) weigh(ctx context.Context, percent int) error {
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
	if err := c.s.SetTraffic(ctx, c.app, azure.MultipleRevisions, c.split(percent)); err != nil {
		return fmt.Errorf("give the candidate %d%% of the traffic: %w", percent, err)
	}
	return nil
}

// promote puts the app back in single revision mode with all of the
// traffic on its latest revision, the candidate, and Container Apps
// deactivates the baseline. The candidate only takes all of the traffic
// while it's up and it's the latest, so the next ordinary deploy, which
// makes a newer one, takes it all again.
//
// A candidate that isn't up means an earlier promote got as far as it
// could: when what serves all of the traffic is up and runs the release,
// its whole template as azure.Revision.Runs compares it, there's nothing
// left to do. Otherwise the baseline gets the traffic back and nothing is
// promoted.
func (c *azureCanary) promote(ctx context.Context, rel azure.Template) error {
	candidate, err := c.revision(ctx, c.candidate)
	if err != nil {
		return err
	}
	app, err := c.s.GetApp(ctx, c.app)
	if err != nil {
		return err
	}
	if !candidate.Up() {
		if serving := azureServing(app); serving != "" {
			rev, rerr := c.revision(ctx, serving)
			if rerr != nil {
				return rerr
			}
			if rev.Up() && rev.Runs(rel) {
				return nil
			}
		}
		return c.giveBack(ctx, fmt.Errorf("candidate revision %s can't take all of the traffic (%s), and nothing that serves runs the release, so nothing was promoted", c.candidate, candidate.Why()))
	}
	if p := app.Properties; !strings.EqualFold(p.LatestRevisionName, c.candidate) || !strings.EqualFold(p.LatestReadyRevision, c.candidate) {
		return c.giveBack(ctx, fmt.Errorf("the app's latest revision is %s, not candidate %s, so nothing was promoted", p.LatestRevisionName, c.candidate))
	}
	if err := c.s.SetTraffic(ctx, c.app, azure.SingleRevision, azure.Latest()); err != nil {
		return fmt.Errorf("give the candidate all of the traffic: %w", err)
	}
	return nil
}

// giveBack gives the baseline all of the traffic after err, and says
// whether it got it.
func (c *azureCanary) giveBack(ctx context.Context, err error) error {
	if werr := c.s.SetTraffic(ctx, c.app, azure.MultipleRevisions, c.alone()); werr != nil {
		return fmt.Errorf("%w, and the baseline couldn't get all of the traffic back: %w", err, werr)
	}
	return fmt.Errorf("%w. The baseline has all of the traffic", err)
}

// remove gives the baseline all of the traffic, puts the baseline's
// template back while the template still makes the candidate, then
// deactivates the candidate. Container Apps makes the restored template a
// revision of its own, which takes no traffic, and that and anything else
// active but the baseline is deactivated too, or noted when Azure won't.
// The template goes back before the candidate is deactivated, so the
// candidate is no longer the latest revision by then. Each step is safe
// to repeat.
func (c *azureCanary) remove(ctx context.Context, restoreBase string) error {
	if err := c.s.SetTraffic(ctx, c.app, azure.MultipleRevisions, c.alone()); err != nil {
		return fmt.Errorf("give the baseline all of the traffic: %w", err)
	}
	app, err := c.s.GetApp(ctx, c.app)
	if err != nil {
		return err
	}
	if c.makesCandidate(app) {
		suffix, serr := c.freeSuffix(ctx, restoreBase)
		if serr != nil {
			return fmt.Errorf("put the baseline's template back: %w", serr)
		}
		if rerr := c.s.RestoreTemplate(ctx, c.app, c.baseline, suffix); rerr != nil {
			return fmt.Errorf("put the baseline's template back: %w", rerr)
		}
	}
	if derr := c.s.DeactivateRevision(ctx, c.app, c.candidate); derr != nil {
		return fmt.Errorf("deactivate the candidate revision: %w", derr)
	}
	others, err := c.others(ctx)
	if err != nil {
		return fmt.Errorf("read what's still active beside the baseline: %w", err)
	}
	c.noteLeft(c.deactivate(ctx, others))
	// The candidate's own secrets hold the release's values. Nothing active
	// reads them now.
	if perr := azurePruneSecrets(ctx, c.s, c.app); perr != nil && c.note != nil {
		c.note(fmt.Sprintf("The candidate's secrets are left on the container app: %v", perr))
	}
	return nil
}

// azureBaseline is what a candidate ramps from. Once the canary has
// recorded a baseline, as a retry finds it, that is the baseline for as
// long as its revision is up, whatever share of the traffic it has now: a
// step at 100% leaves it none, and the revision with all of the traffic is
// then the candidate that failed. A recorded baseline that's gone, or
// can't serve, is refused, and nothing else is picked. Only a
// deployment's first start, with nothing recorded, takes the one revision
// that serves, and only while the traffic is settled.
func azureBaseline(ctx context.Context, job *Job, c *azureCanary, app *azure.App) (*azure.Revision, error) {
	if recorded := job.Deployment.Canary.service(job.Action.ID); recorded != nil && recorded.Baseline != "" {
		rev, err := c.revision(ctx, recorded.Baseline)
		if err != nil {
			return nil, err
		}
		if !rev.Up() {
			return nil, fmt.Errorf("%s can't start its canary again: its baseline, revision %s of container app %s, can't serve (%s), so the traffic was left as it is. Release this environment direct once to settle it",
				job.Action.Name, recorded.Baseline, c.name, rev.Why())
		}
		return rev, nil
	}
	if err := azureSettled(job, c.name, app); err != nil {
		return nil, err
	}
	serving := azureServing(app)
	if serving == "" {
		return nil, fmt.Errorf("container app %s serves no revision to ramp a canary from", c.name)
	}
	rev, err := c.revision(ctx, serving)
	if err != nil {
		return nil, err
	}
	if !rev.Up() {
		return nil, fmt.Errorf("%s can't start a canary: revision %s, which serves container app %s, can't serve (%s), so the traffic was left as it is",
			job.Action.Name, serving, c.name, rev.Why())
	}
	if rev.Name == "" {
		rev.Name = serving
	}
	return rev, nil
}

// azureCandidateSecrets renames the secrets a template's variables read
// to the candidate's own, named after its revision suffix, in secrets as
// well, so setting them changes nothing the baseline reads.
func azureCandidateSecrets(t *azure.Template, secrets []azure.Secret, suffix string) {
	renamed := map[string]string{}
	for i := range t.Containers {
		for j, e := range t.Containers[i].Env {
			if e.SecretRef == "" {
				continue
			}
			renamed[e.SecretRef] = e.SecretRef + "-" + suffix
			t.Containers[i].Env[j].SecretRef = renamed[e.SecretRef]
		}
	}
	for i, sec := range secrets {
		if name, ok := renamed[sec.Name]; ok {
			secrets[i].Name = name
		}
	}
}

// azureReleaseTemplate is the template a canary's candidate was started
// from: the release's, reading the candidate's own secrets.
func azureReleaseTemplate(job *Job, image, suffix string) azure.Template {
	t, secrets := azureTemplate(job, job.Action.Service, image)
	azureCandidateSecrets(&t, secrets, suffix)
	return t
}

// ApplyCandidate implements Weighted. It checks the service principal may
// read and deactivate revisions and list secrets, picks the baseline,
// builds the image as Apply does, then starts the candidate as a revision
// beside the baseline, reading secrets of its own.
func (a *Azure) ApplyCandidate(ctx context.Context, job *Job) (*CanaryTarget, error) {
	cfg := job.Action.Service
	if cfg == nil {
		return nil, fmt.Errorf("%w: azure container apps runs services, not %s", ErrNoExecutor, job.Action.Subject)
	}
	s, err := azureSession(ctx, a.Client, job.Connection, job.Credential)
	if err != nil {
		return nil, err
	}
	if job.Binding == nil || !s.Owns(job.Binding.ExternalID) {
		return nil, fmt.Errorf("%w: %s has nothing running on Azure Container Apps to ramp a canary from", ErrNoExecutor, job.Action.Name)
	}
	c := &azureCanary{s: s, app: job.Binding.ExternalID, name: path.Base(job.Binding.ExternalID), note: func(m string) { job.Progress(StepDeploying, m) }}
	if !strings.EqualFold(s.AppPath(c.name), c.app) {
		return nil, fmt.Errorf("%s can't ramp a canary: container app %s isn't in resource group %s, where this release deploys", job.Action.Name, c.name, s.Credential().ResourceGroup)
	}
	if perr := a.canaryPermissions(ctx, s, job); perr != nil {
		return nil, perr
	}
	app, err := s.GetApp(ctx, c.app)
	if azure.IsNotFound(err) {
		return nil, fmt.Errorf("%w: %s has no container app %s to ramp a canary from", ErrNoExecutor, job.Action.Name, c.name)
	}
	if err != nil {
		return nil, err
	}
	if app.Properties.Configuration.Ingress == nil {
		return nil, fmt.Errorf("%s can't ramp a canary: container app %s takes no HTTP traffic", job.Action.Name, c.name)
	}
	if changes := app.IngressChanges(azureAppIngress(cfg, nil)); len(changes) > 0 {
		return nil, azureAppWideRefusal(job, c.name, changes)
	}
	baseline, err := azureBaseline(ctx, job, c, app)
	if err != nil {
		return nil, err
	}
	c.baseline = baseline.Name
	ctx, r, err := a.prepare(ctx, s, job, cfg)
	if err != nil {
		return nil, err
	}
	job.Progress(StepDeploying, "Starting the candidate on "+r.state.Image)
	suffix, err := c.freeSuffix(ctx, azureCandidateSuffix(job.Deployment.ID, job.Deployment.Retries))
	if err != nil {
		return nil, err
	}
	c.candidate = c.name + "--" + suffix
	candidate := azureApp(job, cfg, r.location, r.env.ID, r.pull, r.state.Image, azure.MultipleRevisions, c.alone())
	candidate.Properties.Template.RevisionSuffix = suffix
	azureCandidateSecrets(&candidate.Properties.Template, candidate.Properties.Configuration.Secrets, suffix)
	// How the app pulls only shows once the release is set up.
	if changes := app.AppWideChanges(candidate); len(changes) > 0 {
		return nil, azureAppWideRefusal(job, c.name, changes)
	}
	if err := c.start(ctx, candidate, azureRestoreSuffix(job.Deployment.ID, job.Deployment.Retries)); err != nil {
		return nil, err
	}
	return &CanaryTarget{Baseline: c.baseline, Candidate: c.candidate, Image: r.state.Image, Refs: map[string]string{azureRefApp: c.app}}, nil
}

// azureAppWideRefusal refuses a canary of a release that changes what
// Container Apps applies to every revision at once.
func azureAppWideRefusal(job *Job, app string, changes []string) error {
	return fmt.Errorf("%s can't run a canary: this release changes container app %s's %s, which Container Apps applies app-wide, to every revision at once, the baseline included. Release this environment direct or blue_green once to make that change",
		job.Action.Name, app, strings.Join(changes, ", "))
}

// canaryPermissions refuses a canary whose service principal may not read
// or deactivate revisions or list secrets. It asks Azure now, since the
// connection's last check may be from before canaries, and a principal
// without them is still a complete connection.
func (a *Azure) canaryPermissions(ctx context.Context, s *azure.Session, job *Job) error {
	missing, err := s.MissingOf(ctx, azure.CanaryActions)
	if err != nil {
		return fmt.Errorf("check that the service principal may split traffic: %w", err)
	}
	if len(missing) == 0 {
		return nil
	}
	conn := "the connection"
	if job.Connection != nil && job.Connection.Name != "" {
		conn = job.Connection.Name
	}
	return fmt.Errorf("%s can't run a canary: the service principal of %s isn't allowed %s. Grant them and check the connection again under Integrations, or release this environment direct or blue_green, which don't need them",
		job.Action.Name, conn, strings.Join(missing, ", "))
}

// SetWeights implements Weighted.
func (a *Azure) SetWeights(ctx context.Context, job *Job, t CanaryTarget, candidatePercent int) error {
	s, err := azureSession(ctx, a.Client, job.Connection, job.Credential)
	if err != nil {
		return err
	}
	c, err := azureCanaryFor(s, t, nil)
	if err != nil {
		return err
	}
	return c.weigh(ctx, candidatePercent)
}

// Promote implements Weighted. The candidate becomes what the app runs,
// with all of the traffic, in single revision mode again.
func (a *Azure) Promote(ctx context.Context, job *Job, t CanaryTarget) (*Outcome, error) {
	if job.Action.Service == nil {
		return nil, fmt.Errorf("%w: azure container apps runs services, not %s", ErrNoExecutor, job.Action.Subject)
	}
	s, err := azureSession(ctx, a.Client, job.Connection, job.Credential)
	if err != nil {
		return nil, err
	}
	c, err := azureCanaryFor(s, t, nil)
	if err != nil {
		return nil, err
	}
	if job.Binding == nil || !strings.EqualFold(job.Binding.ExternalID, c.app) {
		return nil, fmt.Errorf("%s's candidate ran in container app %s, which isn't what the binding names now", job.Action.Name, c.name)
	}
	if perr := c.promote(ctx, azureReleaseTemplate(job, t.Image, c.suffix())); perr != nil {
		return nil, perr
	}
	app, err := s.GetApp(ctx, c.app)
	if err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: c.app, URL: s.URL(app), Image: t.Image, Revision: t.Image}, nil
}

// Revert implements Weighted. The baseline gets all of the traffic back,
// the app's template goes back to the baseline revision's, and the
// candidate is deactivated, so the template, the traffic and the binding
// agree again. The app stays in multiple revisions mode with its traffic
// pinned to the baseline until the next deploy, which goes back to single
// revision mode.
func (a *Azure) Revert(ctx context.Context, job *Job, t CanaryTarget) error {
	s, err := azureSession(ctx, a.Client, job.Connection, job.Credential)
	if err != nil {
		return err
	}
	c, err := azureCanaryFor(s, t, func(m string) { job.Progress(StepDeploying, m) })
	if err != nil {
		return err
	}
	return c.remove(ctx, azureRestoreSuffix(job.Deployment.ID, job.Deployment.Retries))
}
