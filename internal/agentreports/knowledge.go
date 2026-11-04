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
	// Text is rendered like the issue body (Task 4's renderer), after
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
	doc := document(r)
	for _, o := range observers {
		if err := o.ReportChanged(context.WithoutCancel(ctx), doc); err != nil {
			s.warn("agentreports: an observer failed on report "+r.ID, err)
		}
	}
}

// Documents returns the workspace's reports with findings or no longer
// open, updated after after, oldest first: what a backfill of the agent's
// knowledge reads. limit is 1 to 200.
func (s *Service) Documents(ctx context.Context, orgID string, after time.Time, limit int) ([]KnowledgeDocument, error) {
	if orgID == "" {
		return nil, invalid("no workspace")
	}
	if limit < 1 || limit > maxDocuments {
		return nil, invalid("limit is 1 to %d", maxDocuments)
	}
	reports, err := s.d.Store.Documents(ctx, orgID, after.UTC(), limit)
	if err != nil {
		return nil, err
	}
	docs := make([]KnowledgeDocument, 0, len(reports))
	for _, r := range reports {
		docs = append(docs, document(r))
	}
	return docs, nil
}

func document(r *Report) KnowledgeDocument {
	return KnowledgeDocument{
		SourceKey: SourcePrefix + r.ID, OrgID: r.OrgID, ProjectID: r.ProjectID, Title: r.Title,
		Text: renderDocumentText(r), UpdatedAt: r.UpdatedAt,
		Metadata: map[string]string{
			"project_id": r.ProjectID, "environment": r.Environment, "signal": r.Signal,
			"component": r.Component, "status": r.Status, "severity": r.Severity,
		},
	}
}

// renderDocumentText is a report's text for knowledge. It is the title and
// facts for now; delivery's renderer, with redaction and the outbound
// scan, replaces this one function.
func renderDocumentText(r *Report) string {
	var b strings.Builder
	b.WriteString(r.Title)
	for _, f := range r.Facts {
		b.WriteString("\n")
		b.WriteString(f.Label)
		b.WriteString(": ")
		b.WriteString(f.Value)
	}
	return b.String()
}
