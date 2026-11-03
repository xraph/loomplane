package agentrequests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/checkpoint"
	"github.com/xraph/cortex/suspension"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/teams"
	pkgops "github.com/xraph/loomplane/pkg/agentops"
)

const (
	maxOptions      = 20
	maxNoticeTitle  = 200
	maxNoticeBody   = 2000
	maxQuestionRune = 4000
)

// keyPattern is a variable key, as the variables service takes it.
var keyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,254}$`)

// shortKind says what a request asks for, for a notice's title.
var shortKind = map[Kind]string{
	KindApprovePlan:      "approve a plan",
	KindConnectGitHub:    "connect GitHub",
	KindProvideSecret:    "set a secret",
	KindCreateConnection: "connect a cloud",
	KindAnswer:           "answer a question",
}

// blockedQuestion is what a request asks when the model's words don't pass
// the outbound scan.
var blockedQuestion = map[Kind]string{
	KindApprovePlan:      "The agent asks you to approve a plan, but its words didn't pass Loomplane's safety scan.",
	KindConnectGitHub:    "The agent needs GitHub connected, but its words didn't pass Loomplane's safety scan.",
	KindProvideSecret:    "The agent needs a secret set, but its words didn't pass Loomplane's safety scan.",
	KindCreateConnection: "The agent needs a cloud connected, but its words didn't pass Loomplane's safety scan.",
	KindAnswer:           "The agent has a question for you, but its words didn't pass Loomplane's safety scan.",
}

// kindOf is the request a pending call makes.
var kindOf = map[string]Kind{
	"approve_plan":              KindApprovePlan,
	"request_github_connection": KindConnectGitHub,
	"request_secret":            KindProvideSecret,
	"request_cloud_connection":  KindCreateConnection,
	"ask_person":                KindAnswer,
}

// draft is a request about to be made, with the team the agent named.
type draft struct {
	req          *Request
	named        string
	reason       string
	projectTitle string
}

// OnSuspended makes one request for each call the run waits on, routes
// it, and tells whoever it's routed to, in one transaction. A notice
// cortex repeats finds the requests made and adds nothing.
//
// The task is bound first, from its stored row, so every scan runs in its
// workspace; a task that can't be bound gets no request.
func (s *Service) OnSuspended(ctx context.Context, task *agentruns.Task, n suspension.Notice) error {
	if task == nil || len(n.Pending) == 0 {
		return nil
	}
	if n.Reason != suspension.ReasonApproval && n.Reason != suspension.ReasonExternalTool {
		return nil
	}
	bound, err := s.d.Runs.Bind(ctx, task)
	if err != nil {
		return fmt.Errorf("bind the paused task: %w", err)
	}
	actor, ok := cortex.PrincipalFromContext(bound).(security.Principal)
	if !ok || !actor.IsAgent() || actor.OrgID != task.OrgID {
		return errors.New("agentrequests: the bound task carries no agent principal")
	}
	if n.Reason == suspension.ReasonApproval {
		for _, pc := range n.Pending {
			if pc.Name != "approve_plan" {
				// The risk table pauses only plan approvals (Task 14).
				s.warn("agentrequests: a checkpoint on a call that isn't a plan approval was rejected", fmt.Errorf("tool %q", pc.Name))
				return s.reject(bound, n, "only plan approvals wait for a person; ask with ask_person instead")
			}
		}
	}
	now := s.now().UTC()
	drafts := make([]draft, 0, len(n.Pending))
	for _, pc := range n.Pending {
		d, rejectWhy := s.draft(bound, actor, task, n, pc, now)
		if rejectWhy != "" {
			return s.reject(bound, n, rejectWhy)
		}
		drafts = append(drafts, d)
	}
	made, err := s.create(ctx, bound, task, drafts)
	if err != nil {
		return err
	}
	for _, r := range made {
		if rerr := s.d.Runs.RecordRequest(ctx, task, r.ID, string(r.Kind)); rerr != nil {
			s.warn("agentrequests: couldn't log a request on its run", rerr)
		}
	}
	if n.Reason != suspension.ReasonExternalTool {
		return nil
	}
	// The run goes on once every request of the pause is settled: calls
	// that couldn't become requests are settled as they're made. This
	// reads the stored pause, so a notice repeated after a failed resume
	// still resumes it.
	pause, err := s.d.Store.Pause(ctx, task.OrgID, task.RunID, n.SuspensionID.String())
	if err != nil || len(pause) == 0 {
		return err
	}
	return s.resume(ctx, pause[0], true)
}

// reject decides an approval pause rejected, with why.
func (s *Service) reject(bound context.Context, n suspension.Notice, why string) error {
	if n.CheckpointID.IsNil() {
		return errors.New("agentrequests: a request couldn't be made for a paused call")
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	err = eng.DecideCheckpoint(bound, n.CheckpointID, checkpoint.Decision{Approved: false, DecidedBy: "loomplane", Reason: why, DecidedAt: s.now().UTC()})
	if err != nil && !errors.Is(err, cortex.ErrInvalidState) {
		return err
	}
	return nil
}

// create writes the drafts, their routes and their notices in one
// transaction, and returns the requests it made.
func (s *Service) create(ctx, bound context.Context, task *agentruns.Task, drafts []draft) ([]*Request, error) {
	tx, err := s.d.Store.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // a committed transaction can't roll back
	made := make([]*Request, 0, len(drafts))
	for _, d := range drafts {
		var recipients []string
		if d.req.Status == StatusPending {
			if recipients, err = s.route(ctx, bound, tx, task, &d); err != nil {
				return nil, err
			}
		}
		created, ierr := s.d.Store.Insert(ctx, tx, d.req)
		if ierr != nil {
			return nil, ierr
		}
		if !created {
			continue
		}
		if d.req.Status == StatusPending {
			if err = s.enqueue(ctx, tx, s.notice(bound, d, recipients)); err != nil {
				return nil, fmt.Errorf("queue the request's notice: %w", err)
			}
		}
		made = append(made, d.req)
	}
	if cerr := tx.Commit(); cerr != nil {
		return nil, cerr
	}
	return made, nil
}

// route picks who hears about the request and records it on the request.
// Plan approvals go to the project's team, then the admins; the agent
// names no team for them. A question or a secret in a chat goes to the
// asker, plus the team the agent named when it has members.
func (s *Service) route(ctx, bound context.Context, tx Tx, task *agentruns.Task, d *draft) ([]string, error) {
	r := d.req
	if s.d.Teams == nil {
		return nil, errors.New("agentrequests: teams aren't configured")
	}
	named, reason := d.named, d.reason
	if r.Kind == KindApprovePlan {
		named, reason = "", ""
	}
	chat := task.RequestedBy != "" && (r.Kind == KindAnswer || r.Kind == KindProvideSecret)
	if chat && named == "" {
		r.RouteSource = RouteAsker
		return []string{task.RequestedBy}, nil
	}
	route, err := s.d.Teams.Resolve(ctx, tx, r.OrgID, r.ProjectID, named, reason)
	if err != nil {
		return nil, fmt.Errorf("route the request: %w", err)
	}
	r.RouteNote = s.routeNote(bound, route)
	if chat && route.Source != "named" {
		r.RouteSource = RouteAsker
		return []string{task.RequestedBy}, nil
	}
	r.TeamID, r.TeamName, r.RouteSource, r.TeamReason = route.TeamID, route.Team, route.Source, route.Reason
	recipients := slices.Clone(route.Members)
	if chat {
		recipients = append(recipients, task.RequestedBy)
	}
	slices.Sort(recipients)
	return slices.Compact(recipients), nil
}

// unknownTeamNote is a route note that didn't pass the outbound scan.
const unknownTeamNote = "The agent named a team that doesn't exist; sent to workspace admins"

// routeNote is the route's note as people may read it. It can quote the
// team the model named, so it goes through the outbound scan, and a note
// that doesn't pass becomes a fixed one.
func (s *Service) routeNote(bound context.Context, route teams.Route) string {
	if strings.TrimSpace(route.Note) == "" {
		return ""
	}
	if out, err := s.outbound(bound, route.Note); err == nil && strings.TrimSpace(out) != "" {
		return out
	}
	if route.Source == "admins" {
		return unknownTeamNote
	}
	return "The agent named a team that doesn't exist; sent to " + route.Team
}

// notice tells the recipients about r. Its title is Loomplane's; its body
// is the outbound-scanned question and who it was sent to, scanned again
// as a whole.
func (s *Service) notice(bound context.Context, d draft, recipients []string) notifications.Notice {
	r := d.req
	line := ""
	switch r.RouteSource {
	case "named":
		line = "Sent to " + r.TeamName
		if r.TeamReason != "" {
			line += ": " + r.TeamReason
		}
	case "project":
		line = "Sent to " + r.TeamName + ", the team responsible for " + d.projectTitle + "."
	case "admins":
		line = "Sent to the workspace admins."
	case RouteAsker:
		line = "Asked in your conversation with the agent."
	}
	room := maxNoticeBody - utf8.RuneCountInString(line) - 2
	body := cutRunes(r.Question, room) + "\n\n" + line
	if scanned, err := s.outbound(bound, body); err == nil {
		body = cutRunes(scanned, maxNoticeBody)
	} else {
		body = "Open the request in Loomplane to see what the agent needs."
	}
	return notifications.Notice{
		Kind: notifications.KindAgentRequest, OrgID: r.OrgID, ProjectID: r.ProjectID, SubjectID: r.ID,
		Recipients: recipients, Title: cutRunes("The agent needs you: "+shortKind[r.Kind], maxNoticeTitle), Body: body,
		Path:     "/agent/requests?id=" + r.ID,
		Metadata: map[string]string{"request_id": r.ID, "kind": string(r.Kind), "task_id": r.TaskID},
		Need:     security.AgentUse.Name(),
	}
}

func (s *Service) outbound(bound context.Context, text string) (string, error) {
	if s.d.Safety == nil {
		return "", errors.New("agentrequests: no safety scanner")
	}
	return s.d.Safety.Outbound(bound, text)
}

// scanned is text through the outbound scan, or fallback when it's
// blocked or empty.
func (s *Service) scanned(bound context.Context, text, fallback string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return fallback
	}
	out, err := s.outbound(bound, cutRunes(text, maxQuestionRune))
	if err != nil || strings.TrimSpace(out) == "" {
		return fallback
	}
	return out
}

func cutRunes(text string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	r := []rune(text)
	return string(r[:n])
}

// draft builds the request for one pending call. A call whose request
// can't be made, such as a secret for a project outside the run's, is a
// request already cancelled, with the reason the model reads. rejectWhy
// is set instead for a plan approval that can't be asked: its checkpoint
// is rejected.
func (s *Service) draft(bound context.Context, actor security.Principal, task *agentruns.Task, n suspension.Notice, pc suspension.PendingCall, now time.Time) (d draft, rejectWhy string) {
	reqID, err := typeid.Generate("areq")
	kind, known := kindOf[pc.Name]
	r := &Request{
		OrgID: task.OrgID, ProjectID: task.ProjectID, EnvironmentID: task.EnvironmentID, TaskID: task.ID, RunID: task.RunID,
		Kind: kind, Status: StatusPending, Context: []Link{}, Options: []string{}, AskedFor: task.RequestedBy,
		ExpiresAt: now.Add(agentops.QuestionTTL), CreatedAt: now, UpdatedAt: now,
		SuspensionID: n.SuspensionID.String(), ToolCallID: pc.ID,
	}
	if n.ExpiresAt != nil {
		r.ExpiresAt = n.ExpiresAt.UTC()
	}
	if n.Reason == suspension.ReasonApproval {
		r.CheckpointID = n.CheckpointID.String()
	}
	d = draft{req: r}
	if err != nil {
		return d, "a request couldn't be made"
	}
	r.ID = reqID.String()
	cancel := func(why string) (draft, string) {
		if !known {
			r.Kind = KindAnswer
		}
		r.Status, r.Outcome, r.Answer, r.AnsweredAt = StatusCancelled, OutcomeCancelled, why, &now
		r.Question = "The agent's request couldn't be made: " + why
		return d, ""
	}
	if !known {
		return cancel(pc.Name + " isn't a request Loomplane knows")
	}
	args := json.RawMessage(pc.Arguments)
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if kind != KindApprovePlan {
		if verr := agentops.ValidateRequest(pc.Name, args); verr != nil {
			return cancel(verr.Error())
		}
	}
	switch kind {
	case KindApprovePlan:
		var in pkgops.ApproveInput
		if json.Unmarshal(args, &in) != nil || in.PlanID == "" {
			return d, "approve_plan needs the plan's ID"
		}
		plan, gerr := s.d.Plans.Get(bound, security.AgentPrincipal(task.OrgID, nil), in.PlanID)
		if gerr != nil || plan.OrgID != task.OrgID || (task.ProjectID != "" && plan.ProjectID != task.ProjectID) ||
			(task.EnvironmentID != "" && plan.EnvironmentID != task.EnvironmentID) {
			return d, "the plan isn't one this run can reach"
		}
		// The request names the revision the stored plan has, never the
		// model's string: a person approves it, and loomctl prints it in a
		// command people paste. A model that read another revision is
		// refused, the way approving a changed plan is.
		if in.Revision != plan.Revision {
			return d, "the plan is stale: it changed since you read it, so read it again with get_plan"
		}
		r.ProjectID, r.EnvironmentID = plan.ProjectID, plan.EnvironmentID
		r.Target = Target{PlanID: plan.ID, PlanRevision: plan.Revision}
		r.Context = append(r.Context, Link{Label: "The plan", Path: "/deployments/" + plan.ID})
		text := strings.TrimSpace(strings.Join(nonEmpty(in.Note, pc.Note), "\n\n"))
		r.Question = s.scanned(bound, text, blockedQuestion[kind])
		if text == "" {
			r.Question = "The agent asks you to approve this plan."
		}
	case KindAnswer:
		var in pkgops.AskPersonInput
		if json.Unmarshal(args, &in) != nil {
			return cancel("ask_person's arguments couldn't be read")
		}
		projectID, perr := s.projectFor(bound, actor, task, in.Project)
		if perr != nil {
			return cancel(perr.Error())
		}
		r.ProjectID = projectID
		r.Question = s.scanned(bound, in.Question, blockedQuestion[kind])
		for _, o := range in.Options {
			if len(r.Options) == maxOptions {
				break
			}
			if o = strings.TrimSpace(o); o == "" {
				continue
			}
			if scanned, oerr := s.outbound(bound, cutRunes(o, 200)); oerr == nil && strings.TrimSpace(scanned) != "" {
				r.Options = append(r.Options, scanned)
			}
		}
		d.named, d.reason = in.Team, in.TeamReason
	case KindProvideSecret:
		var in pkgops.RequestSecretInput
		if json.Unmarshal(args, &in) != nil {
			return cancel("request_secret's arguments couldn't be read")
		}
		projectID, perr := s.projectFor(bound, actor, task, in.Project)
		if perr == nil && projectID == "" {
			perr = errors.New("name the project the secret is for")
		}
		if perr != nil {
			return cancel(perr.Error())
		}
		key := strings.TrimSpace(in.Key)
		if !keyPattern.MatchString(key) {
			return cancel("a key is letters, digits and underscores, not starting with a digit")
		}
		env, eerr := s.environmentFor(bound, actor, task, projectID, in.Environment)
		if eerr != nil {
			return cancel(eerr.Error())
		}
		r.ProjectID = projectID
		r.Target = Target{Key: key, Environment: env, Service: strings.TrimSpace(in.Service)}
		r.Question = s.scanned(bound, in.Why, blockedQuestion[kind])
		r.Context = append(r.Context, Link{Label: "The project", Path: "/projects/" + projectID})
		d.named, d.reason = in.Team, in.TeamReason
	case KindConnectGitHub:
		var in pkgops.RequestGitHubInput
		if json.Unmarshal(args, &in) != nil {
			return cancel("request_github_connection's arguments couldn't be read")
		}
		r.Target = Target{Repository: s.scanned(bound, in.Repository, "")}
		r.Question = s.scanned(bound, in.Why, blockedQuestion[kind])
		r.Context = append(r.Context, Link{Label: "GitHub", Path: "/integrations"})
		d.named, d.reason = in.Team, in.TeamReason
	case KindCreateConnection:
		var in pkgops.RequestConnectionInput
		if json.Unmarshal(args, &in) != nil {
			return cancel("request_cloud_connection's arguments couldn't be read")
		}
		cloud := strings.ToLower(strings.TrimSpace(in.Cloud))
		if cloud == "" || len(cloud) > 40 || strings.ContainsAny(cloud, " \t\n/") {
			return cancel("name the cloud to connect, such as aws, gcp, azure or digitalocean")
		}
		r.Target = Target{Cloud: cloud}
		r.Question = s.scanned(bound, in.Why, blockedQuestion[kind])
		r.Context = append(r.Context, Link{Label: "Cloud accounts", Path: "/clouds"})
		d.named, d.reason = in.Team, in.TeamReason
	}
	if r.ProjectID != "" {
		if r.Kind == KindAnswer {
			r.Context = append(r.Context, Link{Label: "The project", Path: "/projects/" + r.ProjectID})
		}
		if pr, perr := s.d.Projects.Get(bound, actor, r.ProjectID); perr == nil {
			d.projectTitle = pr.Name
		}
		if d.projectTitle == "" {
			d.projectTitle = "this project"
		}
	}
	d.named = strings.TrimSpace(d.named)
	if d.named != "" {
		d.reason = s.scanned(bound, d.reason, "the agent's reason didn't pass Loomplane's safety scan")
	} else {
		d.reason = ""
	}
	return d, ""
}

func nonEmpty(parts ...string) []string {
	out := []string{}
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// errOutside is a project the run can't reach.
var errOutside = errors.New("the project it names isn't one this run can reach")

// projectFor resolves the project a call names, by ID, slug or name, as
// the run's own principal: "" is the task's project. A project outside
// the task's is refused.
func (s *Service) projectFor(bound context.Context, actor security.Principal, task *agentruns.Task, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return task.ProjectID, nil
	}
	if s.d.Projects == nil {
		return "", errOutside
	}
	list, err := s.d.Projects.List(bound, actor)
	if err != nil {
		return "", errOutside
	}
	var found string
	var byName []string
	for _, p := range list {
		if p.OrgID != task.OrgID {
			continue
		}
		if p.ID == ref || p.Slug == ref {
			found = p.ID
			break
		}
		if strings.EqualFold(p.Name, ref) {
			byName = append(byName, p.ID)
		}
	}
	if found == "" && len(byName) == 1 {
		found = byName[0]
	}
	if found == "" || (task.ProjectID != "" && found != task.ProjectID) {
		return "", errOutside
	}
	return found, nil
}

// environmentFor checks the environment a secret is for: one of the
// project's, by name, and the run's own when the run is scoped to one. A
// run scoped to an environment sets secrets there only.
func (s *Service) environmentFor(bound context.Context, actor security.Principal, task *agentruns.Task, projectID, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" && task.EnvironmentID == "" {
		return "", nil
	}
	envs, err := s.d.Projects.Environments(bound, actor, projectID)
	if err != nil {
		return "", errors.New("the project's environments couldn't be read")
	}
	for _, e := range envs {
		switch {
		case task.EnvironmentID != "" && e.ID == task.EnvironmentID:
			if name == "" || name == e.Name {
				return e.Name, nil
			}
			return "", errors.New("this run sets secrets only in its own environment")
		case task.EnvironmentID == "" && e.Name == name:
			return e.Name, nil
		}
	}
	return "", errors.New("the project has no such environment")
}
