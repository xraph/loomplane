package agentwatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

// How the watcher runs.
const (
	LeaseTTL     = 2 * time.Minute
	EventPage    = 500
	RecheckEvery = 10 * time.Minute
	// OffForLong is how long a watcher goes without a tick before it
	// starts again at the high water mark instead of replaying.
	OffForLong         = time.Hour
	InvestigationGrace = 10 * time.Minute
	// InvestigationCeiling is how long an investigation may stay queued
	// or running before the watcher gives up on it, so a run that never
	// ends doesn't hold one of the MaxConcurrentInvestigations for good.
	InvestigationCeiling        = time.Hour
	MaxConcurrentInvestigations = 3
	PauseBudget                 = "budget"
	// runTimeout bounds one workspace's pass, well under LeaseTTL, so the
	// lease never runs out mid-pass.
	runTimeout = 90 * time.Second
	// maxNoteRunes bounds an investigation note people read.
	maxNoteRunes = 500
)

// What people read about investigations and the budget.
const (
	noteBudget     = "The agent didn't investigate this: the workspace's AI budget for the month is used up."
	noteCapped     = "Three investigations were already running."
	noteUnfinished = "The investigation didn't finish within an hour."
	// A run that couldn't start or stopped early gets one of these, never
	// its error: an error can name a host, a database or a provider, and
	// the note goes into the issue. The log keeps the error.
	noteNotStarted = "The agent couldn't start looking into this."
	noteStopped    = "The agent couldn't finish looking into this."

	pausedTitle  = "The agent stopped investigating: this workspace has used its AI budget for the month"
	pausedBody   = "Loomplane still opens reports from what it sees and sends them as usual. The agent won't investigate them until the budget has room. An admin can raise it in AI settings."
	resumedTitle = "The agent is investigating again"
	resumedBody  = "This workspace's AI budget has room again, so the agent is investigating new reports."
	settingsPath = "/settings/ai"
)

// Events reads a workspace's recorded events. *realtime.PgStore is the
// real one: cursors are "<org>:<sequence>".
type Events interface {
	HighWater(ctx context.Context, org string) (string, error)
	Events(ctx context.Context, org, after string, limit int) ([]realtime.Event, bool, error)
}

var _ Events = (*realtime.PgStore)(nil)

// Budget is what the watcher reads of the agent's settings.
// *agentsettings.Service is the real one.
type Budget interface {
	EnabledOrgs(ctx context.Context) ([]string, error)
	Enabled(ctx context.Context, orgID string) (bool, error)
	RequireBudget(ctx context.Context, orgID string) error
}

var _ Budget = (*agentsettings.Service)(nil)

// Runs starts and reads the agent's tasks. *agentruns.Service is the real
// one.
type Runs interface {
	Start(ctx context.Context, in agentruns.StartInput) (*agentruns.Task, error)
	ByRun(ctx context.Context, orgID, runID string) (*agentruns.Task, error)
}

var _ Runs = (*agentruns.Service)(nil)

// Enqueuer queues the watcher's jobs. DispatchEnqueuer is the real one.
type Enqueuer interface {
	WatchOrg(ctx context.Context, orgID string) error
	Deliver(ctx context.Context, orgID, reportID string) error
}

// Issues is what the watcher reads of GitHub issues: whether a waiting
// report's installation accepted them now, and how they stand for a
// person. *repositories.Connections is the real one.
type Issues interface {
	repositories.IssueTracker
	IssuesStatus(ctx context.Context, p security.Principal) (*repositories.IssuesStatus, error)
}

var _ Issues = (*repositories.Connections)(nil)

// Authorizer checks a person may read the watcher's status.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// Watcher watches each workspace whose agent is on: it turns the
// workspace's events, a sweep of its bindings and rechecks of its open
// reports into reports, investigates new ones within the AI budget, and
// queues their delivery. One replica watches a workspace at a time.
type Watcher struct {
	Store    Store
	Events   Events
	Detector *Detector
	Reports  *agentreports.Service
	Budget   Budget
	Runs     Runs
	Safety   *agentsafety.Scanner
	Binder   *agentscope.Binder
	// DB reads the owners and admins the budget notices go to.
	// *pgdriver.PgDB is the real one.
	DB    notifications.Querier
	Jobs  Enqueuer
	Audit projects.Auditor
	// Issues re-queues reports waiting on an installation that accepted
	// Issues since. Nil leaves them waiting.
	Issues Issues
	Authz  Authorizer
	// Owner is this replica, Dispatch's worker ID. Each pass leases the
	// workspace under it and a random suffix, so two passes on one replica
	// don't share a lease either.
	Owner string
	Now   func() time.Time
	Warn  func(msg string, err error)
}

func (w *Watcher) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func (w *Watcher) warn(msg string, err error) {
	if w.Warn != nil {
		w.Warn(msg, err)
	}
}

// Tick is the cron job's body: it queues a pass for every workspace whose
// agent is on. One that can't be queued doesn't stop the others.
func (w *Watcher) Tick(ctx context.Context) error {
	orgs, err := w.Budget.EnabledOrgs(ctx)
	if err != nil {
		return fmt.Errorf("list the workspaces with the agent on: %w", err)
	}
	var errs []error
	for _, org := range orgs {
		if err := w.Jobs.WatchOrg(ctx, org); err != nil {
			errs = append(errs, fmt.Errorf("queue the watcher of %s: %w", org, err))
		}
	}
	return errors.Join(errs...)
}

// RunOrg is one pass over a workspace, the per-workspace job's body. It
// does nothing while the agent is off there, or while another pass holds
// the workspace.
func (w *Watcher) RunOrg(ctx context.Context, orgID string) error {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	on, err := w.Budget.Enabled(ctx, orgID)
	if err != nil || !on {
		return err
	}
	owner, err := passOwner(w.Owner)
	if err != nil {
		return err
	}
	st, claimed, err := w.Store.Claim(ctx, orgID, owner, LeaseTTL, func() (int64, error) { return w.highWater(ctx, orgID) })
	if err != nil || !claimed {
		return err
	}
	defer func() {
		if rerr := w.Store.Release(context.WithoutCancel(ctx), orgID, owner); rerr != nil {
			w.warn("agentwatch: let the watcher of "+orgID+" go", rerr)
		}
	}()
	bound, err := w.Binder.Bind(ctx, security.AgentPrincipal(orgID, nil), agentscope.Scope{OrgID: orgID})
	if errors.Is(err, agentsettings.ErrDisabled) {
		return nil // turned off since Enabled said on
	}
	if err != nil {
		return err
	}
	d := *w.Detector
	if d.Warn == nil {
		d.Warn = w.Warn
	}
	p := &pass{w: w, ctx: bound, org: orgID, owner: owner, st: st, now: w.now(), detector: &d}
	err = p.run()
	if errors.Is(err, ErrLeaseLost) {
		return nil // the replica that holds it now carries on
	}
	return err
}

// passOwner is one pass's lease owner: the replica and a random suffix.
func passOwner(replica string) (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("agentwatch: make a lease owner: %w", err)
	}
	if replica == "" {
		replica = "watcher"
	}
	return replica + ":" + hex.EncodeToString(b), nil
}

func (w *Watcher) highWater(ctx context.Context, orgID string) (int64, error) {
	hw, err := w.Events.HighWater(ctx, orgID)
	if err != nil {
		return 0, err
	}
	return sequence(orgID, hw)
}

// sequence is the sequence in one of realtime's "<org>:<sequence>"
// cursors.
func sequence(orgID, cursor string) (int64, error) {
	owner, value, ok := strings.Cut(cursor, ":")
	if !ok || owner != orgID {
		return 0, fmt.Errorf("agentwatch: %q isn't a cursor of %s", cursor, orgID)
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("agentwatch: %q isn't a cursor of %s", cursor, orgID)
	}
	return n, nil
}

func cursor(orgID string, seq int64) string { return orgID + ":" + strconv.FormatInt(seq, 10) }

// pass is one RunOrg: the workspace's state while it holds the lease.
type pass struct {
	w        *Watcher
	ctx      context.Context // bound to the workspace as the agent alone
	org      string
	owner    string
	st       *State
	now      time.Time
	detector *Detector
	// rechecked is set once the open reports were rechecked this pass.
	rechecked bool
}

func (p *pass) run() error {
	// A watcher that was off for long doesn't replay what happened: it
	// starts at the high water mark and rechecks what's open.
	if p.st.TickedAt != nil && p.now.Sub(*p.st.TickedAt) > OffForLong {
		if err := p.jump(); err != nil {
			return err
		}
	}
	if p.st.Paused {
		if err := p.resume(); err != nil {
			return err
		}
	}
	if err := p.events(); err != nil {
		return err
	}
	if err := p.sweep(); err != nil {
		return err
	}
	if err := p.investigate(); err != nil {
		return err
	}
	if err := p.follow(); err != nil {
		return err
	}
	if err := p.deliver(); err != nil {
		return err
	}
	now := p.now
	p.st.TickedAt = &now
	return p.save()
}

func (p *pass) save() error { return p.w.Store.Save(p.ctx, p.owner, p.st) }

// jump moves the cursor to the high water mark and rechecks every open
// report now, for a gap the events can't fill.
func (p *pass) jump() error {
	hw, err := p.w.highWater(p.ctx, p.org)
	if err != nil {
		return err
	}
	p.st.EventCursor = hw
	if err := p.recheck(); err != nil {
		return err
	}
	return p.save()
}

// events reads one page of events after the cursor. One a detector can't
// read is skipped, so it never holds the cursor up.
func (p *pass) events() error {
	evs, found, err := p.w.Events.Events(p.ctx, p.org, cursor(p.org, p.st.EventCursor), EventPage)
	if err != nil {
		return fmt.Errorf("read the workspace's events: %w", err)
	}
	if !found {
		// Pruned past the cursor, or the cursor is ahead of the log.
		if jerr := p.jump(); jerr != nil {
			return jerr
		}
		return p.ticked()
	}
	var failed error
	for _, ev := range evs {
		seq, err := sequence(p.org, ev.ID)
		if err != nil {
			p.w.warn("agentwatch: skip an event", err)
			continue
		}
		if failed = p.event(ev); failed != nil {
			break
		}
		p.st.EventCursor = max(p.st.EventCursor, seq)
	}
	if failed != nil {
		// The tick isn't recorded: a page that never applies leaves the
		// watcher to jump past it once it's OffForLong.
		if err := p.save(); err != nil {
			return err
		}
		return failed
	}
	return p.ticked()
}

// ticked records the pass's tick with the cursor, once the events are
// read, so a later step that keeps failing doesn't make the next pass
// look like one after a long pause and jump past events.
func (p *pass) ticked() error {
	now := p.now
	p.st.TickedAt = &now
	return p.save()
}

// event applies one event's signals and clearances. A detector error is
// logged and the event skipped; a report store error stops the page, so
// the event is read again next pass.
func (p *pass) event(ev realtime.Event) error {
	sigs, clears, err := p.detector.FromEvent(p.ctx, ev)
	if err != nil {
		p.w.warn("agentwatch: skip event "+ev.ID, err)
		return nil
	}
	return p.apply(sigs, clears)
}

func (p *pass) apply(sigs []agentreports.Signal, clears []agentreports.Clearance) error {
	for _, sig := range sigs {
		if _, err := p.w.Reports.Observe(p.ctx, sig); err != nil {
			if errors.Is(err, agentreports.ErrInvalid) {
				p.w.warn("agentwatch: skip a "+sig.Kind+" signal", err)
				continue
			}
			return fmt.Errorf("record a %s signal: %w", sig.Kind, err)
		}
	}
	for _, c := range clears {
		if _, err := p.w.Reports.Clear(p.ctx, c); err != nil {
			if errors.Is(err, agentreports.ErrInvalid) {
				p.w.warn("agentwatch: skip a clearance", err)
				continue
			}
			return fmt.Errorf("clear reports: %w", err)
		}
	}
	return nil
}

// sweep checks the production bindings every SweepEvery and rechecks the
// open reports every RecheckEvery.
func (p *pass) sweep() error {
	changed := false
	if due(p.st.SweptAt, p.now, SweepEvery) {
		sigs, clears, checks, err := p.detector.Sweep(p.ctx, p.org, p.st.Checks)
		if err != nil {
			p.w.warn("agentwatch: sweep "+p.org, err)
		} else {
			if err := p.apply(sigs, clears); err != nil {
				return err
			}
			p.st.Checks = checks
		}
		now := p.now
		p.st.SweptAt, changed = &now, true
	}
	if !p.rechecked && due(p.st.RecheckedAt, p.now, RecheckEvery) {
		if err := p.recheck(); err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return p.save()
}

func due(last *time.Time, now time.Time, every time.Duration) bool {
	return last == nil || now.Sub(*last) >= every
}

// recheck clears each open report whose signal no longer holds. Manual
// reports stay: nothing Loomplane reads says they're over.
func (p *pass) recheck() error {
	open, err := p.w.Reports.Open(p.ctx, p.org, "", "")
	if err != nil {
		return fmt.Errorf("read the open reports: %w", err)
	}
	for _, r := range open {
		if manual(r) {
			continue
		}
		holds, err := p.detector.Recheck(p.ctx, r)
		if err != nil {
			p.w.warn("agentwatch: recheck report "+r.ID, err)
			continue
		}
		if holds {
			continue
		}
		c := agentreports.Clearance{
			OrgID: r.OrgID, ProjectID: r.ProjectID, EnvironmentID: r.EnvironmentID,
			Signals: []string{r.Signal}, Components: []string{r.Component},
			Reason: r.Signal + " no longer holds", At: p.now,
		}
		if err := p.apply(nil, []agentreports.Clearance{c}); err != nil {
			return err
		}
	}
	now := p.now
	p.st.RecheckedAt, p.rechecked = &now, true
	return nil
}

func manual(r *agentreports.Report) bool {
	return r.Signal == agentreports.SignalManual || r.OpenedBy != "" || r.OpenedByTask != ""
}

// investigate starts an investigation for each new open report, newest
// first, while the budget and the concurrency cap allow.
func (p *pass) investigate() error {
	open, err := p.w.Reports.Open(p.ctx, p.org, "", "")
	if err != nil {
		return fmt.Errorf("read the open reports: %w", err)
	}
	busy, err := p.w.Reports.Investigating(p.ctx, p.org)
	if err != nil {
		return fmt.Errorf("read the running investigations: %w", err)
	}
	running := len(busy)
	slices.Reverse(open)
	for _, r := range open {
		if r.Investigation != agentreports.InvestigationNone {
			continue
		}
		if manual(r) {
			if serr := p.move(r, []string{agentreports.InvestigationNone}, agentreports.Investigation{Status: agentreports.InvestigationSkipped}); serr != nil {
				return serr
			}
			continue
		}
		if !p.st.Paused && running < MaxConcurrentInvestigations {
			switch berr := p.w.Budget.RequireBudget(p.ctx, p.org); {
			case errors.Is(berr, agentsettings.ErrBudgetExhausted):
				if serr := p.pause(); serr != nil {
					return serr
				}
			case berr != nil:
				// Try again next pass: the budget couldn't be read.
				p.w.warn("agentwatch: read the AI budget of "+p.org, berr)
				return nil
			}
		}
		switch {
		case p.st.Paused:
			err = p.move(r, []string{agentreports.InvestigationNone}, agentreports.Investigation{Status: agentreports.InvestigationSkipped, Note: noteBudget})
		case running >= MaxConcurrentInvestigations:
			err = p.move(r, []string{agentreports.InvestigationNone}, agentreports.Investigation{Status: agentreports.InvestigationSkipped, Note: noteCapped})
		default:
			var started bool
			started, err = p.start(r)
			if started {
				running++
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// move sets a report's investigation, when it's still in from.
func (p *pass) move(r *agentreports.Report, from []string, inv agentreports.Investigation) error {
	inv.Note = note(inv.Note)
	if _, err := p.w.Reports.SetInvestigation(p.ctx, p.org, r.ID, from, inv); err != nil {
		return fmt.Errorf("move the investigation of report %s: %w", r.ID, err)
	}
	return nil
}

func note(s string) string {
	if utf8.RuneCountInString(s) <= maxNoteRunes {
		return s
	}
	return string([]rune(s)[:maxNoteRunes])
}

// start claims a report's investigation and starts it as the agent alone.
// started is whether a run was queued.
func (p *pass) start(r *agentreports.Report) (started bool, err error) {
	claimed, err := p.w.Reports.SetInvestigation(p.ctx, p.org, r.ID, []string{agentreports.InvestigationNone},
		agentreports.Investigation{Status: agentreports.InvestigationQueued})
	if err != nil || !claimed {
		return false, err
	}
	queued := []string{agentreports.InvestigationQueued}
	task, err := p.w.Runs.Start(p.ctx, agentruns.StartInput{
		Kind: agentruns.KindInvestigation, OrgID: p.org, OnBehalfOf: nil,
		ProjectID: r.ProjectID, EnvironmentID: r.EnvironmentID,
		Input:    InvestigationInput(p.ctx, p.w.Safety, r, p.projectName(r.ProjectID)),
		Metadata: map[string]string{agentruns.MetaReportID: r.ID, agentruns.MetaFingerprint: r.Fingerprint},
	})
	switch {
	case errors.Is(err, agentsettings.ErrBudgetExhausted):
		if serr := p.pause(); serr != nil {
			return false, serr
		}
		return false, p.move(r, queued, agentreports.Investigation{Status: agentreports.InvestigationSkipped, Note: noteBudget})
	case err != nil:
		p.w.warn("agentwatch: start the investigation of report "+r.ID, err)
		return false, p.move(r, queued, agentreports.Investigation{Status: agentreports.InvestigationFailed, Note: noteNotStarted})
	}
	// The run is live now. Losing its run ID here would leave follow()
	// nothing to follow it by, so the move is tried again before giving
	// up; even then the run's findings still land on the queued report.
	running := agentreports.Investigation{Status: agentreports.InvestigationRunning, TaskID: task.ID, RunID: task.RunID}
	for attempt := 1; ; attempt++ {
		err = p.move(r, queued, running)
		if err == nil || attempt == moveAttempts {
			return true, err
		}
	}
}

// moveAttempts is how many times start records a live run on its report.
const moveAttempts = 3

// projectName is the project's name for the investigation's input, or ""
// when it can't be read.
func (p *pass) projectName(id string) string {
	if p.detector.World == nil {
		return ""
	}
	proj, err := p.detector.World.Project(p.ctx, p.org, id)
	if err != nil {
		p.w.warn("agentwatch: read the name of project "+id, err)
		return ""
	}
	return proj.Name
}

// follow moves each running investigation on once its task finished. A
// report that resolved meanwhile still gets its findings.
func (p *pass) follow() error {
	list, err := p.w.Reports.Investigating(p.ctx, p.org)
	if err != nil {
		return fmt.Errorf("read the running investigations: %w", err)
	}
	running := []string{agentreports.InvestigationRunning}
	for _, r := range list {
		if r.RunID == "" {
			// Queued by a pass that stopped before it started the run, or
			// before it recorded the run it started: nothing to follow it
			// by, so it waits out the ceiling. A run that did start may
			// have written its findings meanwhile.
			if p.overdue(r) {
				inv := agentreports.Investigation{Status: agentreports.InvestigationFailed, Note: noteUnfinished}
				if wrote(r) {
					inv = agentreports.Investigation{Status: agentreports.InvestigationDone}
				}
				if err := p.move(r, []string{r.Investigation}, inv); err != nil {
					return err
				}
			}
			continue
		}
		task, err := p.w.Runs.ByRun(p.ctx, p.org, r.RunID)
		if errors.Is(err, agentruns.ErrNotFound) {
			if serr := p.move(r, running, agentreports.Investigation{Status: agentreports.InvestigationFailed, Note: "The investigation's task is gone."}); serr != nil {
				return serr
			}
			continue
		}
		if err != nil {
			p.w.warn("agentwatch: read the investigation of report "+r.ID, err)
			continue
		}
		switch task.Status {
		case agentruns.StatusQueued, agentruns.StatusRunning, agentruns.StatusWaiting:
			// Still going. One that outlives the ceiling frees its slot.
			if p.overdue(r) {
				if err := p.move(r, running, agentreports.Investigation{Status: agentreports.InvestigationFailed, Note: noteUnfinished}); err != nil {
					return err
				}
			}
		case agentruns.StatusCompleted:
			if err := p.move(r, running, agentreports.Investigation{Status: agentreports.InvestigationDone}); err != nil {
				return err
			}
		case agentruns.StatusFailed, agentruns.StatusCancelled:
			if task.Error != "" {
				p.w.warn("agentwatch: the investigation of report "+r.ID+" ended "+task.Status, errors.New(task.Error))
			}
			if err := p.move(r, running, agentreports.Investigation{Status: agentreports.InvestigationFailed, Note: noteStopped}); err != nil {
				return err
			}
			// A run that ran out of budget pauses the rest.
			if berr := p.w.Budget.RequireBudget(p.ctx, p.org); errors.Is(berr, agentsettings.ErrBudgetExhausted) {
				if err := p.pause(); err != nil {
					return err
				}
			} else if berr != nil {
				p.w.warn("agentwatch: read the AI budget of "+p.org, berr)
			}
		}
	}
	return nil
}

// overdue reports whether a report's investigation has been queued or
// running for longer than InvestigationCeiling.
func (p *pass) overdue(r *agentreports.Report) bool {
	return r.InvestigationStartedAt != nil && p.now.Sub(*r.InvestigationStartedAt) > InvestigationCeiling
}

// wrote reports whether the report's findings came from the investigation
// in hand, written after it was queued.
func wrote(r *agentreports.Report) bool {
	return r.Findings != nil && r.InvestigationStartedAt != nil && !r.Findings.WrittenAt.Before(*r.InvestigationStartedAt)
}

// deliver queues the delivery of each report whose notice or issue is
// behind, once its investigation finished or had InvestigationGrace to.
// A report behind only on an issue that waits for its installation goes
// again once the installation accepted Issues; one with a notice to send
// goes now.
func (p *pass) deliver() error {
	pending, err := p.w.Reports.Pending(p.ctx, p.org)
	if err != nil {
		return fmt.Errorf("read the reports to deliver: %w", err)
	}
	accepted := map[string]bool{}
	for _, r := range pending {
		if !p.ready(r) {
			continue
		}
		// A report behind only on an issue that waits for its
		// installation goes again once the installation accepted Issues.
		// One with a notice to send, such as a repeat, goes now.
		if !r.NoticeBehind && r.Status == agentreports.StatusOpen && r.Issue.State == agentreports.IssueWaiting &&
			!p.accepted(r.Repository, accepted) {
			continue
		}
		queued, err := p.w.Reports.QueueDelivery(p.ctx, p.org, r.ID, p.now)
		if err != nil {
			return fmt.Errorf("queue the delivery of report %s: %w", r.ID, err)
		}
		if !queued {
			continue
		}
		if err := p.w.Jobs.Deliver(p.ctx, p.org, r.ID); err != nil {
			// Queued but not enqueued: a later pass queues it again after
			// DeliveryRequeue.
			p.w.warn("agentwatch: queue the delivery job of report "+r.ID, err)
		}
	}
	return nil
}

// ready reports whether a report's investigation is out of delivery's way.
func (p *pass) ready(r *agentreports.Report) bool {
	if r.Investigation != agentreports.InvestigationQueued && r.Investigation != agentreports.InvestigationRunning {
		return true
	}
	return r.InvestigationStartedAt == nil || p.now.Sub(*r.InvestigationStartedAt) > InvestigationGrace
}

// accepted reports whether repo's installation accepts Issues now, once
// per repository a pass.
func (p *pass) accepted(repo string, seen map[string]bool) bool {
	if p.w.Issues == nil || repo == "" {
		return false
	}
	if ok, read := seen[repo]; read {
		return ok
	}
	access, err := p.w.Issues.Access(p.ctx, p.org, repo)
	if err != nil {
		p.w.warn("agentwatch: read whether "+repo+" accepts issues", err)
	}
	seen[repo] = err == nil && access.Accepted
	return seen[repo]
}

// pause stops investigations until the budget has room: once, with one
// notice to the owners and admins saved with the state.
func (p *pass) pause() error {
	if p.st.Paused {
		return nil
	}
	now := p.now
	p.st.Paused, p.st.PausedReason, p.st.PausedAt = true, PauseBudget, &now
	if err := p.notifyAt("watcher-paused", p.st.PausedAt, pausedTitle, pausedBody); err != nil {
		return err
	}
	// Recorded once the pause is saved, so the audit never says paused
	// when the state doesn't.
	p.audit("agent.watcher.pause")
	return nil
}

// resume starts investigating again once the budget has room.
func (p *pass) resume() error {
	err := p.w.Budget.RequireBudget(p.ctx, p.org)
	if errors.Is(err, agentsettings.ErrBudgetExhausted) {
		return nil
	}
	if err != nil {
		p.w.warn("agentwatch: read the AI budget of "+p.org, err)
		return nil
	}
	pausedAt := p.st.PausedAt
	p.st.Paused, p.st.PausedReason, p.st.PausedAt = false, "", nil
	if err := p.notifyAt("watcher-resumed", pausedAt, resumedTitle, resumedBody); err != nil {
		return err
	}
	p.audit("agent.watcher.resume")
	return nil
}

// notifyAt saves the state with one budget notice to the owners and
// admins, its subject naming the pause it's about.
func (p *pass) notifyAt(subject string, pausedAt *time.Time, title, body string) error {
	var admins []string
	if p.w.DB != nil {
		var err error
		if admins, err = notifications.Admins(p.ctx, p.w.DB, p.org); err != nil {
			return fmt.Errorf("read the workspace's admins: %w", err)
		}
	}
	at := p.now
	if pausedAt != nil {
		at = *pausedAt
	}
	n := notifications.Notice{
		Kind: notifications.KindAgentBudget, OrgID: p.org,
		SubjectID:  fmt.Sprintf("%s:%s:%d", subject, p.org, at.Unix()),
		Recipients: admins, Title: title, Body: body, Path: settingsPath, Need: security.AIManage.Name(),
	}
	return p.w.Store.SaveNotifying(p.ctx, p.owner, p.st, func(ctx context.Context, q notifications.Execer) error {
		return notifications.Enqueue(ctx, q, n)
	})
}

func (p *pass) audit(action string) {
	if p.w.Audit == nil {
		return
	}
	p.w.Audit.Record(context.WithoutCancel(p.ctx), projects.AuditEvent{
		Action: action, Severity: "info", Principal: security.AgentPrincipal(p.org, nil),
		Attributes: map[string]any{"reason": PauseBudget},
	})
}

// Status is where a workspace's watcher stands, for its people.
type Status struct {
	Paused       bool                      `json:"paused"`
	PausedReason string                    `json:"paused_reason,omitempty"`
	PausedAt     *time.Time                `json:"paused_at,omitempty"`
	TickedAt     *time.Time                `json:"ticked_at,omitempty"`
	Issues       repositories.IssuesStatus `json:"issues"`
}

// Status returns where p's workspace's watcher stands: anyone who may use
// the agent may read it.
func (w *Watcher) Status(ctx context.Context, p security.Principal) (*Status, error) {
	if w.Authz == nil {
		return nil, errors.New("agentwatch: no authorizer is configured")
	}
	if err := w.Authz.Require(ctx, p, security.AgentUse, ""); err != nil {
		if errors.Is(err, security.ErrNotMember) || errors.Is(err, security.ErrUnauthenticated) {
			return nil, fmt.Errorf("%w: %w", security.ErrForbidden, err)
		}
		return nil, err
	}
	st, err := w.Store.Get(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	out := &Status{Paused: st.Paused, PausedReason: st.PausedReason, PausedAt: st.PausedAt, TickedAt: st.TickedAt,
		Issues: repositories.IssuesStatus{Installations: []repositories.InstallationIssues{}}}
	if w.Issues != nil {
		is, err := w.Issues.IssuesStatus(ctx, p)
		if err != nil {
			return nil, err
		}
		if is != nil {
			out.Issues = *is
		}
	}
	return out, nil
}
