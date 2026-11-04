package agentreports

import (
	"context"
	"strings"
	"time"
)

// SourcePrefix starts every report's KnowledgeDocument.SourceKey.
const SourcePrefix = "agent-report:"

// KnowledgeDocument is a report as the agent's knowledge keeps it.
type KnowledgeDocument struct {
	SourceKey string // "agent-report:" + report ID; stable, for weave's upsert by source
	OrgID     string
	ProjectID string
	Title     string
	// Text is the issue's title and body, without the marker, after
	// redaction and the outbound scan; never raw facts.
	Text      string
	UpdatedAt time.Time
	Metadata  map[string]string // project_id, environment, signal, component, status, severity
}

// Observer hears about reports worth remembering.
type Observer interface {
	ReportChanged(ctx context.Context, doc KnowledgeDocument) error
}

// AddObserver adds o, which is called after an investigation finishes and
// after a report resolves, closes or reopens. Its errors are logged and
// never fail the change.
func (s *Service) AddObserver(o Observer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observers = append(s.observers, o)
}

func (s *Service) notify(ctx context.Context, r *Report) {
	s.mu.RLock()
	observers := append([]Observer(nil), s.observers...)
	s.mu.RUnlock()
	if len(observers) == 0 {
		return
	}
	text, err := s.Text(ctx, r)
	if err != nil {
		s.warn("agentreports: render report "+r.ID+" for its observers", err)
		return
	}
	if text == (Rendered{}) {
		return // the agent is off: nothing can be scanned, and knowledge waits
	}
	doc := document(r, knowledgeText(r, text))
	for _, o := range observers {
		if err := o.ReportChanged(context.WithoutCancel(ctx), doc); err != nil {
			s.warn("agentreports: an observer failed on report "+r.ID, err)
		}
	}
}

// Documents returns the workspace's reports with findings or no longer
// open, updated after after, oldest first: what a backfill of the agent's
// knowledge reads. limit is 1 to 200; a full page goes on through every
// report updated at the same moment as its last one, so reading on from
// the last UpdatedAt never skips a report. While the workspace's agent is
// off it returns none.
func (s *Service) Documents(ctx context.Context, orgID string, after time.Time, limit int) ([]KnowledgeDocument, error) {
	if orgID == "" {
		return nil, invalid("no workspace")
	}
	if limit < 1 || limit > maxDocuments {
		return nil, invalid("limit is 1 to %d", maxDocuments)
	}
	bound, on, err := s.bind(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if !on {
		return []KnowledgeDocument{}, nil
	}
	reports, err := s.d.Store.Documents(ctx, orgID, after.UTC(), limit)
	if err != nil {
		return nil, err
	}
	// One read of the secrets for each project and environment.
	senders := map[[2]string]*sender{}
	docs := make([]KnowledgeDocument, 0, len(reports))
	for _, r := range reports {
		key := [2]string{r.ProjectID, r.Environment}
		out, ok := senders[key]
		if !ok {
			out = s.sender(bound, r)
			senders[key] = out
		}
		title, body := s.issueTexts(bound, out, r, s.names(bound, r, out))
		docs = append(docs, document(r, knowledgeText(r, Rendered{IssueTitle: title, IssueBody: body})))
	}
	return docs, nil
}

// knowledgeText is the issue's title and its body without the marker:
// what knowledge keeps is what GitHub got.
func knowledgeText(r *Report, text Rendered) string {
	return text.IssueTitle + "\n\n" + strings.TrimPrefix(text.IssueBody, Marker(r.ID)+"\n\n")
}

func document(r *Report, text string) KnowledgeDocument {
	return KnowledgeDocument{
		SourceKey: SourcePrefix + r.ID, OrgID: r.OrgID, ProjectID: r.ProjectID, Title: r.Title,
		Text: text, UpdatedAt: r.UpdatedAt,
		Metadata: map[string]string{
			"project_id": r.ProjectID, "environment": r.Environment, "signal": r.Signal,
			"component": r.Component, "status": r.Status, "severity": r.Severity,
		},
	}
}
