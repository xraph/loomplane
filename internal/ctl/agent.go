package ctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/xraph/forge/cli"
	"golang.org/x/term"

	"github.com/xraph/loomplane/pkg/client"
)

// agentOff is what loomctl says when the server has no agent routes.
const agentOff = "This Loomplane doesn't run the agent (ai.enabled is off)."

// maxSecretBytes is the most a provided value may be: the API's own limit.
const maxSecretBytes = 64 << 10

// agentCommand is `loomctl agent`. Its subcommands are agentSubcommands, so
// the agent's other commands join `requests` there.
func (a *App) agentCommand() cli.Command {
	opts := make([]cli.CommandOption, 0, 4)
	for _, sub := range a.agentSubcommands() {
		opts = append(opts, cli.WithSubcommand(sub))
	}
	opts = append(opts,
		cli.WithUsage("agent [--project <ref>] [--conversation <id>]"),
		cli.WithFlags(
			cli.NewStringFlag("project", "", "Scope a new conversation to this project: its slug, name or ID", ""),
			cli.NewStringFlag("conversation", "", "Carry on this conversation", "")))
	return cli.NewCommand("agent", "Talk to the workspace's built-in AI agent, or work with what it asks", a.agentChat, opts...)
}

func (a *App) agentSubcommands() []cli.Command {
	return []cli.Command{a.agentAskCommand(), a.agentConversationsCommand(), a.agentRequestsCommand(), a.agentReportsCommand(), a.agentWatcherCommand()}
}

func (a *App) agentRequestsCommand() cli.Command {
	return cli.NewCommand("requests", "List what the agent is asking people for", a.agentRequests,
		cli.WithUsage("agent requests [--all] [--mine] [--json]"),
		cli.WithFlags(
			cli.NewBoolFlag("all", "", "Include the requests that are settled", false),
			cli.NewBoolFlag("mine", "", "Only what's asked of you: your questions, your teams' and, for admins, the workspace's", false),
			jsonFlag),
		cli.WithSubcommand(cli.NewCommand("answer", "Answer a question the agent asked", a.agentAnswer,
			cli.WithUsage("agent requests answer <id> <text...>"))),
		cli.WithSubcommand(cli.NewCommand("provide", "Set the secret the agent asked for; the value comes from standard input", a.agentProvide,
			cli.WithUsage("agent requests provide <id>   (the value on standard input, never as an argument)"))),
		cli.WithSubcommand(cli.NewCommand("done", "Say you connected what the agent asked for", a.agentDone,
			cli.WithUsage("agent requests done <id>"))),
		cli.WithSubcommand(cli.NewCommand("decline", "Turn a request down; the agent sees why", a.agentDecline,
			cli.WithUsage("agent requests decline <id> [--reason ...]"),
			cli.WithFlag(cli.NewStringFlag("reason", "", "Why, for the agent and the record", "")))),
	)
}

// sentence is an error that reads as a sentence, capital and full stop
// included, because loomctl prints it as it stands.
type sentence string

func (s sentence) Error() string { return string(s) }

// agentError says the agent is off when the server has no such route. A 404
// on one request's, conversation's or task's ID is a different thing: that
// one isn't there.
func agentError(err error) error {
	var e *client.Error
	if !errors.As(err, &e) || e.Status != 404 {
		return err
	}
	msg := strings.ToLower(e.Message)
	if strings.Contains(msg, "agent request") {
		return sentence("There's no agent request with that ID.")
	}
	if strings.Contains(msg, "agent report") {
		return sentence("There's no agent report with that ID.")
	}
	if strings.Contains(msg, "conversation not found") || strings.Contains(msg, "agent task not found") {
		return err
	}
	return sentence(agentOff)
}

func (a *App) agentRequests(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	var opts []client.AgentRequestsOption
	if ctx.Bool("mine") {
		opts = append(opts, client.ForMe())
	}
	list, err := c.AgentRequests(ctxOf(ctx), ctx.Bool("all"), opts...)
	if err != nil {
		return agentError(err)
	}
	if ctx.Bool("json") {
		if list == nil {
			list = []client.AgentRequest{}
		}
		return a.json(list)
	}
	if len(list) == 0 {
		a.printf("The agent isn't waiting on anyone.\n")
		return nil
	}
	project, person, err := agentNames(ctxOf(ctx), c, list)
	if err != nil {
		return err
	}
	now := time.Now()
	table := make([][]string, 0, len(list))
	var approvals []string
	for _, r := range list {
		table = append(table, []string{
			r.ID, r.Kind, project(r.ProjectID), oneLine(r.Question, 60),
			sentTo(r), person(r.AskedFor), age(now, r.CreatedAt),
		})
		if r.Kind == "approve_plan" && r.Status == "pending" && r.Target.PlanID != "" {
			if line := approveLine(ctxOf(ctx), c, r); line != "" {
				approvals = append(approvals, line)
			}
		}
	}
	a.table([]string{"ID", "KIND", "PROJECT", "QUESTION", "SENT TO", "ASKED FOR", "AGE"}, table)
	if len(approvals) > 0 {
		a.printf("\n%s\n", strings.Join(approvals, "\n"))
	}
	return nil
}

// approveLine is the command that approves r's plan, for pasting. Its
// revision is the one the plan has, read from the plan: a request whose
// plan changed since the agent asked gets a pointer to read it again
// instead, and one whose plan can't be read gets nothing.
func approveLine(ctx context.Context, c *client.Client, r client.AgentRequest) string {
	plan, err := c.Plan(ctx, r.Target.PlanID)
	if err != nil || plan.ID == "" {
		return ""
	}
	id := oneLine(plan.ID, 80)
	if plan.Revision == "" || plan.Revision != r.Target.PlanRevision {
		return fmt.Sprintf("%s changed since the agent asked; read it with: loomctl show %s", id, id)
	}
	return fmt.Sprintf("approve with: loomctl approve %s --revision %s", id, oneLine(plan.Revision, 128))
}

// agentNames names the projects and people a list of requests mentions. It
// reads each list only when a request needs it.
func agentNames(ctx context.Context, c *client.Client, list []client.AgentRequest) (project, person func(string) string, err error) {
	projects, people := map[string]string{}, map[string]string{}
	needProjects, needPeople := false, false
	for _, r := range list {
		needProjects = needProjects || r.ProjectID != ""
		needPeople = needPeople || r.AskedFor != ""
	}
	if needProjects {
		ps, perr := c.Projects(ctx)
		if perr != nil {
			return nil, nil, perr
		}
		for _, p := range ps {
			projects[p.ID] = p.Slug
		}
	}
	if needPeople {
		ms, merr := c.Members(ctx)
		if merr != nil {
			return nil, nil, merr
		}
		for _, m := range ms {
			people[m.UserID] = orDash(m.Name)
			if m.Name == "" {
				people[m.UserID] = m.Email
			}
		}
	}
	project = func(id string) string {
		if id == "" {
			return "-"
		}
		if n, ok := projects[id]; ok {
			return n
		}
		return id
	}
	person = func(id string) string {
		if id == "" {
			return "-"
		}
		if n, ok := people[id]; ok {
			return client.ActorName(n)
		}
		return client.ActorName(id)
	}
	return project, person, nil
}

// sentTo says who a request went to: the team the agent named, with its
// reason, the project's team, or the workspace admins.
func sentTo(r client.AgentRequest) string {
	switch r.RouteSource {
	case "named":
		if r.TeamReason != "" {
			return fmt.Sprintf("%s (%s)", oneLine(r.TeamName, 80), oneLine(r.TeamReason, 40))
		}
		return oneLine(r.TeamName, 80)
	case "project":
		return oneLine(r.TeamName, 80)
	case "admins":
		return "workspace admins"
	case "asker":
		return "the asker"
	}
	return "-"
}

// oneLine puts s on one line and cuts it to at most n characters. Control
// characters go first (tabs and newlines become spaces), so text the agent
// wrote can't drive the terminal: no escape sequences, no OSC-8 links.
func oneLine(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			return ' '
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// age is how long ago t was, in the largest whole unit.
func age(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case t.IsZero():
		return "-"
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func agentRequestID(ctx cli.CommandContext) (string, error) {
	id := ctx.Arg(0)
	if id == "" {
		return "", errors.New("name the request: its ID, from `loomctl agent requests`")
	}
	return id, nil
}

func (a *App) agentAnswer(ctx cli.CommandContext) error {
	id, err := agentRequestID(ctx)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(strings.Join(ctx.Args()[1:], " "))
	if text == "" {
		return errors.New("write your answer after the request's ID")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	r, err := c.AnswerAgentRequest(ctxOf(ctx), id, text)
	if err != nil {
		return agentError(err)
	}
	a.printf("Answered %s. The agent carries on.\n", r.ID)
	return nil
}

func (a *App) agentProvide(ctx cli.CommandContext) error {
	id, err := agentRequestID(ctx)
	if err != nil {
		return err
	}
	if len(ctx.Args()) > 1 {
		// Never echo the argument: it is the secret.
		return errors.New("the value goes on standard input, not the command line, so it stays out of your shell history: echo -n \"$VALUE\" | loomctl agent requests provide " + id)
	}
	value, err := a.readSecret()
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	r, err := c.ProvideAgentSecret(ctxOf(ctx), id, value)
	if err != nil {
		return agentError(err)
	}
	name := r.Target.Key
	if name == "" {
		name = "the secret"
	}
	a.printf("Saved %s as a secret. Loomplane doesn't show it again.\n", name)
	return nil
}

// readSecret reads a value from standard input. On a terminal it doesn't
// echo; piped, it takes everything up to one trailing newline.
func (a *App) readSecret() (string, error) {
	if f, ok := a.opts.In.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(os.Stderr, "Value (hidden): ")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("read the value: %w", err)
		}
		return checkSecret(string(b))
	}
	b, err := io.ReadAll(io.LimitReader(a.opts.In, maxSecretBytes+1))
	if err != nil {
		return "", fmt.Errorf("read the value: %w", err)
	}
	v := string(b)
	v = strings.TrimSuffix(v, "\n")
	v = strings.TrimSuffix(v, "\r")
	return checkSecret(v)
}

func checkSecret(v string) (string, error) {
	switch {
	case v == "":
		return "", errors.New("the value on standard input was empty")
	case len(v) > maxSecretBytes:
		return "", fmt.Errorf("the value is longer than %d bytes", maxSecretBytes)
	}
	return v, nil
}

func (a *App) agentDone(ctx cli.CommandContext) error {
	id, err := agentRequestID(ctx)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	r, err := c.CompleteAgentRequest(ctxOf(ctx), id)
	if err != nil {
		return agentError(err)
	}
	a.printf("Thanks. %s is %s.\n", r.ID, r.Status)
	return nil
}

func (a *App) agentDecline(ctx cli.CommandContext) error {
	id, err := agentRequestID(ctx)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	r, err := c.DeclineAgentRequest(ctxOf(ctx), id, strings.TrimSpace(ctx.String("reason")))
	if err != nil {
		return agentError(err)
	}
	a.printf("Declined %s.\n", r.ID)
	return nil
}
