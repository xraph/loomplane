package releaseprep

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// settleBatch is how many reports one sweep looks at.
const settleBatch = 100

// Notice bounds: notifications' own, and how much of the agent's concerns
// a notice quotes.
const (
	maxNoticeTitle    = 200
	maxNoticeBody     = 2000
	noticeConcerns    = 3
	maxConcernInBrief = 300
)

// noticeWords are the states a notice goes out for, as its title says
// them. The rest need no one: approved, rejected, expired and superseded
// were a person's doing, and cancelled was asked for.
var noticeWords = map[string]string{
	StatePromoted: "promoted by the agent",
	StateStale:    "the plan went stale",
	StateStopped:  "waiting for a person",
	StateBlocked:  "the plan is blocked",
	StateFailed:   "preparation failed",
}

// planningBackstop is how long a report may stay planning before Settle
// stops it: well past the prepare job's retries and timeouts, so only a
// job that never ran is caught (lost while AI was off, or a crash between
// storing the report and queueing the job).
const planningBackstop = time.Hour

// Settle is the settle cron's body. For each unsettled report it reads
// the plan and the task with no principal, so it keeps working after the
// workspace turns its agent off, derives the state, and settles a report
// whose state is final. A report whose project, environment or plan no
// longer exists is closed as failed, and one whose prepare job never ran
// is stopped. A final state that needs someone is told to the person who
// asked and the project's team, in the settle's own transaction, so the
// notice and the settle commit together or not at all.
func (s *Service) Settle(ctx context.Context) error {
	reports, err := s.d.Store.Unsettled(ctx, settleBatch)
	if err != nil {
		return err
	}
	sw := newSweep()
	var errs []error
	for _, r := range reports {
		if err := s.settle(ctx, r, sw); err != nil {
			errs = append(errs, fmt.Errorf("settle release report %s: %w", r.ID, err))
		}
	}
	return errors.Join(errs...)
}

// outcome is where a report stands, and the status it settles as when
// that's final.
type outcome struct {
	status, state, reason string
}

// settle settles one report when its state is final.
func (s *Service) settle(ctx context.Context, r *ReleaseReport, sw *sweep) error {
	o, err := s.derive(ctx, r, sw)
	if err != nil || !Terminal(o.state) {
		return err
	}
	return s.finish(ctx, r, o, sw)
}

// derive is where r stands now, read with no principal: closed when its
// project or an environment is gone, stopped when its job never ran,
// otherwise DeriveState from the plan and the task.
func (s *Service) derive(ctx context.Context, r *ReleaseReport, sw *sweep) (outcome, error) {
	why, err := s.gone(ctx, r, sw)
	if err != nil {
		return outcome{}, err
	}
	if why != "" {
		return outcome{StatusFailed, StateFailed, why}, nil
	}
	if r.Status == StatusPlanning {
		// The prepare job moves a planning report on, or settles it.
		if !r.CreatedAt.IsZero() && s.now().Sub(r.CreatedAt) > planningBackstop {
			return outcome{StatusFailed, StateStopped, reasonJobNeverRan}, nil
		}
		return outcome{state: StatePlanning}, nil
	}
	var plan *planning.Plan
	if r.PlanID != "" {
		if s.d.PlanStore == nil {
			return outcome{}, errors.New("releaseprep: no plan store is configured")
		}
		plan, err = s.d.PlanStore.GetPlan(ctx, r.OrgID, r.PlanID)
		if errors.Is(err, planning.ErrNotFound) {
			plan, err = nil, nil
		}
		if err != nil {
			return outcome{}, fmt.Errorf("read the plan: %w", err)
		}
	}
	task, err := s.task(ctx, r, sw)
	if err != nil {
		return outcome{}, fmt.Errorf("read the task: %w", err)
	}
	state, reason := DeriveState(r, plan, task)
	status := StatusDone
	if state == StateFailed {
		status = StatusFailed
	}
	return outcome{status, state, reason}, nil
}

// gone is why a report can't go on: its project or one of its
// environments no longer exists. Nothing ties a report to them in the
// database.
func (s *Service) gone(ctx context.Context, r *ReleaseReport, sw *sweep) (string, error) {
	if s.d.Places == nil {
		return "", nil
	}
	exists, err := sw.place(r.OrgID+"/"+r.ProjectID, func() error {
		_, err := s.d.Places.GetProject(ctx, r.OrgID, r.ProjectID)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("read the project: %w", err)
	}
	if !exists {
		return reasonNoProject, nil
	}
	for _, envID := range []string{r.ToEnvironmentID, r.FromEnvironmentID} {
		if envID == "" {
			continue
		}
		exists, err = sw.place(r.OrgID+"/"+r.ProjectID+"/"+envID, func() error {
			_, gerr := s.d.Places.GetEnvironment(ctx, r.OrgID, r.ProjectID, envID)
			return gerr
		})
		if err != nil {
			return "", fmt.Errorf("read an environment: %w", err)
		}
		if !exists {
			return reasonNoEnvironment, nil
		}
	}
	return "", nil
}

// finish settles the report, and in the same transaction tells who needs
// to know. The notice is built only once the report is held, so nothing is
// scanned or mapped for a report that moved on. A report that moved on
// meanwhile, such as a person cancelling it, is left as it is.
func (s *Service) finish(ctx context.Context, r *ReleaseReport, o outcome, sw *sweep) error {
	var send func(ctx context.Context, tx NoticeTx) error
	if _, ok := noticeWords[o.state]; ok {
		send = func(ctx context.Context, tx NoticeTx) error {
			recipients, err := s.recipients(ctx, tx, r)
			if err != nil {
				return err
			}
			n := s.notice(ctx, r, o.state, o.reason, sw)
			n.Recipients = recipients
			return s.send(ctx, tx, n)
		}
	}
	err := s.d.Store.Finish(ctx, r.OrgID, r.ID, r.State, o.status, o.state, o.reason, "", s.now(), send)
	if errors.Is(err, ErrMovedOn) {
		return nil
	}
	return err
}

// notice is the notice for a report reaching state, all but its
// recipients. Its title and fields are Loomplane's; its body is the
// reason, and the agent's concerns only when they pass the outbound scan.
func (s *Service) notice(ctx context.Context, r *ReleaseReport, state, reason string, sw *sweep) notifications.Notice {
	n := notifications.Notice{
		Kind: notifications.KindAgentRelease, OrgID: r.OrgID, ProjectID: r.ProjectID, SubjectID: r.ID + ":" + state,
		Title: cutRunes("Release to "+orDefault(r.ToEnvironment, "production")+": "+noticeWords[state], maxNoticeTitle),
		Body:  s.noticeBody(ctx, r, reason, sw),
		Path:  "/release-reports/" + r.ID,
		Need:  security.DeploymentRead.Name(),
		Metadata: map[string]string{
			"release_report_id": r.ID, "plan_id": r.PlanID, "state": state,
		},
	}
	if s.d.Push != nil {
		push, err := s.d.Push.ReleasePush(ctx, r, state)
		if err != nil {
			s.warn("releaseprep: couldn't build release report "+r.ID+"'s push; the notice goes without one", err)
		} else {
			n.Push = push
		}
	}
	return n
}

// noticeBody is the reason as a sentence. The agent's concerns, already
// scanned when the agent added them, are quoted only through another
// outbound scan bound to the workspace; with the agent off, or a scan that
// won't pass them, the body is the reason alone.
func (s *Service) noticeBody(ctx context.Context, r *ReleaseReport, reason string, sw *sweep) string {
	plain := cutRunes(sentence(reason), maxNoticeBody)
	if len(r.Concerns) == 0 || s.d.Safety == nil {
		return plain
	}
	bound, ok := sw.bound(ctx, s, r.OrgID)
	if !ok {
		return plain
	}
	var b strings.Builder
	b.WriteString(plain)
	b.WriteString("\n\nThe agent's concerns:")
	for i, c := range r.Concerns {
		if i == noticeConcerns {
			fmt.Fprintf(&b, "\n- and %d more in the report", len(r.Concerns)-noticeConcerns)
			break
		}
		b.WriteString("\n- " + cutRunes(strings.TrimSpace(c.Text), maxConcernInBrief))
	}
	out, err := s.d.Safety.Outbound(bound, cutRunes(b.String(), maxNoticeBody))
	if err != nil || strings.TrimSpace(out) == "" {
		return plain
	}
	return cutRunes(out, maxNoticeBody)
}

// recipients are the person who asked and the project's responsible team,
// or the workspace's admins when it has none, each once. teams.Resolve
// makes that choice, read in the notice's transaction.
func (s *Service) recipients(ctx context.Context, tx NoticeTx, r *ReleaseReport) ([]string, error) {
	var out []string
	if person(r.RequestedBy) {
		out = append(out, r.RequestedBy)
	}
	if s.d.Teams == nil {
		s.warn("releaseprep: teams aren't configured; release report "+r.ID+"'s notice goes to its requester only", errors.New("no teams"))
	} else {
		route, err := s.d.Teams.Resolve(ctx, tx, r.OrgID, r.ProjectID, "", "")
		if err != nil {
			return nil, fmt.Errorf("route the notice: %w", err)
		}
		for _, m := range route.Members {
			if person(m) {
				out = append(out, m)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

func (s *Service) send(ctx context.Context, q notifications.Execer, n notifications.Notice) error {
	if s.d.Notify != nil {
		return s.d.Notify(ctx, q, n)
	}
	return notifications.Enqueue(ctx, q, n)
}

// sentence is a reason as a notice says it: capitalized, ending in a full
// stop.
func sentence(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "Open the release report in Loomplane to see what happened."
	}
	first, size := utf8.DecodeRuneInString(reason)
	reason = string(unicode.ToUpper(first)) + reason[size:]
	if !strings.HasSuffix(reason, ".") {
		reason += "."
	}
	return reason
}

func cutRunes(text string, n int) string {
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	runes := []rune(text)
	return string(runes[:n])
}

// sweep is what one sweep, or one read, has already learnt: whether each
// workspace's agent is on, its bound context, and which projects and
// environments exist.
type sweep struct {
	on     map[string]bool
	ctxs   map[string]context.Context
	places map[string]bool
}

func newSweep() *sweep {
	return &sweep{on: map[string]bool{}, ctxs: map[string]context.Context{}, places: map[string]bool{}}
}

// agentOn reports whether the workspace's agent is on. A read that fails
// counts as on, so a cancelled task isn't said to be the agent turned off
// without knowing it.
func (sw *sweep) agentOn(ctx context.Context, s *Service, orgID string) bool {
	if on, ok := sw.on[orgID]; ok {
		return on
	}
	on := true
	if b := s.sources().Budgets; b != nil {
		enabled, err := b.Enabled(ctx, orgID)
		if err != nil {
			s.warn("releaseprep: couldn't read whether the agent is on", err)
		} else {
			on = enabled
		}
	}
	sw.on[orgID] = on
	return on
}

// bound is ctx bound to the workspace for an outbound scan, once per
// workspace; false while its agent is off.
func (sw *sweep) bound(ctx context.Context, s *Service, orgID string) (context.Context, bool) {
	if b, ok := sw.ctxs[orgID]; ok {
		return b, b != nil
	}
	b, ok := s.bind(ctx, orgID, "")
	if !ok {
		b = nil
	}
	sw.ctxs[orgID] = b
	return b, ok
}

// place reports whether a project or environment exists, reading it once
// per key: read returns nil when it does, projects' not-found when it
// doesn't, and any other error is returned and not remembered.
func (sw *sweep) place(key string, read func() error) (bool, error) {
	if exists, ok := sw.places[key]; ok {
		return exists, nil
	}
	err := read()
	switch {
	case err == nil:
		sw.places[key] = true
	case errors.Is(err, projects.ErrNotFound), errors.Is(err, projects.ErrEnvironmentNotFound):
		sw.places[key] = false
	default:
		return false, err
	}
	return sw.places[key], nil
}
