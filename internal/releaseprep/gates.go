package releaseprep

import (
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/releasewatch"
)

// The seven gates a production promotion passes before the agent may
// approve it without a person.
const (
	GateAutonomy    = 1
	GateFromStaging = 2
	GateCleanPlan   = 3
	GateSoak        = 4
	GateSafetyNet   = 5
	GateNothingOpen = 6
	GateBudget      = 7
)

// GateNames are the gates' names, as people read them.
var GateNames = map[int]string{
	GateAutonomy:    "autonomous promotion",
	GateFromStaging: "promotes what staging runs",
	GateCleanPlan:   "clean plan",
	GateSoak:        "staging soak",
	GateSafetyNet:   "safety net",
	GateNothingOpen: "nothing open on staging",
	GateBudget:      "budget",
}

// DefaultMinSoak is how long staging must have run a release when the
// project sets no minimum (decided by Rex).
const DefaultMinSoak = time.Hour

// AlwaysAsk are the production providers with no canary and no watch
// Loomplane can judge, whose releases always go to a person, whatever
// coverage says (Rex, October 25; firecracker and cloudflare-workers added
// November 4). The list is explicit so a new health reader can't turn
// autonomous promotion on for one without a decision: the Kubernetes
// canary plan removes "kubernetes" here, with its own test change.
var AlwaysAsk = []string{"kubernetes", "server", "vercel", "digitalocean", "firecracker", "cloudflare-workers"}

// providerNames are AlwaysAsk's providers as people read them.
var providerNames = map[string]string{
	"kubernetes": "Kubernetes", "server": "a server", "vercel": "Vercel", "digitalocean": "DigitalOcean",
	"firecracker": "Firecracker", "cloudflare-workers": "Cloudflare Workers",
}

// CheckInput is everything the gates read. Checks reads nothing else.
type CheckInput struct {
	Project *projects.Project
	// RequesterCanApprove: the report's requester holds deployment:approve
	// on the project.
	RequesterCanApprove bool
	// RequesterError says why that couldn't be checked, in Loomplane's
	// own words: it's quoted in gate 1's detail.
	RequesterError string
	Report         *ReleaseReport
	// TaskID is the run asking. An empty one never matches.
	TaskID string
	// Requester is the person the asking run acts for; "" when it acts
	// for nobody.
	Requester string
	Plan      *planning.Plan
	From, To  *projects.Environment
	Evidence  *ReadinessEvidence
	// Now is when the gates are checked. No gate reads a clock: every
	// duration comes from the evidence, measured when it was read.
	Now time.Time

	// preview skips the checks of the run asking. Only PreviewChecks sets
	// it.
	preview bool
}

// Checks computes the seven gates from Loomplane's records and the
// evidence, always seven, numbered 1 to 7, in order. Nothing a model wrote
// is read: concerns only add reasons in Failed. Every detail is
// Loomplane's own words (names, IDs, counts and fixed sentences), since
// details travel in realtime events; outside text in the evidence is never
// quoted.
func Checks(in CheckInput) []GateCheck {
	in.preview = false
	return checks(in)
}

// PreviewChecks is the job's preview of the gates, before any run asks.
// It skips the checks of the run: its task, the person it acts for, the
// report reviewing and holding the evidence the agent reviews. Nothing
// else previews, so an evaluation can never skip them; the gate calls
// Checks.
func PreviewChecks(in CheckInput) []GateCheck {
	in.preview = true
	return checks(in)
}

func checks(in CheckInput) []GateCheck {
	gates := []func(CheckInput) []string{autonomy, fromStaging, cleanPlan, soak, safetyNet, nothingOpen, budget}
	passed := []func(CheckInput) string{autonomyOK, fromStagingOK, cleanPlanOK, soakOK, safetyNetOK, nothingOpenOK, budgetOK}
	out := make([]GateCheck, 0, len(gates))
	for i, gate := range gates {
		n := i + 1
		c := GateCheck{Number: n, Name: GateNames[n]}
		if why := gate(in); len(why) > 0 {
			c.Detail = strings.Join(why, "; ")
		} else {
			c.Passed, c.Detail = true, passed[i](in)
		}
		out = append(out, c)
	}
	return out
}

// Failed lists what a person reads: one line per failing gate, "gate N
// (<name>): <detail>", then one per concern that asks, "the agent asked
// for a person: <text>". Any concern that isn't a risk note asks. Nothing
// in a concern removes a line.
func Failed(checks []GateCheck, concerns []Concern) []string {
	out := []string{}
	for _, c := range checks {
		if !c.Passed {
			out = append(out, fmt.Sprintf("gate %d (%s): %s", c.Number, c.Name, c.Detail))
		}
	}
	for _, c := range concerns {
		if c.Kind != ConcernRisk {
			out = append(out, "the agent asked for a person: "+c.Text)
		}
	}
	return out
}

// usd is a decimal amount in dollars: digits, and optionally a point and
// more digits.
var usd = regexp.MustCompile(`^\d+(\.\d+)?$`)

func parseUSD(s string) (*big.Rat, error) {
	if !usd.MatchString(s) {
		return nil, fmt.Errorf("%q isn't an amount in dollars", s)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("%q isn't an amount in dollars", s)
	}
	return r, nil
}

// WatchReserve is the spend kept free for the post-deploy watch's
// follow-up: the larger of $1 and 10% of the limit (decided by Rex).
func WatchReserve(limitUSD string) (*big.Rat, error) {
	limit, err := parseUSD(limitUSD)
	if err != nil {
		return nil, err
	}
	tenth := new(big.Rat).Mul(limit, big.NewRat(1, 10))
	if one := big.NewRat(1, 1); tenth.Cmp(one) < 0 {
		return one, nil
	}
	return tenth, nil
}

// unread says a piece of evidence couldn't be read. It never quotes why:
// that's error text from outside Loomplane, which the report keeps under
// its unavailable evidence.
func unread(what string) string {
	return "couldn't read " + what + ", so it counts as missing evidence"
}

// missing reports whether a key of the evidence couldn't be read.
func missing(ev *ReadinessEvidence, key string) bool {
	_, ok := ev.Unavailable[key]
	return ok
}

// Gate 1: the project lets the agent promote alone, and the person who
// asked could approve the release themselves.
func autonomy(in CheckInput) []string {
	var why []string
	switch {
	case in.Project == nil:
		return []string{"couldn't read the project"}
	case !in.Project.AutonomousPromotion:
		why = append(why, "autonomous promotion is off for this project")
	}
	switch {
	case in.RequesterError != "":
		why = append(why, "couldn't read whether the person who asked may approve production releases: "+in.RequesterError)
	case !in.RequesterCanApprove:
		why = append(why, "the person who asked can't approve production releases in this project, so someone who can approves it")
	}
	return why
}

func autonomyOK(CheckInput) string {
	return "autonomous promotion is on, and the person who asked may approve production releases"
}

// Gate 2: the plan is the promotion this preparation built, from staging
// to production, asked for by this run, and staging still runs its pins.
func fromStaging(in CheckInput) []string {
	r, plan := in.Report, in.Plan
	switch {
	case r == nil:
		return []string{"no release readiness report covers this plan; ask the agent to prepare the release"}
	case plan == nil:
		return []string{"couldn't read the plan"}
	}
	var why []string
	if plan.ID != r.PlanID || plan.ProjectID != r.ProjectID || plan.EnvironmentID != r.ToEnvironmentID || plan.FromEnvironmentID != r.FromEnvironmentID {
		return []string{"this is not the plan this release preparation built"}
	}
	if plan.Revision != r.PlanRevision {
		why = append(why, "the plan changed since this release preparation read it")
	}
	if plan.Kind != planning.KindPromotion {
		why = append(why, fmt.Sprintf("the plan is a %s plan, not a promotion", plan.Kind))
	}
	switch {
	case in.From == nil:
		why = append(why, "couldn't read the environment it promotes from")
	case in.From.ID != r.FromEnvironmentID:
		why = append(why, "this is not the plan this release preparation built")
	case in.From.Kind != projects.KindStaging:
		why = append(why, fmt.Sprintf("it promotes from %s, which isn't a staging environment", in.From.Name))
	}
	switch {
	case in.To == nil:
		why = append(why, "couldn't read the environment it deploys to")
	case in.To.ID != r.ToEnvironmentID:
		why = append(why, "this is not the plan this release preparation built")
	case in.To.Kind != projects.KindProduction:
		why = append(why, fmt.Sprintf("%s isn't a production environment", in.To.Name))
	}
	if !in.preview {
		// The task is recorded just after the run starts, so a call made
		// before that lands fails here and asks: fail closed, nothing more.
		switch {
		case r.Status != StatusReviewing:
			why = append(why, fmt.Sprintf("the release preparation is %s, not reviewing", r.Status))
		case in.TaskID == "" || in.TaskID != r.TaskID:
			why = append(why, "the run asking isn't this release preparation's own run")
		}
		if in.Requester == "" || in.Requester != r.RequestedBy {
			why = append(why, "this release preparation was asked for by someone else")
		}
		if r.Evidence == nil {
			why = append(why, "this release preparation holds no evidence the agent reviewed")
		}
	}
	switch ev := in.Evidence; {
	case ev == nil:
		why = append(why, unread("what staging runs now"))
	case missing(ev, KeyPins):
		why = append(why, unread("what staging runs now"))
	case !ev.PinsMatch:
		moved := "staging no longer runs what the plan pins"
		if len(ev.PinDifferences) > 0 {
			moved += ": " + strings.Join(ev.PinDifferences, "; ")
		}
		why = append(why, moved)
	}
	return dedupe(why)
}

func fromStagingOK(in CheckInput) string {
	if in.preview {
		return fmt.Sprintf("it promotes exactly what %s runs to %s", in.From.Name, in.To.Name)
	}
	return fmt.Sprintf("it promotes exactly what %s runs to %s, in this release preparation's own run", in.From.Name, in.To.Name)
}

// Gate 3: the plan is ready, has no problems or warnings, destroys
// nothing, sets no backup schedule, and every diff was read.
func cleanPlan(in CheckInput) []string {
	var why []string
	if in.Plan == nil {
		why = append(why, "couldn't read the plan")
	} else if in.Plan.Status != planning.StatusReady {
		why = append(why, fmt.Sprintf("the plan is %s, not ready", in.Plan.Status))
	}
	ev := in.Evidence
	if ev == nil {
		return append(why, unread("the plan's evidence"))
	}
	if len(ev.Problems) > 0 {
		why = append(why, fmt.Sprintf("the plan has problems (%s)", strings.Join(codes(ev.Problems), ", ")))
	}
	var others, back []string
	for _, w := range ev.Warnings {
		switch w.Code {
		case "newer-manifest":
			why = append(why, "it promotes an older manifest revision than the latest, and promoting from an older manifest is a person's call")
		case WarningProductionMovesBack:
			back = append(back, w.Name)
		default:
			others = append(others, w.Code)
		}
	}
	if len(back) > 0 {
		why = append(why, fmt.Sprintf("production would move back to older code (%s), which a person decides", strings.Join(back, ", ")))
	}
	if len(others) > 0 {
		why = append(why, fmt.Sprintf("the plan has warnings (%s)", strings.Join(dedupe(others), ", ")))
	}
	if len(ev.Destroy) > 0 {
		why = append(why, fmt.Sprintf("it removes or replaces production data or resources (%s), which always needs a person", strings.Join(ev.Destroy, ", ")))
	}
	if ev.BackupSchedule {
		why = append(why, "it sets a default backup schedule, which a person who manages backups approves")
	}
	var unreadable []string
	for _, d := range ev.Sources {
		if d.Error != "" {
			unreadable = append(unreadable, d.Alias)
		}
	}
	switch {
	case len(unreadable) > 0:
		why = append(why, unread("the diff of "+strings.Join(unreadable, ", ")))
	case missing(ev, KeyDiff):
		why = append(why, unread("the diff"))
	}
	return why
}

func cleanPlanOK(in CheckInput) string {
	return fmt.Sprintf("the plan is ready, with no problems or warnings, destroys nothing, and every diff was read (%d %s)",
		len(in.Evidence.Sources), plural(len(in.Evidence.Sources), "source", "sources"))
}

// minSoak is the project's minimum staging soak, or DefaultMinSoak.
func minSoak(p *projects.Project) time.Duration {
	if p != nil && p.MinStagingSoakSeconds > 0 {
		return time.Duration(p.MinStagingSoakSeconds) * time.Second
	}
	return DefaultMinSoak
}

// Gate 4: staging has run the plan's commits, healthy and verified, for
// at least the minimum, and its current release is verified.
func soak(in CheckInput) []string {
	ev := in.Evidence
	if ev == nil {
		return []string{unread("the staging soak")}
	}
	env := stagingName(in.From)
	var why []string
	switch s := ev.Soak; {
	case s == nil || missing(ev, KeySoak):
		why = append(why, unread("the staging soak"))
	case !s.Running && !movesARepository(in.Plan):
		why = append(why, "the plan moves no repository, so there's nothing to soak")
	case !s.Running:
		why = append(why, env+" doesn't run every commit the plan pins")
	default:
		failed, unjudged := soakVerdicts(s.Services)
		if !s.Healthy && !s.Unverified {
			msg := env + " isn't healthy"
			if len(failed) > 0 {
				msg += ": the health check failed for " + strings.Join(failed, ", ")
			}
			why = append(why, msg)
		}
		if s.Unverified {
			msg := "the " + env + " soak is unverified"
			if len(unjudged) > 0 {
				msg += ": Loomplane couldn't judge " + strings.Join(unjudged, ", ")
			}
			why = append(why, msg)
		}
		if lowest := minSoak(in.Project); s.Duration < lowest {
			why = append(why, fmt.Sprintf("%s has soaked %s of the %s minimum", env, formatDuration(s.Duration), formatDuration(lowest)))
		}
	}
	switch r := ev.StagingRelease; {
	case r == nil || missing(ev, KeyStagingRelease):
		why = append(why, unread(env+"'s current release"))
	case r.Verdict == string(releasehealth.Fail):
		why = append(why, env+"'s current release failed its post-deploy watch")
	case r.Unverified:
		why = append(why, env+"'s current release is unverified: "+unverifiedRelease(r, env))
	}
	return why
}

// movesARepository reports whether the plan moves at least one source.
func movesARepository(plan *planning.Plan) bool {
	return plan == nil || slices.ContainsFunc(plan.Sources, func(s planning.PlanSource) bool { return s.InScope })
}

// unverifiedRelease says why a release is unverified from what Loomplane
// knows of it. It never quotes the watch's reason, which can hold a health
// reader's error.
func unverifiedRelease(r *releasewatch.ReleaseEvidence, env string) string {
	switch {
	case r.DeploymentID == "":
		return env + " has no release yet"
	case !r.Watched:
		return "it wasn't watched"
	case !r.Settled:
		return "its post-deploy watch hasn't finished"
	}
	return "its post-deploy watch couldn't judge it"
}

// soakVerdicts are the services that failed, and those Loomplane couldn't
// judge. Only names: a service's reason can hold a cloud's error text.
func soakVerdicts(services []releasewatch.SoakService) (failed, unjudged []string) {
	for _, s := range services {
		switch s.Verdict {
		case releasehealth.Pass:
		case releasehealth.Fail:
			failed = append(failed, s.Name)
		default:
			unjudged = append(unjudged, s.Name)
		}
	}
	return failed, unjudged
}

func soakOK(in CheckInput) string {
	return fmt.Sprintf("%s has run it for %s, healthy, past the %s minimum", stagingName(in.From),
		formatDuration(in.Evidence.Soak.Duration), formatDuration(minSoak(in.Project)))
}

// canaryWords say how a service's last canary ended when it didn't
// promote. Every status but promoted is unresolved: a failure until a
// newer canary of the service promotes, and one still moving resolves
// nothing.
var canaryWords = map[string]string{
	deployments.CanaryRolledBack:    "rolled back, and no later canary of it promoted",
	deployments.CanaryRevertFailed:  "couldn't move traffic back, and no later canary of it promoted",
	deployments.CanaryPromoteFailed: "failed to promote, and no later canary of it promoted",
	deployments.CanaryPending:       "hasn't finished",
	deployments.CanaryRamping:       "hasn't finished",
	deployments.CanaryPromoting:     "hasn't finished",
	deployments.CanaryReverting:     "hasn't finished",
}

// judgeable reports whether Loomplane has a canary or a watch reader for a
// provider: it's in the support matrix and not in AlwaysAsk. An empty or
// unknown provider isn't.
func judgeable(provider string) bool {
	if provider == "" || slices.Contains(AlwaysAsk, provider) {
		return false
	}
	return slices.ContainsFunc(releasehealth.Matrix(), func(s releasehealth.Support) bool { return s.Provider == provider })
}

// Gate 5: something guards every changed service once it's live (a
// canary, or a post-deploy watch that can judge it), production's last
// release is verified, no data-bearing resource is updated, and no
// changed service's last canary failed without a newer one promoting.
func safetyNet(in CheckInput) []string {
	ev := in.Evidence
	if ev == nil {
		return []string{unread("production's safety net")}
	}
	var why []string
	cov := ev.Coverage
	if cov == nil || missing(ev, KeyCoverage) {
		why = append(why, unread("production's post-deploy watch coverage"))
		cov = nil
	}
	for _, s := range ev.ChangedServices {
		provider := s.Provider
		if cov != nil {
			// The environment's own record of the service counts too.
			for _, row := range cov.Services {
				if row.Name == s.Name && slices.Contains(AlwaysAsk, row.Provider) {
					provider = row.Provider
				}
			}
		}
		switch {
		case slices.Contains(AlwaysAsk, provider):
			why = append(why, fmt.Sprintf("%s runs on %s, which has no canary or watch Loomplane can judge, so a person releases it", s.Name, providerNames[provider]))
			continue
		case !judgeable(provider):
			why = append(why, s.Name+" runs on a provider Loomplane can't judge, so a person releases it")
			continue
		}
		if cov != nil && !guarded(s, cov.Duration, cov.Services) {
			why = append(why, s.Name+" has no canary and no post-deploy watch that can judge it")
		}
	}
	if cov != nil && cov.Unverified {
		why = append(why, "production's post-deploy watch coverage is unverified")
	}
	if len(ev.DataUpdates) > 0 {
		why = append(why, fmt.Sprintf("it updates data-bearing resources (%s), which no canary or watch can roll back, so a person approves it", strings.Join(ev.DataUpdates, ", ")))
	}
	switch r := ev.ProductionRelease; {
	case r == nil || missing(ev, KeyProductionRelease):
		why = append(why, unread("production's last release"))
	case r.Verdict == string(releasehealth.Fail):
		why = append(why, "the last production release failed its post-deploy watch")
	case r.Unverified:
		why = append(why, "the last production release is unverified: "+unverifiedRelease(r, envOr(in.To, "production")))
	}
	if ev.Canaries == nil || missing(ev, KeyCanaries) {
		why = append(why, unread("production's canaries"))
	} else {
		for _, s := range ev.ChangedServices {
			why = append(why, lastCanary(ev.Canaries, s)...)
		}
	}
	return why
}

// guarded reports whether a service is covered once live: a canary the
// release ramps on a provider that can run one, or a post-deploy watch
// that can judge it.
func guarded(s ChangedService, watch time.Duration, services []releasewatch.ServiceCoverage) bool {
	if s.Canary && releasehealth.SupportFor(s.Provider).Canary {
		return true
	}
	return watch > 0 && slices.ContainsFunc(services, func(c releasewatch.ServiceCoverage) bool { return c.Name == s.Name && c.Watchable })
}

// lastCanary says why a service's newest canary leaves it unresolved, or
// nothing when it promoted or no canary ever ramped it.
func lastCanary(runs []CanarySummary, s ChangedService) []string {
	if s.LastCanary == "" {
		return nil
	}
	i := slices.IndexFunc(runs, func(r CanarySummary) bool { return r.DeploymentID == s.LastCanary })
	switch {
	case i < 0 || !slices.Contains(runs[i].ActionIDs, s.ActionID):
		return []string{unread(s.Name + "'s last canary")}
	case runs[i].Status == deployments.CanaryPromoted:
		return nil
	}
	words, ok := canaryWords[runs[i].Status]
	if !ok {
		words = "ended in a state Loomplane doesn't know"
	}
	return []string{fmt.Sprintf("%s's last canary %s", s.Name, words)}
}

func safetyNetOK(in CheckInput) string {
	ev := in.Evidence
	if len(ev.ChangedServices) == 0 {
		return "the plan changes no service"
	}
	var ramped, watched []string
	for _, s := range ev.ChangedServices {
		if s.Canary && releasehealth.SupportFor(s.Provider).Canary {
			ramped = append(ramped, s.Name)
		} else {
			watched = append(watched, s.Name)
		}
	}
	parts := []string{"no canary ran"}
	if len(ramped) > 0 {
		parts[0] = "a canary ramps " + strings.Join(ramped, ", ")
	}
	if len(watched) > 0 {
		parts = append(parts, fmt.Sprintf("a %s post-deploy watch guards %s", formatDuration(ev.Coverage.Duration), strings.Join(watched, ", ")))
	}
	return strings.Join(parts, "; ")
}

// Gate 6: no agent report is open on staging.
func nothingOpen(in CheckInput) []string {
	ev := in.Evidence
	env := stagingName(in.From)
	if ev == nil || ev.OpenReports == nil || missing(ev, KeyReports) {
		return []string{unread("the agent reports open on " + env)}
	}
	if n := len(ev.OpenReports); n > 0 {
		// IDs and severities only: a report's title is untrusted.
		ids := make([]string, 0, n)
		for _, r := range ev.OpenReports {
			id := r.ID
			if slices.Contains(severities, r.Severity) {
				id += " (" + r.Severity + ")"
			}
			ids = append(ids, id)
		}
		return []string{fmt.Sprintf("%d agent %s open on %s: %s", n, plural(n, "report is", "reports are"), env, strings.Join(ids, ", "))}
	}
	return nil
}

func nothingOpenOK(in CheckInput) string {
	return "no agent report is open on " + stagingName(in.From)
}

// Gate 7: the agent's budget isn't used up, and what's left keeps the
// watch's reserve free.
func budget(in CheckInput) []string {
	ev := in.Evidence
	if ev == nil || ev.Budget == nil || missing(ev, KeyBudget) {
		return []string{unread("the budget")}
	}
	b := ev.Budget
	if b.Exhausted {
		return []string{"the agent's budget is used up"}
	}
	left, limit, reserve, err := budgetLeft(b.LimitUSD, b.SpentUSD)
	if err != nil {
		return []string{"couldn't read the budget: its amounts aren't in dollars, so it counts as missing evidence"}
	}
	if left.Cmp(reserve) < 0 {
		return []string{fmt.Sprintf("$%s left of $%s, less than the $%s kept for the post-deploy watch", money(left), money(limit), money(reserve))}
	}
	return nil
}

func budgetLeft(limitUSD, spentUSD string) (left, limit, reserve *big.Rat, err error) {
	limit, err = parseUSD(limitUSD)
	if err != nil {
		return nil, nil, nil, err
	}
	spent, err := parseUSD(spentUSD)
	if err != nil {
		return nil, nil, nil, err
	}
	reserve, err = WatchReserve(limitUSD)
	if err != nil {
		return nil, nil, nil, err
	}
	return new(big.Rat).Sub(limit, spent), limit, reserve, nil
}

func budgetOK(in CheckInput) string {
	b := in.Evidence.Budget
	left, limit, reserve, err := budgetLeft(b.LimitUSD, b.SpentUSD)
	if err != nil {
		return "the budget isn't used up"
	}
	return fmt.Sprintf("$%s left of $%s keeps the $%s reserve for the post-deploy watch", money(left), money(limit), money(reserve))
}

// severities are 3c's, the only ones a detail names.
var severities = []string{"critical", "high", "medium", "low"}

// money is an amount with two decimals.
func money(r *big.Rat) string { return r.FloatString(2) }

// stagingName is the name of the environment a promotion comes from, or
// "staging" when it couldn't be read.
func stagingName(e *projects.Environment) string { return envOr(e, "staging") }

// envOr is an environment's name, or fallback when it couldn't be read.
func envOr(e *projects.Environment, fallback string) string {
	if e == nil {
		return fallback
	}
	return e.Name
}

// formatDuration is a duration in whole minutes as people read it: 18m,
// 1h, 1h30m.
func formatDuration(d time.Duration) string {
	m := int(d / time.Minute)
	switch h, rest := m/60, m%60; {
	case h == 0:
		return fmt.Sprintf("%dm", rest)
	case rest == 0:
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dh%dm", h, rest)
	}
}

func codes(fs []planning.Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Code)
	}
	return dedupe(out)
}

// dedupe keeps each string once, in order.
func dedupe(in []string) []string {
	out := in[:0:0]
	for _, s := range in {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
