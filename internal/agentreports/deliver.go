package agentreports

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

const noRepository = "Loomplane couldn't tell which repository this belongs to"

// Rendered are a report's texts as they leave Loomplane: known secrets
// redacted, then scanned on the way out, with the fixed fallback for any
// text the scan blocked.
type Rendered struct{ NoticeTitle, NoticeBody, IssueTitle, IssueBody string }

// Pending returns the reports whose notice or issue is behind, and the
// open ones waiting for an installation to accept Issues.
func (s *Service) Pending(ctx context.Context, orgID string) ([]*Report, error) {
	if orgID == "" {
		return nil, invalid("no workspace")
	}
	return s.d.Store.Pending(ctx, orgID)
}

// QueueDelivery marks a report's delivery queued, unless it already was
// within DeliveryRequeue, and reports whether the caller should enqueue
// the delivery job.
func (s *Service) QueueDelivery(ctx context.Context, orgID, id string, now time.Time) (bool, error) {
	if orgID == "" || id == "" {
		return false, invalid("no workspace or report")
	}
	return s.d.Store.QueueDelivery(ctx, orgID, id, now.UTC())
}

// Text returns a report's texts as delivery would send them, for the
// agent's knowledge and for tests. While the workspace's agent is off it
// returns a zero Rendered and no error: nothing can be scanned then.
func (s *Service) Text(ctx context.Context, r *Report) (Rendered, error) {
	bound, on, err := s.bind(ctx, r.OrgID)
	if err != nil || !on {
		return Rendered{}, err
	}
	out, err := s.sender(bound, r)
	if err != nil {
		return Rendered{}, err
	}
	n := s.names(bound, r, out)
	title, body := s.noticeTexts(bound, out, r, "", n)
	issueTitle, issueText := s.issueTexts(bound, out, r, n)
	return Rendered{NoticeTitle: title, NoticeBody: body, IssueTitle: issueTitle, IssueBody: issueText}, nil
}

// Deliver is the delivery job's body: it sends a report's notice through
// Herald and brings its GitHub issue up to date. It does nothing for a
// report another worker is delivering, or while the workspace's agent is
// off, and running it again after it succeeded does nothing. A worker
// that loses the delivery to another stops quietly. An error means
// Dispatch should retry; the notice, once queued, is never queued again.
func (s *Service) Deliver(ctx context.Context, orgID, id, owner string) error {
	err := s.deliver(ctx, orgID, id, owner)
	if errors.Is(err, ErrLeaseLost) {
		return nil // the worker that holds it now finishes it
	}
	return err
}

func (s *Service) deliver(ctx context.Context, orgID, id, owner string) error {
	if orgID == "" || id == "" || owner == "" {
		return invalid("a delivery names its workspace, report and owner")
	}
	d, err := s.d.Store.ClaimDelivery(ctx, orgID, id, owner, DeliveryLease)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil || d == nil {
		return err
	}
	done := false
	defer func() {
		if rerr := s.d.Store.ReleaseDelivery(context.WithoutCancel(ctx), orgID, id, owner, done); rerr != nil {
			s.warn("agentreports: release the delivery of report "+id, rerr)
		}
	}()
	bound, on, err := s.bind(ctx, orgID)
	if err != nil {
		return err
	}
	if !on {
		done = true
		return nil
	}
	out, err := s.sender(bound, d.Report)
	if err != nil {
		return err
	}
	if d, err = s.deliverNotice(bound, out, d, owner); err != nil || d == nil {
		return err
	}
	if ierr := s.deliverIssue(bound, out, d, owner); ierr != nil {
		return ierr
	}
	done = true
	return nil
}

// bind binds ctx to the workspace as the agent alone. on is false when
// the workspace's agent is off.
func (s *Service) bind(ctx context.Context, orgID string) (bound context.Context, on bool, err error) {
	if s.d.Binder == nil {
		return ctx, false, errors.New("agentreports: no binder is configured")
	}
	bound, err = s.d.Binder.Bind(ctx, security.AgentPrincipal(orgID, nil), agentscope.Scope{OrgID: orgID})
	if errors.Is(err, agentsettings.ErrDisabled) {
		return ctx, false, nil
	}
	return bound, err == nil, err
}

// deliverNotice sends the pending notice, or just records that the
// revision needs none, before any GitHub call. A report that moves on
// meanwhile is read again, at most three times. It returns nil, with no
// error, when another worker took the delivery over, or when the report
// kept moving: then nothing goes to GitHub for a revision whose notice
// isn't recorded, and the next delivery catches up.
func (s *Service) deliverNotice(ctx context.Context, out *sender, d *Delivery, owner string) (*Delivery, error) {
	for range 3 {
		r := d.Report
		if r.Revision <= d.NotifiedRevision {
			return d, nil
		}
		var send func(context.Context, notifications.Execer, int) error
		if d.PendingNotice != "" {
			n := s.names(ctx, r, out)
			title, body := s.noticeTexts(ctx, out, r, d.PendingNotice, n)
			recipients, err := s.recipients(ctx, r)
			if err != nil {
				return nil, err
			}
			notice := notifications.Notice{
				Kind: notifications.KindAgentReport, OrgID: r.OrgID, ProjectID: r.ProjectID, Recipients: recipients,
				Title: title, Body: body, Path: "/agent/reports/" + r.ID, Need: security.DeploymentRead.Name(),
				Metadata: map[string]string{"report_id": r.ID, "signal": r.Signal, "severity": r.Severity, "environment": r.Environment},
			}
			send = func(ctx context.Context, q notifications.Execer, seq int) error {
				notice.SubjectID = r.ID + ":" + strconv.Itoa(seq)
				return notifications.Enqueue(ctx, q, notice)
			}
		}
		saved, err := s.d.Store.SaveNotice(ctx, r.OrgID, r.ID, owner, r.Revision, d.PendingNotice, send, s.now())
		if err != nil {
			return nil, err
		}
		if saved {
			d.NotifiedRevision, d.PendingNotice = r.Revision, ""
			return d, nil
		}
		if d, err = s.d.Store.ClaimDelivery(ctx, r.OrgID, r.ID, owner, DeliveryLease); err != nil || d == nil {
			return nil, err
		}
	}
	return nil, nil
}

// recipients are who hears about a report: the project's responsible team,
// or the workspace's owners and admins when the project has none (or the
// report has no project), and the person who started the deployment it's
// about, each once. teams.Resolve makes that choice, as it does for the
// agent's requests.
func (s *Service) recipients(ctx context.Context, r *Report) ([]string, error) {
	if s.d.DB == nil {
		return nil, errors.New("agentreports: no database to read recipients from")
	}
	var out []string
	add := func(users ...string) {
		for _, u := range users {
			if u != "" && !slices.Contains(out, u) {
				out = append(out, u)
			}
		}
	}
	if s.d.Teams != nil {
		route, err := s.d.Teams.Resolve(ctx, s.d.DB, r.OrgID, r.ProjectID, "", "")
		if err != nil {
			return nil, err
		}
		add(route.Members...)
	} else {
		admins, err := notifications.Admins(ctx, s.d.DB, r.OrgID)
		if err != nil {
			return nil, err
		}
		add(admins...)
	}
	if s.d.Starters != nil && r.Evidence.DeploymentID != "" {
		by, err := s.d.Starters.StartedBy(ctx, r.OrgID, r.Evidence.DeploymentID)
		if err != nil {
			s.warn("agentreports: read who started deployment "+r.Evidence.DeploymentID, err)
		} else if person(by) {
			add(by)
		}
	}
	return out, nil
}

// person is a subject that is a person: not the agent, not Loomplane's
// automation.
func person(subject string) bool {
	if subject == "" {
		return false
	}
	if _, ok := security.IsAgentSubject(subject); ok {
		return false
	}
	_, ok := security.IsSystemSubject(subject)
	return !ok
}

// issueStep is where one run of the issue step has got to.
type issueStep struct {
	s     *Service
	ctx   context.Context
	out   *sender
	d     *Delivery
	owner string
	n     names
	clean *Report // the report with its texts scrubbed, for comments
	save  IssueSave
}

// deliverIssue brings the report's GitHub issue up to date.
func (s *Service) deliverIssue(ctx context.Context, out *sender, d *Delivery, owner string) error {
	r := d.Report
	waiting := r.Issue.State == IssueWaiting && r.Status == StatusOpen
	if r.Revision <= d.IssueRevision && !waiting {
		return nil
	}
	st := &issueStep{
		s: s, ctx: ctx, out: out, d: d, owner: owner, n: s.names(ctx, r, out), clean: out.clean(r),
		save: IssueSave{Number: r.Issue.Number, URL: r.Issue.URL, State: r.Issue.State},
	}
	if r.Repository == "" {
		return st.finish(IssueNoRepository, noRepository)
	}
	if s.d.Issues == nil {
		return st.finish(IssueNoRepository, "This Loomplane has no GitHub App, so reports go to Herald only")
	}
	iss, err := s.d.Issues.Issues(ctx, r.OrgID, r.Repository)
	switch {
	case errors.Is(err, repositories.ErrIssuesNotAccepted):
		return st.finish(IssueWaiting, reason(err, repositories.ErrIssuesNotAccepted))
	case errors.Is(err, repositories.ErrNoInstallation):
		return st.finish(IssueNoRepository, reason(err, repositories.ErrNoInstallation))
	case err != nil:
		return st.fail(err)
	}
	if err = st.sync(iss); err != nil {
		return st.fail(err)
	}
	st.save.Error, st.save.Done = "", true
	return s.d.Store.SaveIssue(ctx, r.OrgID, r.ID, owner, r.Revision, st.save)
}

// reason is the person-facing reason an Issues error carries.
func reason(err, sentinel error) string {
	return strings.TrimPrefix(err.Error(), sentinel.Error()+": ")
}

// finish ends the issue step without GitHub: the state and why, queued
// comments dropped, the revision delivered.
func (st *issueStep) finish(state, why string) error {
	r := st.d.Report
	st.save.State, st.save.Error = state, why
	st.save.Handled, st.save.Done = len(st.d.IssueComments), true
	return st.s.d.Store.SaveIssue(st.ctx, r.OrgID, r.ID, st.owner, r.Revision, st.save)
}

// fail keeps what the step did before err, so a retry neither opens a
// second issue nor posts a comment twice, and returns err for Dispatch.
func (st *issueStep) fail(err error) error {
	r := st.d.Report
	st.save.State, st.save.Error, st.save.Done = IssueFailed, failure(err), false
	if errors.Is(err, ErrLeaseLost) {
		return err // the issue is another worker's to record now
	}
	if serr := st.s.d.Store.SaveIssue(st.ctx, r.OrgID, r.ID, st.owner, r.Revision, st.save); serr != nil {
		st.s.warn("agentreports: record the failed issue step of report "+r.ID, serr)
	}
	return err
}

// failure is a GitHub error as a person reads it, without a token or a
// URL in it.
func failure(err error) string {
	msg := err.Error()
	if errors.Is(err, repositories.ErrRateLimited) {
		return "GitHub rate-limited Loomplane; it will try again"
	}
	return cutRunes(urlPasswords.ReplaceAllString(msg, "${1}[redacted]@"), maxReasonRunes)
}

// sync makes the issue say what the report says, then posts the queued
// comments and transitions.
func (st *issueStep) sync(iss repositories.Issues) error {
	r := st.d.Report
	title, body := st.s.issueTexts(st.ctx, st.out, r, st.n)
	if err := iss.EnsureLabel(st.ctx, Label, LabelColor, LabelDescription); err != nil {
		return err
	}
	if st.save.Number > 0 {
		got, err := iss.Edit(st.ctx, st.save.Number, repositories.IssueEdit{Title: &title, Body: &body})
		switch {
		case errors.Is(err, repositories.ErrNotFound):
			st.save.Number, st.save.URL = 0, ""
		case err != nil:
			return err
		default:
			st.saw(got, "edited")
		}
	}
	created := false
	if st.save.Number == 0 {
		found, err := iss.Find(st.ctx, Label, Marker(r.ID))
		if err != nil {
			return err
		}
		switch {
		case found != nil:
			st.saw(found, "")
			got, err := iss.Edit(st.ctx, found.Number, repositories.IssueEdit{Title: &title, Body: &body})
			if err != nil {
				return err
			}
			st.saw(got, "edited")
		case r.Status != StatusOpen:
			// A report that ended before it had an issue doesn't open one.
			st.save.State, st.save.Handled = IssueNone, len(st.d.IssueComments)
			return nil
		default:
			// Opening an issue is the one step a second worker must never
			// repeat. The lease is renewed first, only while it's still
			// this worker's, and the issue is opened only when the row
			// still records the issue this step started from (none, or
			// one GitHub just said is gone): another number means another
			// worker opened one meanwhile. It's recorded right after.
			held, err := st.s.d.Store.RenewDelivery(st.ctx, r.OrgID, r.ID, st.owner, DeliveryLease)
			if err != nil {
				return err
			}
			if held.Report.Issue.Number != r.Issue.Number {
				return ErrLeaseLost
			}
			got, err := iss.Create(st.ctx, repositories.IssueInput{Title: title, Body: body, Labels: []string{Label}})
			if err != nil {
				return err
			}
			st.saw(got, "created")
			created = true
			if serr := st.s.d.Store.SaveIssue(st.ctx, r.OrgID, r.ID, st.owner, r.Revision, st.save); serr != nil {
				return serr
			}
		}
	}
	for _, kind := range st.d.IssueComments {
		// A new issue already says everything queued before it.
		if created {
			st.save.Handled++
			continue
		}
		posted, err := st.post(iss, kind)
		if err != nil {
			if posted {
				// The comment is out; only its close is left, as a step
				// of its own.
				st.save.Handled++
				st.save.Requeue = []string{closePrefix + closeReason[kind]}
			}
			return err
		}
		st.save.Handled++
	}
	return nil
}

// A close left over from a comment that went out, queued as its own step:
// "close:" and the state reason.
const closePrefix = "close:"

// closeReason is the state reason a comment closes the issue with.
var closeReason = map[string]string{commentResolved: "completed", commentClosed: "not_planned"}

// saw records the issue as GitHub returned it, and audits action.
func (st *issueStep) saw(is *repositories.Issue, action string) {
	st.save.Number, st.save.URL = is.Number, is.URL
	switch is.State {
	case "open":
		st.save.State = IssueOpen
	case "closed":
		st.save.State = IssueClosed
	}
	if action != "" {
		st.s.auditIssue(st.ctx, st.d.Report, is.Number, action)
	}
}

// post posts one queued comment with its transition: a reopen opens the
// issue before its comment, a resolve or a close closes it after. posted
// is whether the comment went out, even when the close after it failed.
func (st *issueStep) post(iss repositories.Issues, kind string) (posted bool, err error) {
	if why, ok := strings.CutPrefix(kind, closePrefix); ok {
		return false, st.close(iss, why)
	}
	r, number := st.d.Report, st.save.Number
	now := st.s.now()
	if kind == commentRepeated {
		if last := st.lastRepeat(); last != nil && last.After(now.Add(-RepeatNoticeEvery)) {
			return false, nil // the body's count says it
		}
	}
	if kind == commentReopened {
		got, err := iss.Edit(st.ctx, number, repositories.IssueEdit{State: "open", StateReason: "reopened"})
		if err != nil {
			return false, err
		}
		st.saw(got, "reopened")
	}
	if text, ok := comment(kind, st.clean, st.n); ok {
		if sent, ok := st.out.text(st.ctx, text); ok {
			if err := iss.Comment(st.ctx, number, sent); err != nil {
				return false, err
			}
			posted = true
			st.s.auditIssue(st.ctx, r, number, "commented")
			if kind == commentRepeated {
				st.save.CommentAt = &now
			}
		} else {
			st.s.warn("agentreports: the safety scan blocked a comment on report "+r.ID, agentsafety.ErrOutboundBlocked)
		}
	}
	if why, ok := closeReason[kind]; ok {
		return posted, st.close(iss, why)
	}
	return posted, nil
}

// close closes the issue with why as its state reason. Closing a closed
// issue again does no harm, so a retry can repeat it.
func (st *issueStep) close(iss repositories.Issues, why string) error {
	got, err := iss.Edit(st.ctx, st.save.Number, repositories.IssueEdit{State: "closed", StateReason: why})
	if err != nil {
		return err
	}
	st.saw(got, "closed")
	return nil
}

func (st *issueStep) lastRepeat() *time.Time {
	if st.save.CommentAt != nil {
		return st.save.CommentAt
	}
	return st.d.IssueCommentAt
}

// auditIssue records a change Loomplane made on GitHub.
func (s *Service) auditIssue(ctx context.Context, r *Report, number int, action string) {
	if s.d.Audit == nil {
		return
	}
	s.d.Audit.Record(context.WithoutCancel(ctx), projects.AuditEvent{
		Action: "agent.report.issue", Severity: "info", ProjectID: r.ProjectID, Principal: security.AgentPrincipal(r.OrgID, nil),
		Attributes: map[string]any{"report_id": r.ID, "repository": r.Repository, "number": number, "action": action},
	})
}

// names are what r's texts call the things around it.
func (s *Service) names(ctx context.Context, r *Report, out *sender) names {
	n := names{Project: r.ProjectID, Link: strings.TrimRight(s.d.PublicURL, "/") + "/agent/reports/" + r.ID}
	if s.d.ProjectReader != nil {
		p, err := s.d.ProjectReader.GetProject(ctx, r.OrgID, r.ProjectID)
		switch {
		case err != nil:
			s.warn("agentreports: read the name of project "+r.ProjectID, err)
		case p.Name != "":
			n.Project = p.Name
		}
	}
	switch _, system := security.IsSystemSubject(r.ClosedBy); {
	case r.ClosedBy == "":
	case system:
		n.Closer = "Loomplane automation"
	case !person(r.ClosedBy):
		n.Closer = security.AgentActorName
	case s.d.People != nil:
		n.Closer = s.d.People.Name(ctx, r.ClosedBy)
	}
	n.Project, n.Closer = out.scrub(n.Project), out.scrub(n.Closer)
	return n
}

// noticeTexts are the notice's title and body for pending.
func (s *Service) noticeTexts(ctx context.Context, out *sender, r *Report, pending string, n names) (title, body string) {
	clean := out.clean(r)
	var ok bool
	if title, ok = out.text(ctx, noticeTitle(clean, pending, n)); !ok {
		title = fallbackTitle(n)
	}
	if body, ok = out.text(ctx, noticeBody(clean, pending)); !ok {
		body = "Open it in Loomplane to read it."
	}
	return cutRunes(title, MaxNoticeTitle), cutRunes(body, MaxNoticeBody)
}

// issueTexts are the issue's title and body. A body that lost its
// marker or outgrew GitHub on the way out is the fallback, which keeps
// the marker.
func (s *Service) issueTexts(ctx context.Context, out *sender, r *Report, n names) (title, body string) {
	clean := out.clean(r)
	var ok bool
	if title, ok = out.text(ctx, cutRunes(clean.Title, MaxNoticeTitle)); !ok || title == "" {
		title = fallbackTitle(n)
	}
	body, ok = out.textWith(ctx, issueBody(clean, n, true), issueLinks(clean, n))
	if !ok || !strings.HasPrefix(body, Marker(r.ID)+"\n") || len(body) > MaxIssueBody {
		body = Marker(r.ID) + "\n\nLoomplane opened a report for this repository. Read it in Loomplane: " + n.Link
	}
	return title, body
}

func fallbackTitle(n names) string {
	return cutRunes("Loomplane opened a report for "+n.Project, MaxNoticeTitle)
}

// sender is how one report's texts leave Loomplane: the report's known
// secret values and any URL password redacted, then the outbound scan.
type sender struct {
	scanner *agentsafety.Scanner
	redact  *realtime.Redactor
}

// sender reads r's secrets. A read that fails sends nothing: the caller
// returns the error and tries again later. Only a project, environment or
// secret that no longer exists is logged and passed over, since there's
// nothing of it left to leak; the outbound scan still runs.
func (s *Service) sender(ctx context.Context, r *Report) (*sender, error) {
	var values []string
	if s.d.Secrets != nil {
		v, err := s.d.Secrets.SecretValues(ctx, r.OrgID, r.ProjectID, r.Environment)
		if err != nil {
			if !gone(err) {
				return nil, fmt.Errorf("read the secrets to redact from report %s: %w", r.ID, err)
			}
			s.warn("agentreports: some secrets of report "+r.ID+" no longer exist", err)
		}
		values = v
	}
	return &sender{scanner: s.d.Safety, redact: realtime.NewRedactor(secretForms(values))}, nil
}

// gone is whether err says only that something no longer exists: the
// project, or a secret's variable. Every error at the end of every chain
// must, through wrappers and joins at any depth, so a join wrapped in
// one more error still fails closed when any part of it is something else.
func gone(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		parts := joined.Unwrap()
		for _, part := range parts {
			if !gone(part) {
				return false
			}
		}
		return len(parts) > 0
	}
	if wrapper, ok := err.(interface{ Unwrap() error }); ok {
		if inner := wrapper.Unwrap(); inner != nil {
			return gone(inner)
		}
	}
	return errors.Is(err, projects.ErrNotFound) || errors.Is(err, environments.ErrNotFound)
}

// secretForms adds the password of a URL-shaped secret, and its escaped
// forms, since an error message often quotes only that part.
func secretForms(values []string) []string {
	out := slices.Clone(values)
	for _, v := range values {
		if u, err := url.Parse(strings.TrimSpace(v)); err == nil && u.User != nil {
			if pw, ok := u.User.Password(); ok && pw != "" {
				out = append(out, pw, url.QueryEscape(pw), url.PathEscape(pw))
			}
		}
	}
	return out
}

// urlPasswords finds the password in scheme://user:password@host. The
// password runs to the last @ before the host, so one with an unencoded
// /, ?, # or @ in it is found whole. Where that's ambiguous, as with an @
// later in the URL, it takes more rather than less.
var urlPasswords = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^\s:/?#@]*:)\S+@`)

// scrub redacts the known secrets and URL passwords in text.
func (o *sender) scrub(text string) string {
	text, _ = o.redact.Line(text, len(text)+1)
	return urlPasswords.ReplaceAllString(text, "${1}[redacted]@")
}

// text is text as it may leave, and false when the scan blocked it.
func (o *sender) text(ctx context.Context, text string) (string, bool) {
	text = o.scrub(text)
	if text == "" {
		return "", true
	}
	sent, err := o.scanner.Outbound(ctx, text)
	if err != nil {
		return "", false
	}
	return sent, true
}

// textWith is text as it may leave, with links, Loomplane's own, kept out
// of the outbound scan: its entropy check takes a link with a port and an
// ID in it, such as http://localhost:8780/agent/reports/arep_..., for a
// secret and redacts it. A link is Loomplane's public URL and IDs it made,
// never anything read from outside, so it can't carry one. Text that
// already holds a placeholder is scanned whole.
func (o *sender) textWith(ctx context.Context, text string, links []string) (string, bool) {
	text = o.scrub(text)
	if text == "" {
		return "", true
	}
	held, restore := holdLinks(text, links)
	sent, err := o.scanner.Outbound(ctx, held)
	if err != nil {
		return "", false
	}
	return restore.Replace(sent), true
}

// linkHold starts the placeholder a held link takes during the scan.
const linkHold = "loomplane-link-"

// holdLinks swaps each link in text for a placeholder, longest first, and
// returns the replacer that puts them back.
func holdLinks(text string, links []string) (string, *strings.Replacer) {
	if strings.Contains(text, linkHold) {
		return text, strings.NewReplacer()
	}
	links = slices.Clone(links)
	slices.SortFunc(links, func(a, b string) int { return len(b) - len(a) })
	links = slices.Compact(links)
	var hold, restore []string
	for i, l := range links {
		if l == "" || !strings.Contains(text, l) {
			continue
		}
		ph := linkHold + strconv.Itoa(i) + "-held"
		hold, restore = append(hold, l, ph), append(restore, ph, l)
	}
	return strings.NewReplacer(hold...).Replace(text), strings.NewReplacer(restore...)
}

// issueLinks are the links r's issue body has to Loomplane: the report's
// own, and each fact's whose path Loomplane made of its own IDs.
func issueLinks(r *Report, n names) []string {
	links := []string{n.Link}
	for _, f := range r.Facts {
		if !f.Untrusted && ownPath.MatchString(f.Path) {
			links = append(links, baseOf(n.Link)+f.Path)
		}
	}
	return links
}

// ownPath is a dashboard path exactly as agentwatch's detectors make
// them: a plan's deployment page, maybe on its logs or changes tab. Any
// other path or query is scanned like the rest of the text.
var ownPath = regexp.MustCompile(`^/deployments/plan_[0-9a-z]{26}(\?tab=(logs|changes))?$`)

// clean is a copy of r with every text in it scrubbed, so what the
// renderer cuts to fit is what is sent.
func (o *sender) clean(r *Report) *Report {
	c := *r
	c.Title, c.Environment = o.scrub(r.Title), o.scrub(r.Environment)
	c.Resolution, c.InvestigationNote = o.scrub(r.Resolution), o.scrub(r.InvestigationNote)
	c.Facts = make([]Fact, len(r.Facts))
	for i, f := range r.Facts {
		f.Label, f.Value = o.scrub(f.Label), o.scrub(f.Value)
		c.Facts[i] = f
	}
	if r.Findings != nil {
		f := *r.Findings
		f.Summary, f.LikelyCause = o.scrub(f.Summary), o.scrub(f.LikelyCause)
		f.Suggestions = make([]string, len(r.Findings.Suggestions))
		for i, sug := range r.Findings.Suggestions {
			f.Suggestions[i] = o.scrub(sug)
		}
		c.Findings = &f
	}
	return &c
}
