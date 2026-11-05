package ctl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
)

// releasePoll is how often `release prepare --wait` reads the report.
var releasePoll = 3 * time.Second

func (a *App) releaseCommand() cli.Command {
	return cli.NewCommand("release", "Prepare a production release and read its readiness report", nil,
		cli.WithUsage("release prepare|report|list|cancel"),
		cli.WithSubcommand(cli.NewCommand("prepare", "Ask for a release: Loomplane plans it, collects evidence and has the agent review it", a.releasePrepare,
			cli.WithUsage("release prepare <project> [--from staging] [--to production] [--scope api,web] [--replace] [--wait] [--json]"),
			cli.WithFlags(
				cli.NewStringFlag("from", "", "The staging environment; the project's only one if left out", ""),
				cli.NewStringFlag("to", "", "The production environment; the project's only one if left out", ""),
				cli.NewStringSliceFlag("scope", "", "A source to release, by alias; every one if none", nil),
				cli.NewBoolFlag("replace", "", "Replace the production plan someone else has open", false),
				cli.NewBoolFlag("wait", "", "Follow the preparation until it finishes or waits for a person, then print the report", false),
				jsonFlag))),
		cli.WithSubcommand(cli.NewCommand("report", "Show a release report: the seven gates, what changes and what the agent noted", a.releaseReport,
			cli.WithUsage("release report <id> [--json]"), cli.WithFlag(jsonFlag))),
		cli.WithSubcommand(cli.NewCommand("list", "List a project's release reports", a.releaseList,
			cli.WithUsage("release list <project> [--json]"), cli.WithFlag(jsonFlag))),
		cli.WithSubcommand(cli.NewCommand("cancel", "Stop a release preparation that's still going", a.releaseCancel,
			cli.WithUsage("release cancel <id>"))),
	)
}

// releaseError says the agent is off when the server has no release routes,
// and says so plainly for a report that isn't there. Other answers pass.
func releaseError(err error) error {
	var e *client.Error
	if !errors.As(err, &e) || e.Status != http.StatusNotFound {
		return err
	}
	msg := strings.ToLower(e.Message)
	switch {
	case strings.Contains(msg, "release report not found"):
		return sentence("There's no release report with that ID.")
	case strings.Contains(msg, "project"):
		return err
	}
	return sentence(agentOff)
}

// quoted prints text that came from outside Loomplane (commit titles,
// authors, file paths, the agent's notes) as a quoted string on one line,
// so it can't pass for Loomplane's own words or move the cursor.
func quoted(s string, n int) string { return strconv.Quote(oneLine(s, n)) }

func releaseReportID(ctx cli.CommandContext) (string, error) {
	id := ctx.Arg(0)
	if id == "" {
		return "", errors.New("name the release report: its ID, from `loomctl release list <project>`")
	}
	return id, nil
}

func (a *App) releasePrepare(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	r, err := c.PrepareRelease(ctxOf(ctx), p.ID, client.PrepareRelease{
		From: ctx.String("from"), To: ctx.String("to"), Scope: ctx.StringSlice("scope"), Replace: ctx.Bool("replace"),
	})
	if err != nil {
		return preparingError(err)
	}
	if ctx.Bool("wait") {
		if r, err = a.followRelease(ctxOf(ctx), c, r, !ctx.Bool("json")); err != nil {
			return err
		}
	}
	if ctx.Bool("json") {
		return a.json(r)
	}
	if !ctx.Bool("wait") {
		a.printf("Preparing release %s: %s to %s\n", oneLine(r.ID, 60), oneLine(r.FromEnvironment, 40), oneLine(r.ToEnvironment, 40))
		a.printf("Report: /release-reports/%s\n", oneLine(r.ID, 60))
		a.printf("Read it with: loomctl release report %s\n", oneLine(r.ID, 60))
		return nil
	}
	a.printReleaseReport(r)
	return nil
}

// preparingError adds the way forward to a 409: an open plan can be
// replaced; a preparation already running can only be read or cancelled.
// Any other 409, such as the agent off for the workspace, passes as it is.
func preparingError(err error) error {
	var e *client.Error
	if !errors.As(err, &e) || e.Status != http.StatusConflict {
		return releaseError(err)
	}
	msg := oneLine(e.Message, 600)
	switch lower := strings.ToLower(e.Message); {
	case strings.Contains(lower, "already being prepared"):
		return sentence(msg + "\nRead it with `loomctl release list <project>`, or stop it with `loomctl release cancel <id>`.")
	case strings.Contains(lower, "waiting for a decision"):
		return sentence(msg + "\nTo replace that plan with this release, run the command again with --replace.")
	}
	return err
}

// releaseSettled is a state to stop waiting at: a final one, or the one
// where a person decides.
func releaseSettled(state string) bool {
	switch state {
	case "planning", "reviewing", "promoting":
		return false
	}
	return true
}

// followRelease reads the report every few seconds until it's settled.
func (a *App) followRelease(ctx context.Context, c *client.Client, r *client.ReleaseReport, say bool) (*client.ReleaseReport, error) {
	last := ""
	for {
		if r.State != last && say {
			a.printf("%s: %s\n", oneLine(r.ID, 60), oneLine(r.State, 40))
			last = r.State
		}
		if releaseSettled(r.State) {
			return r, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(releasePoll):
		}
		next, err := c.ReleaseReport(ctx, r.ID)
		if err != nil {
			return nil, releaseError(err)
		}
		r = next
	}
}

func (a *App) releaseReport(ctx cli.CommandContext) error {
	id, err := releaseReportID(ctx)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	r, err := c.ReleaseReport(ctxOf(ctx), id)
	if err != nil {
		return releaseError(err)
	}
	if ctx.Bool("json") {
		return a.json(r)
	}
	a.printReleaseReport(r)
	return nil
}

func (a *App) releaseList(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	list, err := c.ReleaseReports(ctxOf(ctx), p.ID, 0)
	if err != nil {
		return releaseError(err)
	}
	if ctx.Bool("json") {
		if list == nil {
			list = []client.ReleaseReport{}
		}
		return a.json(list)
	}
	if len(list) == 0 {
		a.printf("No release has been prepared for this project.\n")
		return nil
	}
	now := time.Now()
	rows := make([][]string, 0, len(list))
	for _, r := range list {
		rows = append(rows, []string{
			oneLine(r.ID, 40), oneLine(r.FromEnvironment, 30) + " -> " + oneLine(r.ToEnvironment, 30), oneLine(r.State, 30),
			gateTally(r), age(now, r.CreatedAt),
		})
	}
	a.table([]string{"ID", "RELEASE", "STATE", "GATES", "AGE"}, rows)
	return nil
}

func (a *App) releaseCancel(ctx cli.CommandContext) error {
	id, err := releaseReportID(ctx)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	r, err := c.CancelReleaseReport(ctxOf(ctx), id)
	if err != nil {
		return releaseError(err)
	}
	a.printf("Stopped %s (%s). The plan, if there is one, waits for a person.\n", oneLine(r.ID, 60), oneLine(r.State, 40))
	return nil
}

// gateTally is how many gates held, or a dash before any were checked.
func gateTally(r client.ReleaseReport) string {
	if len(r.Gates) == 0 {
		return "-"
	}
	n := 0
	for _, g := range r.Gates {
		if g.Passed {
			n++
		}
	}
	return fmt.Sprintf("%d/%d", n, len(r.Gates))
}

// printReleaseReport prints what a person decides on. Loomplane's own
// words (state, gates and their details) are plain; everything that came
// from outside is quoted.
func (a *App) printReleaseReport(r *client.ReleaseReport) {
	a.printf("Release %s: %s to %s\n", oneLine(r.ID, 60), oneLine(r.FromEnvironment, 40), oneLine(r.ToEnvironment, 40))
	a.printf("State:    %s\n", oneLine(r.State, 40))
	if r.StateReason != "" {
		a.printf("Reason:   %s\n", oneLine(r.StateReason, 400))
	}
	if r.Error != "" {
		a.printf("Error:    %s\n", oneLine(r.Error, 400))
	}
	if len(r.Scope) > 0 {
		a.printf("Scope:    %s\n", oneLine(strings.Join(r.Scope, ", "), 200))
	}
	if r.PlanID != "" {
		note := ""
		if r.ReusedPlan {
			note = ", a plan someone else had open"
		}
		a.printf("Plan:     %s (revision %s%s)\n", oneLine(r.PlanID, 60), oneLine(r.PlanRevision, 20), note)
	}
	if r.Via != "" {
		a.printf("Asked:    from %s\n", oneLine(r.Via, 20))
	}
	if len(r.Gates) > 0 {
		head := "\nThe seven gates"
		if r.Preview {
			head += " (a preview: the agent's own check comes when it decides)"
		}
		a.printf("%s\n", head)
		for _, g := range r.Gates {
			verdict := "pass"
			if !g.Passed {
				verdict = "FAIL"
			}
			a.printf("  gate %d  %-28s  %s  %s\n", g.Number, oneLine(g.Name, 28), verdict, oneLine(g.Detail, 400))
		}
	}
	switch r.Decision {
	case "promote":
		a.printf("Decision: every gate holds, so the agent may promote it\n")
	case "ask":
		a.printf("Decision: a person decides\n")
	}
	if ev, err := r.ReadinessEvidence(); err != nil {
		a.printf("\nThe evidence in this report couldn't be read.\n")
	} else if ev != nil {
		a.printEvidence(ev)
	}
	if len(r.Concerns) > 0 {
		a.printf("\nFrom the agent (its words, not Loomplane's)\n")
		for _, k := range r.Concerns {
			a.printf("  %s: %s\n", oneLine(k.Kind, 10), quoted(k.Text, 2000))
		}
	}
	// A stopped review leaves the plan waiting for a person too.
	if (r.State == "waiting_for_person" || r.State == "stopped") && r.PlanID != "" && r.PlanRevision != "" {
		a.printf("\napprove with: loomctl approve %s --revision %s\n", oneLine(r.PlanID, 80), oneLine(r.PlanRevision, 128))
	}
}

func (a *App) printEvidence(ev *client.ReleaseEvidence) {
	for _, s := range ev.Sources {
		a.printf("\nSource %s (%s)\n", quoted(s.Alias, 60), quoted(s.Repository, 120))
		line := fmt.Sprintf("  %s, %d commit(s), %d file(s)", oneLine(s.Direction, 20), s.TotalCommits, s.FilesChanged)
		if s.ProductionCommit != "" || s.StagingCommit != "" {
			line += fmt.Sprintf(", production %s -> staging %s", orDash(short(oneLine(s.ProductionCommit, 64))), orDash(short(oneLine(s.StagingCommit, 64))))
		}
		a.printf("%s\n", line)
		if s.Truncated || s.Error != "" {
			a.printf("  not read in full: %s\n", quoted(s.Error, 300))
		}
		for _, cm := range s.Commits {
			a.printf("  commit %s %s by %s\n", oneLine(short(cm.SHA), 12), quoted(cm.Title, 120), quoted(cm.Author, 60))
		}
		if len(s.Migrations) == 0 {
			a.printf("  migrations: none\n")
		}
		for _, m := range s.Migrations {
			a.printf("  migration %s (%s, +%d -%d)\n", quoted(m.Path, 200), oneLine(m.Status, 20), m.Additions, m.Deletions)
		}
	}
	if sk := ev.Soak; sk != nil {
		switch {
		case !sk.Running:
			a.printf("\nSoak: staging isn't running this release\n")
		default:
			a.printf("\nSoak: staging has run it for %s, %s\n", sk.Duration.Round(time.Minute), soakWord(sk))
		}
	}
	if len(ev.Canaries) == 0 {
		a.printf("Canaries: none ran in production for these services\n")
	}
	for _, cn := range ev.Canaries {
		a.printf("Canary %s: %s for %s\n", oneLine(cn.DeploymentID, 60), oneLine(cn.Status, 30), quoted(strings.Join(cn.Services, ", "), 200))
	}
	if cv := ev.Coverage; cv != nil {
		w := "no watch would be armed"
		if cv.Duration > 0 {
			w = "a watch of " + cv.Duration.Round(time.Minute).String()
		}
		a.printf("Coverage: %s; canary on %s", w, quoted(strings.Join(cv.Canary, ", "), 200))
		if cv.Unverified {
			a.printf("; it would leave evidence missing")
		}
		a.printf("\n")
	}
	for _, ar := range ev.Artifacts {
		how := "rebuilt"
		if ar.Reused {
			how = "reused"
		}
		a.printf("Artifact %s: %s\n", quoted(ar.Service, 80), how)
	}
	for _, o := range ev.OpenReports {
		a.printf("Open on staging: %s %s\n", oneLine(o.Severity, 12), quoted(o.Title, 120))
	}
	if b := ev.Budget; b != nil {
		a.printf("Budget: %s of %s USD spent", oneLine(b.SpentUSD, 20), oneLine(b.LimitUSD, 20))
		if b.Exhausted {
			a.printf(" (spent)")
		}
		a.printf("\n")
	}
	keys := make([]string, 0, len(ev.Unavailable))
	for k := range ev.Unavailable {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		a.printf("Couldn't read %s: %s\n", oneLine(k, 40), quoted(ev.Unavailable[k], 300))
	}
}

func soakWord(s *client.ReleaseSoak) string {
	switch {
	case s.Unverified:
		return "with too little evidence to judge"
	case s.Healthy:
		return "healthy"
	}
	return "not healthy"
}
