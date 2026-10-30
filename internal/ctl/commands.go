package ctl

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
)

var jsonFlag = cli.NewBoolFlag("json", "", "Print JSON, for scripts", false)

func (a *App) commands() []cli.Command {
	return []cli.Command{
		a.mcpCommand(),
		a.federationCommand(),
		a.cloudsCommand(),
		a.cloudTargetsCommand(),
		a.cloudSyncsCommand(),
		a.cloudServersCommand(),
		a.cloudClustersCommand(),
		a.cloudPoolsCommand(),
		a.cloudCapacityCommand(),
		a.dnsZonesCommand(),
		a.domainRoutesCommand(),
		a.cloudGrantsCommand(),
		a.backupCommand(),
		a.restoreCommand(),
		a.retirementsCommand(),
		a.servicesCommand(),
		a.storageCommand(),
		cli.NewCommand("login", "Save where Loomplane is and the API key to use", a.login,
			cli.WithUsage("login --url https://loomplane.example.com --key lp_live_..."),
			cli.WithFlags(
				cli.NewStringFlag("url", "", "Loomplane's address", ""),
				cli.NewStringFlag("key", "", "An API key from Settings, API keys; - reads it from standard input", ""),
			)),
		cli.NewCommand("logout", "Forget the saved key", a.logout),
		cli.NewCommand("whoami", "Who the key acts as, and what it may do", a.whoami, cli.WithFlag(jsonFlag)),
		cli.NewCommand("projects", "List the organization's projects", a.projects,
			cli.WithFlag(jsonFlag),
			cli.WithSubcommand(cli.NewCommand("create", "Create a project, from its repositories", a.createProject,
				cli.WithUsage("projects create <name> --repo acme/web --repo billing=other/web"),
				cli.WithFlags(
					cli.NewStringSliceFlag("repo", "", "A repository, as owner/name or a URL; repeat it for each, and write alias=repository to name one", nil),
					jsonFlag)))),
		cli.NewCommand("analyze", "Read a project's repositories and find their services", a.analyze,
			cli.WithUsage("analyze <project> [--source web] [--ref main]"),
			cli.WithFlags(
				cli.NewStringSliceFlag("source", "s", "A repository to analyze, by alias; every one if none", nil),
				cli.NewStringFlag("ref", "", "A branch, tag or commit, for one repository; its tracked branch if empty", ""),
				cli.NewBoolFlag("no-wait", "", "Start it and return", false), jsonFlag)),
		cli.NewCommand("manifest", "Print a project's loomplane.yaml", a.manifest,
			cli.WithUsage("manifest <project> [--draft]"),
			cli.WithFlag(cli.NewBoolFlag("draft", "", "Print a draft from the latest analysis instead", false)),
			cli.WithSubcommand(cli.NewCommand("save", "Save a new revision of a project's loomplane.yaml", a.saveManifest,
				cli.WithUsage("manifest save <project> -f loomplane.yaml"),
				cli.WithFlag(cli.NewStringFlag("file", "f", "The file; - reads standard input", "loomplane.yaml"))))),
		cli.NewCommand("plan", "Plan a project's manifest for an environment", a.plan,
			cli.WithUsage("plan <project> --env production [--from staging] [--scope web] [--catalog]"),
			cli.WithFlags(
				cli.NewStringFlag("env", "e", "The environment", "production"),
				cli.NewStringFlag("from", "", "An environment to promote from: the plan pins the commits it runs", ""),
				cli.NewStringSliceFlag("scope", "", "A repository the plan moves, by alias; every one if none, and the rest stay as deployed", nil),
				cli.NewBoolFlag("catalog", "", "Plan standalone resources without a repository", false),
				cli.NewBoolFlag("no-wait", "", "Start planning and return", false), jsonFlag)),
		a.promoteCommand(),
		a.rollbackCommand(),
		cli.NewCommand("plans", "List a project's plans", a.plans, cli.WithUsage("plans <project>"), cli.WithFlag(jsonFlag)),
		cli.NewCommand("show", "Show a plan: what it would do, and anything in the way", a.show,
			cli.WithUsage("show <plan-id>"), cli.WithFlag(jsonFlag)),
		cli.NewCommand("approve", "Approve a plan, which deploys it", a.approve,
			cli.WithUsage("approve <plan-id> [--yes] [--note ...]"),
			cli.WithFlags(
				cli.NewStringFlag("note", "", "Why, for the record", ""),
				cli.NewBoolFlag("yes", "y", "Don't ask first", false),
				cli.NewBoolFlag("no-wait", "", "Approve and return without following the deployment", false))),
		cli.NewCommand("reject", "Reject a plan", a.reject,
			cli.WithUsage("reject <plan-id> [--note ...]"),
			cli.WithFlag(cli.NewStringFlag("note", "", "Why, for the record", ""))),
		cli.NewCommand("deployments", "List a project's deployments", a.deployments,
			cli.WithUsage("deployments <project>"), cli.WithFlag(jsonFlag)),
		cli.NewCommand("deployment", "Show a deployment, step by step", a.deployment,
			cli.WithUsage("deployment <deployment-id> [--watch]"),
			cli.WithFlags(cli.NewBoolFlag("watch", "w", "Follow it until it finishes", false), jsonFlag)),
		cli.NewCommand("retry", "Retry a failed deployment from the step that failed", a.retry,
			cli.WithUsage("retry <deployment-id> [--no-wait]"),
			cli.WithFlag(cli.NewBoolFlag("no-wait", "", "Retry and return", false))),
		cli.NewCommand("resume", "Check capacity again for a deployment paused for it", a.resume,
			cli.WithUsage("resume <deployment-id> --revision <n>"),
			cli.WithFlags(pauseRevisionFlag(), jsonFlag)),
		cli.NewCommand("cancel", "Cancel a deployment paused for capacity; finished steps stay", a.cancel,
			cli.WithUsage("cancel <deployment-id> --revision <n>"),
			cli.WithFlags(pauseRevisionFlag(), jsonFlag)),
		cli.NewCommand("status", "What runs each of a project's services and resources, and any drift", a.status,
			cli.WithUsage("status <project>"), cli.WithFlag(jsonFlag)),
		cli.NewCommand("vars", "List a project's variables; secrets show no value", a.vars,
			cli.WithUsage("vars <project>"),
			cli.WithFlag(jsonFlag),
			cli.WithSubcommand(cli.NewCommand("set", "Set a project variable", a.setVar,
				cli.WithUsage("vars set <project> KEY=VALUE [--env production] [--secret]; KEY=- reads the value from standard input"),
				cli.WithFlags(
					cli.NewStringFlag("env", "e", "One environment; every one if empty", ""),
					cli.NewStringFlag("service", "s", "One service's graph ID; every service if empty", ""),
					cli.NewBoolFlag("secret", "", "Keep it secret: write-only, and in the cloud's secret store", false))))),
	}
}

func (a *App) login(ctx cli.CommandContext) error {
	s, err := a.settings()
	if err != nil {
		return err
	}
	if v := ctx.String("url"); v != "" {
		s.URL = strings.TrimRight(v, "/")
	}
	key := ctx.String("key")
	if key == "-" {
		line, rerr := bufio.NewReader(a.opts.In).ReadString('\n')
		if rerr != nil && line == "" {
			return fmt.Errorf("read the key: %w", rerr)
		}
		key = line
	}
	if key = strings.TrimSpace(key); key != "" {
		s.Key = key
	}
	if s.URL == "" || s.Key == "" {
		return errors.New("give both --url and --key; make a key under Settings, API keys")
	}
	who, err := client.New(s.URL, s.Key).Session(ctxOf(ctx))
	if err != nil {
		return fmt.Errorf("that key doesn't work at %s: %w", s.URL, err)
	}
	if err := a.save(s); err != nil {
		return err
	}
	a.printf("Logged in to %s, in organization %s.\n", s.URL, who.OrgID)
	return nil
}

func (a *App) logout(cli.CommandContext) error {
	if err := os.Remove(a.opts.ConfigPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	a.printf("Logged out. The key still works until you revoke it under Settings, API keys.\n")
	return nil
}

func (a *App) whoami(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	who, err := c.Session(ctxOf(ctx))
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(who)
	}
	a.printf("User:         %s\nOrganization: %s\nMay:          %s\n", who.UserID, who.OrgID, strings.Join(who.Permissions, ", "))
	return nil
}

func (a *App) projects(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	list, err := c.Projects(ctxOf(ctx))
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(list)
	}
	rows := make([][]string, 0, len(list))
	for _, p := range list {
		rows = append(rows, []string{p.Slug, p.Name, orDash(strings.Join(p.Repositories, ", ")), p.ID})
	}
	a.table([]string{"SLUG", "NAME", "REPOSITORIES", "ID"}, rows)
	return nil
}

// newRepositories reads each --repo: a repository as owner/name or a URL,
// or alias=repository to name it. A GitHub address never has an "=", so
// the first one splits them.
func newRepositories(flags []string) ([]client.NewRepository, error) {
	var out []client.NewRepository
	for _, f := range flags {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		var r client.NewRepository
		if alias, repo, ok := strings.Cut(f, "="); ok {
			r.Alias, r.Repository = strings.TrimSpace(alias), strings.TrimSpace(repo)
			if r.Alias == "" || r.Repository == "" {
				return nil, fmt.Errorf("--repo %s: write alias=repository, with both", f)
			}
		} else {
			r.Repository = f
		}
		out = append(out, r)
	}
	return out, nil
}

func (a *App) createProject(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	name := strings.TrimSpace(strings.Join(ctx.Args(), " "))
	if name == "" {
		return errors.New("name the project")
	}
	repos, err := newRepositories(ctx.StringSlice("repo"))
	if err != nil {
		return err
	}
	p, err := c.CreateProject(ctxOf(ctx), client.NewProject{Name: name, Repositories: repos})
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(p)
	}
	a.printf("Created %s (%s). Next: loomctl analyze %s\n", p.Name, p.ID, p.Slug)
	return nil
}

func (a *App) analyze(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	queued, err := c.Analyze(ctxOf(ctx), p.ID, client.AnalyzeInput{Sources: ctx.StringSlice("source"), Ref: ctx.String("ref")})
	if err != nil {
		return err
	}
	var failed []string
	for i := range queued {
		an := &queued[i]
		if !ctx.Bool("no-wait") {
			last := ""
			an, err = c.WaitAnalysis(ctxOf(ctx), an.ID, func(x *client.Analysis) {
				if x.Status != last && !ctx.Bool("json") {
					last = x.Status
					a.printf("%s: %s\n", orDash(x.Source), x.Status)
				}
			})
			if err != nil {
				return err
			}
			queued[i] = *an
		}
		if ctx.Bool("json") {
			continue
		}
		switch an.Status {
		case "succeeded":
			a.printf("%s: found %d services (%d deployable), %d packages and %d resources at %s.\n", orDash(an.Source),
				an.Summary.Services, an.Summary.Deployable, an.Summary.Packages, an.Summary.Resources, short(an.CommitSHA))
		case "failed":
			failed = append(failed, fmt.Sprintf("%s: %s", orDash(an.Source), an.Error))
		default:
			a.printf("Analysis %s of %s is %s.\n", an.ID, orDash(an.Source), an.Status)
		}
	}
	if ctx.Bool("json") {
		return a.json(queued)
	}
	if len(failed) > 0 {
		return fmt.Errorf("the analysis failed: %s", strings.Join(failed, "; "))
	}
	return nil
}

func (a *App) manifest(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	if ctx.Bool("draft") {
		yaml, derr := c.DraftManifest(ctxOf(ctx), p.ID)
		if derr != nil {
			return derr
		}
		a.printf("%s", yaml)
		return nil
	}
	m, err := c.Manifest(ctxOf(ctx), p.ID)
	if errors.Is(err, client.ErrNotFound) {
		return fmt.Errorf("%s has no manifest yet; draft one with `loomctl manifest %s --draft`", p.Name, p.Slug)
	}
	if err != nil {
		return err
	}
	a.printf("%s", m.YAML)
	return nil
}

func (a *App) saveManifest(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	var data []byte
	if f := ctx.String("file"); f == "-" {
		data, err = readAll(a.opts.In)
	} else {
		data, err = os.ReadFile(f)
	}
	if err != nil {
		return err
	}
	m, err := c.SaveManifest(ctxOf(ctx), p.ID, string(data))
	if err != nil {
		return err
	}
	a.printf("Saved revision %d of %s's manifest.\n", m.Revision, p.Name)
	return nil
}

func (a *App) plan(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	plan, err := c.CreatePlan(ctxOf(ctx), p.ID, client.NewPlan{Environment: ctx.String("env"), From: ctx.String("from"), Scope: ctx.StringSlice("scope"), Catalog: ctx.Bool("catalog")})
	if err != nil {
		return err
	}
	if !ctx.Bool("no-wait") {
		if plan, err = c.WaitPlan(ctxOf(ctx), plan.ID); err != nil {
			return err
		}
	}
	if ctx.Bool("json") {
		return a.json(plan)
	}
	a.printPlan(plan)
	return nil
}

func (a *App) plans(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	list, err := c.Plans(ctxOf(ctx), p.ID)
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(list)
	}
	rows := make([][]string, 0, len(list))
	for _, pl := range list {
		rows = append(rows, []string{pl.ID, pl.Environment, pl.Status, fmt.Sprint(len(pl.Actions)), orDash(pl.Commits()), pl.CreatedAt.Format("2006-01-02 15:04")})
	}
	a.table([]string{"PLAN", "ENVIRONMENT", "STATUS", "ACTIONS", "MOVES", "CREATED"}, rows)
	return nil
}

func (a *App) show(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	plan, err := c.Plan(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(plan)
	}
	a.printPlan(plan)
	return nil
}

func (a *App) printPlan(p *client.Plan) { a.printPlanWith(p, "") }

// printPlanWith prints a plan with a promotion's soak line, when there is
// one, after what it deploys.
func (a *App) printPlanWith(p *client.Plan, soak string) {
	a.printf("Plan %s for %s: %s\n", p.ID, p.Environment, p.Status)
	if line := kindLine(p); line != "" {
		a.printf("%s\n", line)
	}
	if p.Error != "" {
		a.printf("  %s\n", p.Error)
	}
	for _, s := range p.Sources {
		state := "moves to"
		if !s.InScope {
			state = "held at"
		}
		a.printf("  %s (%s) %s %s\n", s.Alias, s.Repository, state, short(s.CommitSHA))
	}
	if len(p.Actions) > 0 {
		a.printf("\n")
		rows := make([][]string, 0, len(p.Actions))
		for _, x := range p.Actions {
			rows = append(rows, []string{fmt.Sprint(x.Step), x.Op, x.Name, x.Provider, orDash(x.Reason)})
		}
		a.table([]string{"STEP", "OP", "NAME", "PROVIDER", "WHY"}, rows)
	}
	if p.Release != nil {
		a.printf("\nRelease: %s\n", releaseLine(p.Release))
	}
	if len(p.Artifacts) > 0 {
		a.printf("\nImages:\n")
		for _, x := range p.Artifacts {
			a.printf("  %s\n", artifactLine(x))
		}
	}
	if soak != "" {
		a.printf("\n%s\n", soak)
	}
	for _, l := range p.Leftovers {
		a.printf("\n%s\n", leftoverLine(l))
	}
	for _, f := range p.Problems {
		a.printf("\nProblem (%s): %s", f.Code, f.Message)
	}
	for _, f := range p.Warnings {
		a.printf("\nWarning (%s): %s", f.Code, f.Message)
	}
	if len(p.Problems)+len(p.Warnings) > 0 {
		a.printf("\n")
	}
	switch {
	case p.Status == "ready" && p.Executable:
		a.printf("\nApprove it with: loomctl approve %s\n", p.ID)
	case p.Status == "ready":
		a.printf("\nIt's for review only: something in it has no adapter yet.\n")
	}
}

func (a *App) approve(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	plan, err := c.Plan(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	if plan.Status != "ready" {
		return fmt.Errorf("plan %s is %s, so there's nothing to approve", plan.ID, plan.Status)
	}
	a.printPlan(plan)
	if !ctx.Bool("yes") {
		ok, cerr := ctx.Confirm(fmt.Sprintf("Deploy this to %s?", plan.Environment))
		if cerr != nil {
			return fmt.Errorf("pass --yes to approve without asking: %w", cerr)
		}
		if !ok {
			a.printf("Nothing approved.\n")
			return nil
		}
	}
	// The revision is the one just shown, so a plan that changed in
	// between comes back stale instead of approved.
	plan, err = c.Approve(ctxOf(ctx), plan.ID, plan.Revision, ctx.String("note"))
	if err != nil {
		return err
	}
	if plan.Status != "approved" {
		return fmt.Errorf("the plan is %s: what's deployed changed since it was made; plan again", plan.Status)
	}
	a.printf("Approved.\n")
	if ctx.Bool("no-wait") {
		return nil
	}
	d, err := a.deploymentOf(ctx, c, plan.ID)
	if err != nil {
		return err
	}
	return a.follow(ctx, c, d.ID)
}

// deploymentOf waits for the deployment an approved plan starts.
func (a *App) deploymentOf(ctx cli.CommandContext, c *client.Client, planID string) (*client.Deployment, error) {
	for {
		d, err := c.DeploymentForPlan(ctxOf(ctx), planID)
		if err == nil {
			return d, nil
		}
		if !errors.Is(err, client.ErrNotFound) {
			return nil, err
		}
		if _, werr := c.WaitPlan(ctxOf(ctx), planID); werr != nil {
			return nil, werr
		}
		select {
		case <-ctxOf(ctx).Done():
			return nil, ctxOf(ctx).Err()
		case <-afterEvery():
		}
	}
}

func (a *App) reject(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	plan, err := c.Reject(ctxOf(ctx), ctx.Arg(0), ctx.String("note"))
	if err != nil {
		return err
	}
	a.printf("Plan %s is %s.\n", plan.ID, plan.Status)
	return nil
}

func (a *App) deployments(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	list, err := c.Deployments(ctxOf(ctx), p.ID)
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(list)
	}
	rows := make([][]string, 0, len(list))
	for _, d := range list {
		rows = append(rows, []string{d.ID, d.Environment, d.Status, short(d.CommitSHA), d.StartedAt.Format("2006-01-02 15:04"), orDash(d.Error)})
	}
	a.table([]string{"DEPLOYMENT", "ENVIRONMENT", "STATUS", "COMMIT", "STARTED", "ERROR"}, rows)
	return nil
}

func (a *App) deployment(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	if ctx.Bool("watch") {
		return a.follow(ctx, c, ctx.Arg(0))
	}
	d, err := c.Deployment(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(d)
	}
	a.printDeployment(ctx, c, d)
	return nil
}

// follow prints each step's progress until the deployment finishes, and
// fails if it did.
func (a *App) follow(ctx cli.CommandContext, c *client.Client, id string) error {
	seen := map[string]string{}
	judged := map[string]bool{}
	d, err := c.WaitDeployment(ctxOf(ctx), id, func(d *client.Deployment) {
		for _, s := range d.Steps {
			line := s.Status + " " + s.Message
			if seen[s.ActionID] != line {
				seen[s.ActionID] = line
				a.printf("%-10s %-24s %s\n", s.Status, s.Name, s.Message)
			}
		}
		if d.Canary == nil {
			return
		}
		// A verdict prints when it first appears, not on every poll. A
		// failed read is tried again on the next one.
		report, err := c.Canary(ctxOf(ctx), d.ID)
		if err != nil {
			return
		}
		for _, v := range report.Verdicts {
			if !judged[v.ID] {
				judged[v.ID] = true
				a.printf("%-10s %-24s %s\n", "verdict", fmt.Sprintf("%s at %d%%", v.Service, v.Weight), v.Verdict+": "+v.Reason)
			}
		}
	})
	if err != nil {
		return err
	}
	a.printf("\n")
	a.printDeployment(ctx, c, d)
	if d.Paused() {
		a.printWait(d)
		return nil
	}
	if d.Status == "failed" {
		return fmt.Errorf("the deployment failed; retry it from where it stopped with `loomctl retry %s`", d.ID)
	}
	return nil
}

func (a *App) printDeployment(ctx cli.CommandContext, c *client.Client, d *client.Deployment) {
	a.printf("Deployment %s to %s: %s\n", d.ID, d.Environment, d.Status)
	if d.Error != "" {
		a.printf("  %s\n", d.Error)
	}
	rows := make([][]string, 0, len(d.Steps))
	for _, s := range d.Steps {
		rows = append(rows, []string{s.Name, s.Op, s.Status, orDash(s.URL), orDash(s.Message)})
	}
	a.table([]string{"NAME", "OP", "STATUS", "URL", "MESSAGE"}, rows)
	if d.Canary != nil {
		a.printCanary(ctx, c, d)
	}
	a.printWatch(ctx, c, d)
}

// releaseLine says how a plan rolls out. A canary that no service in the
// plan can ramp says so; the plan's warnings say why.
func releaseLine(r *client.Release) string {
	if r.Strategy != "canary" {
		return r.Strategy
	}
	steps := make([]string, 0, len(r.Steps))
	for _, w := range r.Steps {
		steps = append(steps, fmt.Sprintf("%d%%", w))
	}
	line := "canary at " + strings.Join(steps, ", ")
	if r.HoldSeconds > 0 {
		line += ", holding " + holdText(time.Duration(r.HoldSeconds)*time.Second) + " at each step"
	}
	if len(r.Canary) == 0 {
		line += " (no service in this plan can ramp)"
	}
	return line
}

// holdText is a hold as 10m or 1h30m, not 10m0s.
func holdText(d time.Duration) string {
	if d%time.Minute != 0 {
		return d.String()
	}
	if m := int(d / time.Minute); m%60 == 0 {
		return fmt.Sprintf("%dh", m/60)
	} else if m > 60 {
		return fmt.Sprintf("%dh%dm", m/60, m%60)
	}
	return fmt.Sprintf("%dm", int(d/time.Minute))
}

// printCanary prints the canary block: where it is, what a person has to
// do when it ended with traffic that may still be split, and the verdicts
// of the deployment's current retry.
func (a *App) printCanary(ctx cli.CommandContext, c *client.Client, d *client.Deployment) {
	cn := d.Canary
	a.printf("\nCanary: %s at %d%%\n", strings.ReplaceAll(cn.Status, "_", " "), cn.Weight)
	if cn.NeedsAttention() {
		a.printf("  NEEDS YOU: %s\n", orDefault(cn.Reason, "Traffic may still be split."))
		a.printf("  Check how the service splits traffic in the cloud before you retry.\n")
	} else if cn.Reason != "" {
		a.printf("  %s\n", cn.Reason)
	}
	report, err := c.Canary(ctxOf(ctx), d.ID)
	if err != nil {
		a.printf("  The verdicts couldn't be read: %v\n", err)
		return
	}
	if len(report.Verdicts) == 0 {
		return
	}
	rows := make([][]string, 0, len(report.Verdicts))
	for _, v := range report.Verdicts {
		rows = append(rows, []string{fmt.Sprint(v.Step + 1), fmt.Sprintf("%d%%", v.Weight), v.Service, v.Verdict, errorsText(v.Candidate), latencyText(v.Candidate), orDash(v.Reason)})
	}
	a.printf("\n")
	a.table([]string{"STEP", "WEIGHT", "SERVICE", "VERDICT", "ERRORS", "LATENCY", "REASON"}, rows)
}

func errorsText(h client.Health) string {
	if !h.Available {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", h.ErrorRate()*100)
}

func latencyText(h client.Health) string {
	if !h.Available {
		return "-"
	}
	return strings.TrimSpace(fmt.Sprintf("%.0fms %s", h.LatencyMS, h.Statistic))
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func (a *App) retry(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	d, err := c.Retry(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	a.printf("Retrying %s.\n", d.ID)
	if ctx.Bool("no-wait") {
		return nil
	}
	return a.follow(ctx, c, d.ID)
}

func (a *App) status(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	envs, err := c.Environments(ctxOf(ctx), p.ID)
	if err != nil {
		return err
	}
	all := map[string][]client.Binding{}
	var rows [][]string
	for _, e := range envs {
		bindings, berr := c.Bindings(ctxOf(ctx), p.ID, e.Name)
		if berr != nil {
			return berr
		}
		all[e.Name] = bindings
		for _, b := range bindings {
			drift := "in sync"
			if b.Drift != "" {
				drift = b.Drift
			}
			rows = append(rows, []string{e.Name, b.Name, b.Provider, orDash(b.URL), short(b.CommitSHA), drift})
		}
	}
	if ctx.Bool("json") {
		return a.json(all)
	}
	if len(rows) == 0 {
		a.printf("Nothing of %s is deployed yet.\n", p.Name)
		return nil
	}
	a.table([]string{"ENVIRONMENT", "NAME", "PROVIDER", "URL", "COMMIT", "DRIFT"}, rows)
	return nil
}

func (a *App) vars(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	list, err := c.Variables(ctxOf(ctx), p.ID)
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(list)
	}
	rows := make([][]string, 0, len(list))
	for _, v := range list {
		value := v.Value
		if v.Secret {
			value = "(secret)"
		}
		env := v.Environment
		if env == "" {
			env = "every"
		}
		rows = append(rows, []string{v.Key, value, env, orDash(v.Service)})
	}
	a.table([]string{"KEY", "VALUE", "ENVIRONMENT", "SERVICE"}, rows)
	return nil
}

func (a *App) setVar(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	key, value, ok := strings.Cut(ctx.Arg(1), "=")
	if !ok || key == "" {
		return errors.New("give the variable as KEY=VALUE, or KEY=- to read the value from standard input")
	}
	if value == "-" {
		data, rerr := readAll(a.opts.In)
		if rerr != nil {
			return rerr
		}
		value = strings.TrimRight(string(data), "\r\n")
	}
	v, err := c.SetVariable(ctxOf(ctx), p.ID, client.NewVariable{
		Key: key, Value: value, Secret: ctx.Bool("secret"), Environment: ctx.String("env"), Service: ctx.String("service"),
	})
	if err != nil {
		return err
	}
	where := "every environment"
	if v.Environment != "" {
		where = v.Environment
	}
	a.printf("Set %s for %s.\n", v.Key, where)
	return nil
}
