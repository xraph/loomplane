package ctl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
)

func (a *App) agentReportsCommand() cli.Command {
	return cli.NewCommand("reports", "List what the agent's watcher found", a.agentReports,
		cli.WithUsage("agent reports [--all | --status resolved|closed] [--project <ref>] [--json]"),
		cli.WithFlags(
			cli.NewBoolFlag("all", "", "Include the reports that are resolved or closed", false),
			cli.NewStringFlag("status", "", "Only reports in this state: open, resolved or closed", ""),
			cli.NewStringFlag("project", "", "Only this project's reports: its slug, name or ID", ""),
			jsonFlag),
		cli.WithSubcommand(cli.NewCommand("show", "Show one report: its facts, the agent's findings and its issue", a.agentReportShow,
			cli.WithUsage("agent reports show <id> [--json]"),
			cli.WithFlag(jsonFlag))),
		cli.WithSubcommand(cli.NewCommand("close", "Close a report", a.agentReportClose,
			cli.WithUsage("agent reports close <id> [--reason ...]"),
			cli.WithFlag(cli.NewStringFlag("reason", "", "Why, for the record", "")))),
	)
}

func (a *App) agentWatcherCommand() cli.Command {
	return cli.NewCommand("watcher", "Say whether the agent's watcher is watching, and whether it can open GitHub issues", a.agentWatcher,
		cli.WithUsage("agent watcher [--json]"),
		cli.WithFlag(jsonFlag))
}

func (a *App) agentReports(ctx cli.CommandContext) error {
	status := ctx.String("status")
	switch status {
	case "", "open", "resolved", "closed":
	default:
		return errors.New("--status is open, resolved or closed")
	}
	if status != "" && ctx.Bool("all") {
		return errors.New("use --all or --status, not both")
	}
	if ctx.Bool("all") {
		status = "all"
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var projectID string
	if ref := ctx.String("project"); ref != "" {
		p, perr := c.FindProject(ctxOf(ctx), ref)
		if perr != nil {
			return perr
		}
		projectID = p.ID
	}
	list, err := c.AgentReports(ctxOf(ctx), status, projectID)
	if err != nil {
		return agentError(err)
	}
	if ctx.Bool("json") {
		if list == nil {
			list = []client.AgentReport{}
		}
		return a.json(list)
	}
	if len(list) == 0 {
		a.printf("There are no reports.\n")
		return nil
	}
	names, err := projectSlugs(ctxOf(ctx), c)
	if err != nil {
		return err
	}
	now := time.Now()
	table := make([][]string, 0, len(list))
	for _, r := range list {
		table = append(table, []string{
			oneLine(r.ID, 40), oneLine(r.Severity, 10), names(r.ProjectID), orDash(oneLine(r.Environment, 30)),
			oneLine(r.Title, 70), seen(now, r), issueCell(r.Issue),
		})
	}
	a.table([]string{"ID", "SEVERITY", "PROJECT", "ENVIRONMENT", "TITLE", "SEEN", "ISSUE"}, table)
	return nil
}

// projectSlugs returns a function that names a project by its slug.
func projectSlugs(ctx context.Context, c *client.Client) (func(string) string, error) {
	ps, err := c.Projects(ctx)
	if err != nil {
		return nil, err
	}
	slugs := make(map[string]string, len(ps))
	for _, p := range ps {
		slugs[p.ID] = p.Slug
	}
	return func(id string) string {
		if id == "" {
			return "-"
		}
		if s, ok := slugs[id]; ok {
			return oneLine(s, 40)
		}
		return oneLine(id, 40)
	}, nil
}

// seen is how often a report has fired and how long ago it last did.
func seen(now time.Time, r client.AgentReport) string {
	return fmt.Sprintf("%dx, %s", r.Occurrences, age(now, r.LastSeenAt))
}

// issueCell is the report's GitHub issue in a table.
func issueCell(i client.AgentReportIssue) string {
	switch i.State {
	case "", "none":
		return "-"
	case "open", "closed":
		if i.Number > 0 {
			return fmt.Sprintf("#%d (%s)", i.Number, i.State)
		}
	}
	return issueState(i.State)
}

func issueState(s string) string {
	switch s {
	case "waiting_for_permission":
		return "waiting for permission"
	case "no_repository":
		return "no repository"
	}
	return oneLine(s, 30)
}

func (a *App) agentReportShow(ctx cli.CommandContext) error {
	id := ctx.Arg(0)
	if id == "" {
		return errors.New("name the report: its ID, from `loomctl agent reports`")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	r, err := c.AgentReport(ctxOf(ctx), id)
	if err != nil {
		return agentError(err)
	}
	if ctx.Bool("json") {
		return a.json(r)
	}
	names, err := projectSlugs(ctxOf(ctx), c)
	if err != nil {
		return err
	}
	a.printf("%s\n", oneLine(r.Title, 200))
	a.printf("%s, %s, %s\n", oneLine(r.ID, 40), oneLine(r.Status, 20), oneLine(r.Severity, 20))
	a.printf("Project:      %s\n", names(r.ProjectID))
	if r.Environment != "" {
		a.printf("Environment:  %s\n", oneLine(r.Environment, 60))
	}
	a.printf("Signal:       %s\n", oneLine(r.Signal, 60))
	if r.Repository != "" {
		a.printf("Repository:   %s\n", oneLine(r.Repository, 120))
	}
	now := time.Now()
	a.printf("Seen:         %d time(s), first %s ago, last %s ago\n", r.Occurrences, age(now, r.FirstSeenAt), age(now, r.LastSeenAt))
	if r.Status == "closed" && r.Resolution != "" {
		a.printf("Closed:       %s\n", oneLine(r.Resolution, 300))
	}
	if len(r.Facts) > 0 {
		a.printf("\nFacts\n")
		for _, f := range r.Facts {
			a.printf("  %s: %s\n", oneLine(f.Label, 60), oneLine(f.Value, 400))
		}
	}
	a.printf("\nInvestigation: %s\n", oneLine(r.Investigation, 30))
	if r.InvestigationNote != "" {
		a.printf("  %s\n", oneLine(r.InvestigationNote, 400))
	}
	if f := r.Findings; f != nil {
		a.printf("\nFindings (%s confidence)\n", oneLine(f.Confidence, 20))
		a.printf("  %s\n", oneLine(f.Summary, 2000))
		if f.LikelyCause != "" {
			a.printf("\nLikely cause\n  %s\n", oneLine(f.LikelyCause, 2000))
		}
		if len(f.Suggestions) > 0 {
			a.printf("\nSuggestions\n")
			for _, s := range f.Suggestions {
				a.printf("  - %s\n", oneLine(s, 1000))
			}
		}
	}
	a.printf("\nIssue: %s\n", issueLine(r.Issue))
	return nil
}

func issueLine(i client.AgentReportIssue) string {
	var b strings.Builder
	switch {
	case i.URL != "":
		b.WriteString(oneLine(i.URL, 300))
		if i.State != "" {
			b.WriteString(" (" + oneLine(i.State, 30) + ")")
		}
	case i.State == "" || i.State == "none":
		b.WriteString("none")
	default:
		b.WriteString(issueState(i.State))
	}
	if i.Error != "" {
		b.WriteString(": " + oneLine(i.Error, 300))
	}
	return b.String()
}

func (a *App) agentReportClose(ctx cli.CommandContext) error {
	id := ctx.Arg(0)
	if id == "" {
		return errors.New("name the report: its ID, from `loomctl agent reports`")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	r, err := c.CloseAgentReport(ctxOf(ctx), id, strings.TrimSpace(ctx.String("reason")))
	if err != nil {
		return agentError(err)
	}
	a.printf("Closed %s.\n", oneLine(r.ID, 40))
	return nil
}

func (a *App) agentWatcher(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	w, err := c.AgentWatcher(ctxOf(ctx))
	if err != nil {
		return agentError(err)
	}
	if ctx.Bool("json") {
		return a.json(w)
	}
	now := time.Now()
	if w.Paused {
		line := "The watcher is paused"
		if w.PausedReason != "" {
			line += ": " + oneLine(w.PausedReason, 300)
		} else {
			line += "."
		}
		a.printf("%s\n", line)
		if w.PausedAt != nil {
			a.printf("Paused %s ago.\n", age(now, *w.PausedAt))
		}
	} else {
		a.printf("The watcher is watching.\n")
	}
	if w.TickedAt != nil {
		a.printf("Last checked %s ago.\n", age(now, *w.TickedAt))
	}
	switch {
	case !w.Issues.Configured:
		a.printf("\nGitHub issues: off. Loomplane isn't set up to open issues, so reports stay here and in the dashboard.\n")
	case len(w.Issues.Installations) == 0:
		a.printf("\nGitHub issues: waiting. No GitHub installation is connected, so no issue can be opened yet.\n")
	default:
		a.printf("\nGitHub issues\n")
		rows := make([][]string, 0, len(w.Issues.Installations))
		for _, i := range w.Issues.Installations {
			state := "can open issues"
			if !i.Accepted {
				state = "waiting for you to accept the Issues permission"
				if i.ManageURL != "" {
					state += " at " + oneLine(i.ManageURL, 200)
				}
			}
			rows = append(rows, []string{oneLine(i.Account, 60), oneLine(i.AccountType, 30), state, age(now, i.CheckedAt)})
		}
		a.table([]string{"ACCOUNT", "TYPE", "STATE", "CHECKED"}, rows)
	}
	return nil
}
