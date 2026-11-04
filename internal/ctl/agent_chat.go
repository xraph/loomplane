package ctl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xraph/forge/cli"
	"golang.org/x/term"

	"github.com/xraph/loomplane/pkg/client"
)

// safetyStopped is the error of a task Loomplane's safety checks stopped,
// as the server words it (agentruns.SafetyStopped).
const safetyStopped = "Loomplane's safety checks stopped this answer."

// Exit codes of `loomctl agent ask`, besides 0 and 1.
const (
	exitWaiting     = 3
	exitInterrupted = 130
)

func (a *App) agentAskCommand() cli.Command {
	return cli.NewCommand("ask", "Ask the agent one thing and print its answer", a.agentAsk,
		cli.WithUsage(`agent ask "<prompt>" [--project <ref>] [--conversation <id>] [--json] [--no-wait]`),
		cli.WithFlags(
			cli.NewStringFlag("project", "", "Scope a new conversation to this project: its slug, name or ID", ""),
			cli.NewStringFlag("conversation", "", "Add to this conversation instead of starting one", ""),
			cli.NewBoolFlag("no-wait", "", "Exit with status 3 when the agent waits for a person instead of waiting with it", false),
			jsonFlag))
}

func (a *App) agentConversationsCommand() cli.Command {
	return cli.NewCommand("conversations", "List your conversations with the agent", a.agentConversations,
		cli.WithUsage("agent conversations [--project <ref>] [--json]"),
		cli.WithFlags(
			cli.NewStringFlag("project", "", "Only conversations about this project", ""),
			jsonFlag))
}

// ExitError helpers.

func exit(code int) error { return &ExitError{Code: code} }

// interrupts returns the channel that says Ctrl-C was pressed, and a func
// that stops listening.
func (a *App) interrupts() (pressed <-chan struct{}, stop func()) {
	if a.opts.Interrupt != nil {
		return a.opts.Interrupt, func() {}
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	out, done := make(chan struct{}, 1), make(chan struct{})
	go func() {
		for {
			select {
			case <-sig:
				select {
				case out <- struct{}{}:
				default:
				}
			case <-done:
				return
			}
		}
	}()
	return out, func() { signal.Stop(sig); close(done) }
}

// lineSource reads standard input a line at a time on a goroutine, so a
// wait for a line can end on Ctrl-C.
type lineSource struct {
	r    io.Reader
	once sync.Once
	ch   chan string
}

func (l *lineSource) start() {
	l.once.Do(func() {
		l.ch = make(chan string)
		go func() {
			defer close(l.ch)
			br := bufio.NewReader(l.r)
			for {
				line, err := br.ReadString('\n')
				if line != "" {
					l.ch <- strings.TrimRight(line, "\r\n")
				}
				if err != nil {
					return
				}
			}
		}()
	})
}

// next waits for a line. ok is false at the end of the input or when
// stop ends first.
func (l *lineSource) next(stop <-chan struct{}) (line string, ok bool) {
	l.start()
	select {
	case line, ok = <-l.ch:
		return line, ok
	case <-stop:
		return "", false
	}
}

// chat is one `loomctl agent` or `loomctl agent ask` run.
type chat struct {
	a      *App
	c      *client.Client
	ctx    context.Context
	base   string
	asJSON bool
	noWait bool
	// inline asks an answer request on the spot: the interactive chat.
	inline bool
	lines  *lineSource
	intr   <-chan struct{}

	// midLine is whether the agent's text has left stdout mid-line.
	midLine bool
	// safetySaid is whether the safety stop has been printed already.
	safetySaid bool
	// lastSeq is the last event of the task followed, for /follow.
	lastSeq int64
}

// errLeave ends a follow with the task still open: the person left a
// question pending. errWaiting does the same for --no-wait.
var (
	errLeave   = errors.New("left pending")
	errWaiting = errors.New("waiting for a person")
)

func (s *chat) errf(format string, args ...any) { fmt.Fprintf(s.a.opts.Err, format, args...) }

// endLine finishes the line of text on stdout, if one is open.
func (s *chat) endLine() {
	if s.midLine {
		fmt.Fprintln(s.a.opts.Out)
		s.midLine = false
	}
}

// cleanText drops the control characters of text the agent wrote, newlines
// and tabs apart, so it can't drive the terminal.
func cleanText(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			return -1
		}
		return r
	}, s)
}

func eventField(data json.RawMessage, key string) string {
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// handle prints one event.
func (s *chat) handle(ev client.AgentEvent) error {
	s.lastSeq = ev.Seq
	if s.asJSON {
		line, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		fmt.Fprintln(s.a.opts.Out, string(line))
	}
	out := s.a.opts.Out
	switch ev.Kind {
	case "token":
		if s.asJSON {
			return nil
		}
		text := cleanText(eventField(ev.Data, "text"))
		if text == "" {
			return nil
		}
		fmt.Fprint(out, text)
		s.midLine = !strings.HasSuffix(text, "\n")
	case "step":
		if !s.asJSON {
			s.endLine()
		}
	case "tool_call":
		if s.asJSON {
			return nil
		}
		s.endLine()
		name, risk := oneLine(eventField(ev.Data, "tool"), 80), oneLine(eventField(ev.Data, "risk"), 40)
		if risk != "" {
			s.errf("· %s (%s)\n", name, risk)
		} else {
			s.errf("· %s\n", name)
		}
	case "tool_result":
		if s.asJSON {
			return nil
		}
		msg := oneLine(eventField(ev.Data, "message"), 200)
		switch eventField(ev.Data, "outcome") {
		case "ok":
			s.errf("  ok\n")
		case "denied":
			s.errf("  refused: %s\n", orDash(msg))
		default:
			s.errf("  failed: %s\n", orDash(msg))
		}
	case "safety_block":
		if !s.asJSON {
			s.endLine()
		}
		s.errf("%s\n", safetyStopped)
		s.safetySaid = true
	case "request":
		return s.request(ev)
	case "status":
		if s.noWait && eventField(ev.Data, "status") == "waiting" {
			return errWaiting
		}
	}
	return nil
}

// request tells the person what the agent is asking, and how to answer.
// A replay of a request that's been settled says nothing.
func (s *chat) request(ev client.AgentEvent) error {
	id := eventField(ev.Data, "request_id")
	if id == "" {
		return nil
	}
	r, err := s.c.AgentRequest(s.ctx, id)
	if err != nil {
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}
		s.errf("The agent asked for something. See it with: loomctl agent requests\n")
		return s.waitIfAsked()
	}
	if r.Status != "pending" {
		return nil
	}
	if !s.asJSON {
		s.endLine()
	}
	question := cleanText(r.Question)
	if s.inline && !s.asJSON && r.Kind == "answer" {
		return s.askInline(r, question)
	}
	s.errf("agent asks: %s\n", question)
	for i, o := range r.Options {
		s.errf("  %d) %s\n", i+1, oneLine(o, 200))
	}
	switch r.Kind {
	case "answer":
		s.errf("Answer with: loomctl agent requests answer %s <text>\n", r.ID)
	case "provide_secret":
		s.errf("Provide it with: loomctl agent requests provide %s\n", r.ID)
	case "approve_plan":
		if line := approveLine(s.ctx, s.c, *r); line != "" {
			s.errf("%s\n", line)
		} else if r.Target.PlanID != "" {
			s.errf("approve with: loomctl approve %s --revision %s\n", oneLine(r.Target.PlanID, 80), oneLine(r.Target.PlanRevision, 128))
		}
	case "connect_github":
		s.errf("Install the GitHub app at %s/integrations, then run: loomctl agent requests done %s\n", s.base, r.ID)
	case "create_connection":
		s.errf("Connect the cloud account on the clouds page, %s/clouds, then run: loomctl agent requests done %s\n", s.base, r.ID)
	default:
		s.errf("See it with: loomctl agent requests\n")
	}
	return s.waitIfAsked()
}

func (s *chat) waitIfAsked() error {
	if s.noWait {
		return errWaiting
	}
	return nil
}

// askInline asks a question the agent put, at the prompt. An empty line
// leaves it pending.
func (s *chat) askInline(r *client.AgentRequest, question string) error {
	s.errf("agent asks: %s\n", question)
	for i, o := range r.Options {
		s.errf("  %d) %s\n", i+1, oneLine(o, 200))
	}
	s.prompt("answer> ")
	stop := s.ctx.Done()
	line, ok := s.lines.next(stop)
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	line = strings.TrimSpace(line)
	if !ok || line == "" {
		s.errf("Left it pending. Answer with: loomctl agent requests answer %s <text>\n", r.ID)
		return errLeave
	}
	if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(r.Options) {
		line = r.Options[n-1]
	}
	if _, err := s.c.AnswerAgentRequest(s.ctx, r.ID, line); err != nil {
		s.errf("%s\n", agentError(err))
		s.errf("Answer with: loomctl agent requests answer %s <text>\n", r.ID)
	}
	return nil
}

// prompt prints a prompt when a person is typing at a terminal.
func (s *chat) prompt(text string) {
	if f, ok := s.a.opts.In.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		s.errf("%s", text)
	}
}

// followResult is how following a task ended.
type followResult struct {
	end         client.StreamEnd
	ended       bool
	interrupted bool
	waiting     bool
	left        bool
}

// follow streams the task from after, printing as it goes. Ctrl-C cancels
// the task.
func (s *chat) follow(taskID string, after int64) (followResult, error) {
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	type done struct {
		end client.StreamEnd
		err error
	}
	// The handlers use the context that ends with the follow. Nothing
	// else touches the chat until the stream goroutine is done.
	parent := s.ctx
	s.ctx = ctx
	ch := make(chan done, 1)
	go func() {
		end, err := s.c.StreamAgentTask(ctx, taskID, after, s.handle)
		ch <- done{end, err}
	}()
	var d done
	select {
	case d = <-ch:
		s.ctx = parent
	case <-s.intr:
		cancel()
		<-ch
		s.ctx = parent
		s.endLine()
		cctx, stop := context.WithTimeout(context.WithoutCancel(s.ctx), 10*time.Second)
		defer stop()
		if _, err := s.c.CancelAgentTask(cctx, taskID); err != nil {
			s.errf("Couldn't stop the agent's task: %s\n", agentError(err))
		} else {
			s.errf("Stopped the agent's task.\n")
		}
		return followResult{interrupted: true}, nil
	}
	switch {
	case errors.Is(d.err, errWaiting):
		return followResult{waiting: true}, nil
	case errors.Is(d.err, errLeave):
		return followResult{left: true}, nil
	case d.err != nil:
		s.endLine()
		return followResult{}, agentError(d.err)
	}
	s.endLine()
	return followResult{end: d.end, ended: true}, nil
}

// finish turns how a task ended into the exit status of `agent ask`, saying
// why on stderr.
func (s *chat) finish(conversationID string, r followResult) error {
	switch {
	case r.interrupted:
		return exit(exitInterrupted)
	case r.waiting:
		s.errf("The agent is waiting for a person. Continue with: loomctl agent --conversation %s\n", conversationID)
		return exit(exitWaiting)
	case r.end.Status == "completed":
		return nil
	}
	s.reportEnd(r.end)
	return exit(1)
}

// reportEnd says why a task that didn't complete ended.
func (s *chat) reportEnd(end client.StreamEnd) {
	switch {
	case end.Error == safetyStopped && s.safetySaid:
	case end.Error != "":
		s.errf("%s\n", oneLine(end.Error, 500))
	case end.Status == "cancelled":
		s.errf("The agent's task was cancelled.\n")
	default:
		s.errf("The agent's task %s.\n", orDash(end.Status))
	}
}

// refused prints what the server said to a message it won't take (busy, the
// agent off in the workspace, out of budget) and reports it as an exit.
func (s *chat) refused(err error) error {
	var e *client.Error
	if errors.As(err, &e) && e.Status == http.StatusConflict {
		s.errf("%s\n", oneLine(e.Message, 500))
		return exit(1)
	}
	return agentError(err)
}

func (a *App) newChat(ctx cli.CommandContext, inline bool) (*chat, func(), error) {
	c, err := a.client()
	if err != nil {
		return nil, nil, err
	}
	st, err := a.settings()
	if err != nil {
		return nil, nil, err
	}
	intr, stop := a.interrupts()
	return &chat{
		a: a, c: c, ctx: ctxOf(ctx), base: strings.TrimRight(st.URL, "/"),
		asJSON: ctx.Bool("json"), noWait: ctx.Bool("no-wait"), inline: inline,
		lines: &lineSource{r: a.opts.In}, intr: intr,
	}, stop, nil
}

// projectID resolves --project.
func (s *chat) projectID(ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	p, err := s.c.FindProject(s.ctx, ref)
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

func (a *App) agentAsk(ctx cli.CommandContext) error {
	prompt := strings.TrimSpace(strings.Join(ctx.Args(), " "))
	if prompt == "" {
		return errors.New(`say what to ask: loomctl agent ask "what changed in the last deployment?"`)
	}
	s, stop, err := a.newChat(ctx, false)
	if err != nil {
		return err
	}
	defer stop()
	conv := strings.TrimSpace(ctx.String("conversation"))
	var sent *client.SentMessage
	if conv != "" {
		sent, err = s.c.SendMessage(s.ctx, conv, prompt)
	} else {
		var project string
		if project, err = s.projectID(ctx.String("project")); err != nil {
			return err
		}
		sent, err = s.c.StartConversation(s.ctx, prompt, project)
	}
	if err != nil {
		return s.refused(err)
	}
	res, err := s.follow(sent.Task.ID, 0)
	if err != nil {
		return err
	}
	return s.finish(sent.Conversation.ID, res)
}

// agentChat is `loomctl agent`: a conversation at a prompt.
func (a *App) agentChat(ctx cli.CommandContext) error {
	if len(ctx.Args()) > 0 {
		return fmt.Errorf("%q isn't an agent command. Ask one thing with: loomctl agent ask \"...\"", ctx.Arg(0))
	}
	s, stop, err := a.newChat(ctx, true)
	if err != nil {
		return err
	}
	defer stop()
	project, err := s.projectID(ctx.String("project"))
	if err != nil {
		return err
	}
	conv := strings.TrimSpace(ctx.String("conversation"))
	var open string // the task still going, for /stop and /follow
	if conv != "" {
		if open, err = s.resume(conv); err != nil {
			return err
		}
	}
	for {
		s.prompt("you> ")
		line, ok := s.lines.next(s.intr)
		if !ok {
			s.endPrompt()
			return nil
		}
		switch line = strings.TrimSpace(line); line {
		case "":
			continue
		case "/quit", "/exit":
			return nil
		case "/new":
			conv, open = "", ""
			s.errf("Started over. Your next message begins a new conversation.\n")
			continue
		case "/stop":
			if open == "" {
				s.errf("The agent isn't working on anything.\n")
				continue
			}
			if _, cerr := s.c.CancelAgentTask(s.ctx, open); cerr != nil {
				s.errf("%s\n", agentError(cerr))
			} else {
				s.errf("Stopped the agent's task.\n")
			}
			open = ""
			continue
		case "/follow":
			if open == "" {
				s.errf("The agent isn't working on anything.\n")
				continue
			}
			open = s.chatTurn(open, s.lastSeq)
			continue
		}
		var sent *client.SentMessage
		if conv == "" {
			sent, err = s.c.StartConversation(s.ctx, line, project)
		} else {
			sent, err = s.c.SendMessage(s.ctx, conv, line)
		}
		if err != nil {
			s.errf("%s\n", s.report(err))
			continue
		}
		conv = sent.Conversation.ID
		s.lastSeq = 0
		open = s.chatTurn(sent.Task.ID, 0)
	}
}

// endPrompt moves off the prompt's line when input ends or Ctrl-C comes at
// a terminal.
func (s *chat) endPrompt() {
	if f, ok := s.a.opts.In.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		s.errf("\n")
	}
}

// report is how a refused message reads at the prompt.
func (s *chat) report(err error) string {
	var e *client.Error
	if errors.As(err, &e) && e.Status == http.StatusConflict {
		return oneLine(e.Message, 500)
	}
	return agentError(err).Error()
}

// chatTurn follows one task at the prompt and returns the task's ID when it
// is still open afterwards.
func (s *chat) chatTurn(taskID string, after int64) string {
	res, err := s.follow(taskID, after)
	switch {
	case err != nil:
		s.errf("%s\n", err)
		return taskID
	case res.interrupted:
		return ""
	case res.left:
		s.errf("The agent is still waiting. /follow watches it again, /stop stops it.\n")
		return taskID
	case res.ended && res.end.Status != "completed":
		s.reportEnd(res.end)
	}
	s.safetySaid = false
	return ""
}

// resume picks up a conversation: when its latest message is still being
// answered, it follows it. It returns that task's ID.
func (s *chat) resume(conv string) (string, error) {
	h, err := s.c.Conversation(s.ctx, conv)
	if err != nil {
		return "", agentError(err)
	}
	if len(h.Turns) == 0 {
		return "", nil
	}
	last := h.Turns[len(h.Turns)-1]
	switch last.Status {
	case "queued", "running", "waiting":
		s.errf("The agent is still on your last message.\n")
		return s.chatTurn(last.TaskID, 0), nil
	}
	return "", nil
}

func (a *App) agentConversations(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	var project string
	if ref := ctx.String("project"); ref != "" {
		p, perr := c.FindProject(ctxOf(ctx), ref)
		if perr != nil {
			return perr
		}
		project = p.ID
	}
	list, err := c.Conversations(ctxOf(ctx), project)
	if err != nil {
		return agentError(err)
	}
	if ctx.Bool("json") {
		if list == nil {
			list = []client.Conversation{}
		}
		return a.json(list)
	}
	if len(list) == 0 {
		a.printf("No conversations yet. Start one with: loomctl agent\n")
		return nil
	}
	names := map[string]string{}
	for _, cv := range list {
		if cv.ProjectID != "" {
			ps, perr := c.Projects(ctxOf(ctx))
			if perr != nil {
				return perr
			}
			for _, p := range ps {
				names[p.ID] = p.Slug
			}
			break
		}
	}
	now := time.Now()
	rows := make([][]string, 0, len(list))
	for _, cv := range list {
		proj := "-"
		if cv.ProjectID != "" {
			proj = cv.ProjectID
			if n, ok := names[cv.ProjectID]; ok {
				proj = n
			}
		}
		rows = append(rows, []string{cv.ID, proj, orDash(oneLine(cv.Title, 50)), cv.Status, age(now, cv.UpdatedAt)})
	}
	a.table([]string{"ID", "PROJECT", "TITLE", "STATUS", "UPDATED"}, rows)
	return nil
}
