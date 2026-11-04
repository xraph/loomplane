package agentreports

import (
	"fmt"
	"slices"
	"time"

	"go.jetify.com/typeid/v2"
)

const (
	ReopenWindow      = 24 * time.Hour // a signal back within this of resolving reopens the same report
	RepeatNoticeEvery = time.Hour
	MaxOccurrenceKeys = 50
)

// Issue comments a report queues for delivery, posted in order.
const (
	commentRepeated = "repeated"
	commentReopened = "reopened"
	commentFindings = "findings"
	commentResolved = "resolved"
	commentClosed   = "closed"
)

// Notices a report queues for delivery.
const (
	noticeOpened   = "opened"
	noticeReopened = "reopened"
	noticeRepeated = "repeated"
)

// Fingerprint is "<project>/<environment ID or ->/<signal>/<component>":
// what makes two signals the same incident.
func Fingerprint(projectID, environmentID, signal, component string) string {
	if environmentID == "" {
		environmentID = "-"
	}
	return projectID + "/" + environmentID + "/" + signal + "/" + component
}

func newID() (string, error) {
	id, err := typeid.Generate("arep")
	if err != nil {
		return "", fmt.Errorf("generate report id: %w", err)
	}
	return id.String(), nil
}

// record is a report as a Store keeps it: the Report and the delivery
// columns people never see. Every Store applies the same rules to it, so
// the in-memory store and PgStore can't drift apart.
type record struct {
	Report
	OccurrenceKeys   []string
	PendingNotice    string
	LastNoticeAt     *time.Time
	IssueComments    []string
	NotifiedRevision int
	IssueRevision    int
	NoticeSeq        int
	IssueCommentAt   *time.Time
}

// newRecord is a report for a signal no report holds.
func newRecord(id string, sig Signal, now time.Time) *record {
	r := &record{
		Report: Report{
			ID: id, OrgID: sig.OrgID, ProjectID: sig.ProjectID, EnvironmentID: sig.EnvironmentID, Environment: sig.Environment,
			Signal: sig.Kind, Component: sig.Component, Fingerprint: Fingerprint(sig.ProjectID, sig.EnvironmentID, sig.Kind, sig.Component),
			Status: StatusOpen, Severity: sig.Severity, Title: sig.Title, Facts: cloneFacts(sig.Facts), Evidence: cloneEvidence(sig.Evidence),
			Repository: sig.Repository, Occurrences: 1, FirstSeenAt: sig.At, LastSeenAt: sig.At,
			Investigation: InvestigationNone, Issue: Issue{State: IssueNone}, Revision: 1,
			OpenedBy: sig.OpenedBy, OpenedByTask: sig.OpenedByTask, CreatedAt: now, UpdatedAt: now,
		},
		OccurrenceKeys: []string{},
		PendingNotice:  noticeOpened,
		IssueComments:  []string{},
	}
	if sig.Occurrence != "" {
		r.OccurrenceKeys = []string{sig.Occurrence}
	}
	return r
}

// repeat applies a signal to the open report that holds its fingerprint.
// written is false when nothing changed at all.
func (r *record) repeat(sig Signal, now time.Time) (change Change, written bool) {
	if sig.Occurrence == "" {
		// A level signal is the same problem still there: only when it
		// was last seen moves.
		if !sig.At.After(r.LastSeenAt) {
			return ChangeUnchanged, false
		}
		r.LastSeenAt = sig.At
		return ChangeUnchanged, true
	}
	if slices.Contains(r.OccurrenceKeys, sig.Occurrence) {
		return ChangeUnchanged, false
	}
	r.addOccurrence(sig)
	r.Revision++
	r.UpdatedAt = now
	if !slices.Contains(r.IssueComments, commentRepeated) {
		r.IssueComments = append(r.IssueComments, commentRepeated)
	}
	// A notice not yet sent already says what a repeat would; otherwise
	// people hear about repeats at most hourly.
	if r.PendingNotice == "" && (r.LastNoticeAt == nil || r.LastNoticeAt.Before(now.Add(-RepeatNoticeEvery))) {
		r.PendingNotice = noticeRepeated
	}
	return ChangeRepeated, true
}

// reopen brings a report that resolved or closed within ReopenWindow back
// for its signal.
func (r *record) reopen(sig Signal, now time.Time) {
	r.Status = StatusOpen
	r.ResolvedAt, r.ClosedBy, r.Resolution = nil, "", ""
	r.TakenBy, r.TakenAt = "", nil
	r.Investigation, r.InvestigationNote = InvestigationNone, ""
	r.addOccurrence(sig)
	r.Revision++
	r.UpdatedAt = now
	r.PendingNotice = noticeReopened
	r.IssueComments = append(r.IssueComments, commentReopened)
}

// addOccurrence counts one more occurrence and keeps what the signal says
// now.
func (r *record) addOccurrence(sig Signal) {
	if sig.Occurrence != "" {
		r.OccurrenceKeys = append(r.OccurrenceKeys, sig.Occurrence)
		if n := len(r.OccurrenceKeys); n > MaxOccurrenceKeys {
			r.OccurrenceKeys = slices.Clone(r.OccurrenceKeys[n-MaxOccurrenceKeys:])
		}
	}
	r.Occurrences++
	if sig.At.After(r.LastSeenAt) {
		r.LastSeenAt = sig.At
	}
	r.Facts, r.Evidence = cloneFacts(sig.Facts), cloneEvidence(sig.Evidence)
}

// settle ends an open report: Clear resolves it, a person closes it.
func (r *record) settle(at time.Time, comment string) {
	r.ResolvedAt = &at
	r.Revision++
	r.UpdatedAt = at
	r.IssueComments = append(r.IssueComments, comment)
}

// manual reports are opened from a chat and only a person closes them.
func (r *record) manual() bool {
	return r.Signal == SignalManual || r.OpenedBy != "" || r.OpenedByTask != ""
}

// investigate moves the investigation; the Store has checked where it
// was.
func (r *record) investigate(inv Investigation, at time.Time) {
	r.Investigation, r.InvestigationNote = inv.Status, inv.Note
	switch inv.Status {
	case InvestigationQueued:
		r.TaskID, r.RunID = inv.TaskID, inv.RunID
		r.InvestigationStartedAt = &at
	case InvestigationRunning:
		if inv.TaskID != "" {
			r.TaskID = inv.TaskID
		}
		if inv.RunID != "" {
			r.RunID = inv.RunID
		}
		r.InvestigationStartedAt = &at
	case InvestigationDone, InvestigationFailed:
		r.Revision++
		if inv.Status == InvestigationDone && r.Issue.Number > 0 {
			r.IssueComments = append(r.IssueComments, commentFindings)
		}
	}
	r.UpdatedAt = at
}

// report is a copy of the Report a caller may keep.
func (r *record) report() *Report { return &r.clone().Report }

func (r *record) clone() *record {
	c := *r
	c.Facts, c.Evidence = cloneFacts(r.Facts), cloneEvidence(r.Evidence)
	c.OccurrenceKeys, c.IssueComments = slices.Clone(r.OccurrenceKeys), slices.Clone(r.IssueComments)
	if r.Findings != nil {
		f := *r.Findings
		f.Suggestions = slices.Clone(r.Findings.Suggestions)
		c.Findings = &f
	}
	c.ResolvedAt, c.TakenAt = cloneTime(r.ResolvedAt), cloneTime(r.TakenAt)
	c.InvestigationStartedAt, c.LastNoticeAt = cloneTime(r.InvestigationStartedAt), cloneTime(r.LastNoticeAt)
	c.IssueCommentAt = cloneTime(r.IssueCommentAt)
	return &c
}

// delivery is a copy of where the report's delivery stands.
func (r *record) delivery() *Delivery {
	c := r.clone()
	return &Delivery{
		Report: &c.Report, NotifiedRevision: c.NotifiedRevision, IssueRevision: c.IssueRevision,
		PendingNotice: c.PendingNotice, NoticeSeq: c.NoticeSeq, LastNoticeAt: c.LastNoticeAt,
		IssueComments: c.IssueComments, IssueCommentAt: c.IssueCommentAt,
	}
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

func cloneFacts(facts []Fact) []Fact {
	if facts == nil {
		return []Fact{}
	}
	return slices.Clone(facts)
}

func cloneEvidence(e Evidence) Evidence {
	e.VerdictIDs = slices.Clone(e.VerdictIDs)
	return e
}
