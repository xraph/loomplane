package agentknowledge

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/xraph/dispatch/job"

	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/metering"
)

// Past agent reports become each workspace's own knowledge: the reports
// observer keeps a report current as it changes, and a daily backfill
// catches up a workspace that turned knowledge on, or a change the
// observer missed.
const (
	JobBackfillReports = "loomplane.agent.knowledge.reports"
	// CronBackfillReports runs the backfill BackfillReportsEvery, on
	// agentruns.SweepQueue. The platform schedules it, and queues one run
	// at start, only while knowledge is available.
	CronBackfillReports  = "loomplane.agent.knowledge.reports"
	BackfillReportsEvery = "@every 24h"
	// ReportSourcePrefix starts a report's source key in the workspace's
	// collection: "report/" and the report's ID.
	ReportSourcePrefix = "report/"
	// ReportsPage is how many reports the backfill reads at a time.
	ReportsPage = 200
)

// reportsLock is the advisory lock the backfill holds, so replicas that
// start together backfill one at a time.
const reportsLock = "agent-knowledge-reports"

// reportsJobTimeout bounds one backfill of every workspace. Reports that
// are already indexed answer Unchanged before any embed.
const reportsJobTimeout = 30 * time.Minute

// Reports reads a workspace's reports as knowledge keeps them;
// *agentreports.Service is the real one. Documents returns the reports
// updated after after, oldest first, and binds its own scope.
type Reports interface {
	Documents(ctx context.Context, orgID string, after time.Time, limit int) ([]agentreports.KnowledgeDocument, error)
}

var (
	_ Reports               = (*agentreports.Service)(nil)
	_ agentreports.Observer = ReportObserver{}
)

// ReportObserver keeps a report in its workspace's knowledge each time it
// changes. It implements agentreports.Observer, whose errors the reports
// log and never fail the change for.
type ReportObserver struct{ Knowledge *Service }

// ReportChanged indexes doc, or removes it when it has no text. Knowledge
// being off, or another ingest already on the same text, is no error.
func (o ReportObserver) ReportChanged(ctx context.Context, doc agentreports.KnowledgeDocument) error {
	if o.Knowledge == nil {
		return nil
	}
	return o.Knowledge.keepReport(ctx, doc.OrgID, doc)
}

// ReportDocument is doc as the workspace's knowledge keeps it. A report's
// text is redacted and scanned on its way out, but its facts and findings
// came from outside Loomplane or from the model, so it is Untrusted. A
// source key that isn't a report's is ErrInvalid.
func ReportDocument(doc agentreports.KnowledgeDocument) (Document, error) {
	id, ok := strings.CutPrefix(doc.SourceKey, agentreports.SourcePrefix)
	if !ok || strings.TrimSpace(id) == "" {
		return Document{}, fmt.Errorf("%w: %q isn't an agent report's source key", ErrInvalid, doc.SourceKey)
	}
	md := make(map[string]string, len(doc.Metadata)+1)
	maps.Copy(md, doc.Metadata)
	md["report_id"] = id
	return Document{
		SourceKey: ReportSourcePrefix + id, Kind: KindReport, Title: doc.Title, Content: doc.Text,
		URL: "/agent/reports/" + id, ProjectID: doc.ProjectID, Metadata: md, Untrusted: true,
	}, nil
}

// keepReport indexes doc in orgID's knowledge, or removes it when its text
// is empty. Every outcome of Ingest is fine: indexed, unchanged, skipped
// with knowledge off, or pending while another ingest of the same text
// runs (Recover retries one that died).
func (s *Service) keepReport(ctx context.Context, orgID string, doc agentreports.KnowledgeDocument) error {
	if doc.OrgID != orgID {
		return fmt.Errorf("%w: report %s belongs to workspace %q, not %q", ErrInvalid, doc.SourceKey, doc.OrgID, orgID)
	}
	d, err := ReportDocument(doc)
	if err != nil {
		return err
	}
	if strings.TrimSpace(d.Content) == "" {
		return s.Remove(ctx, orgID, d.SourceKey)
	}
	_, err = s.Ingest(ctx, orgID, d)
	return err
}

// BackfillReports indexes every report of orgID's that knowledge keeps,
// reading them ReportsPage at a time by when each last changed, and
// returns how many it kept. Reports already indexed answer Unchanged
// before any embed. One report that fails doesn't stop the rest; the
// workspace's budget running out does, with agentsettings.ErrBudgetExhausted.
// It does nothing while knowledge is off for the server or the workspace.
func (s *Service) BackfillReports(ctx context.Context, r Reports, orgID string) (int, error) {
	if err := checkWorkspace(orgID); err != nil {
		return 0, err
	}
	if _, on, err := s.workspaceEngine(ctx, orgID); err != nil || !on {
		return 0, err
	}
	kept := 0
	var errs []error
	var after time.Time
	for {
		if err := ctx.Err(); err != nil {
			return kept, with(errs, err)
		}
		page, err := r.Documents(ctx, orgID, after, ReportsPage)
		if err != nil {
			return kept, with(errs, fmt.Errorf("agentknowledge: read the reports of %s: %w", orgID, err))
		}
		for _, doc := range page {
			err = s.keepReport(ctx, orgID, doc)
			if errors.Is(err, agentsettings.ErrBudgetExhausted) || errors.Is(err, metering.ErrBlocked) {
				return kept, with(errs, err)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("agentknowledge: report %s: %w", doc.SourceKey, err))
				continue
			}
			kept++
		}
		if len(page) < ReportsPage {
			return kept, errors.Join(errs...)
		}
		last := page[len(page)-1].UpdatedAt
		if !last.After(after) {
			// Reports reads strictly after after, so this can't happen;
			// stop rather than read the same page forever.
			return kept, with(errs, fmt.Errorf("agentknowledge: the reports of %s didn't move past %s", orgID, after))
		}
		after = last
	}
}

// with is err after the errors before it: err itself when there are none,
// so a backfill stopped by the budget alone says only that.
func with(errs []error, err error) error {
	if len(errs) == 0 {
		return err
	}
	return errors.Join(append(errs, err)...)
}

// ReportsJobOptions are the backfill job's queue, retries and timeout. It
// isn't retried: the next day's run, or the next start, tries again.
func ReportsJobOptions() []job.Option {
	return []job.Option{job.WithQueue(agentruns.SweepQueue), job.WithMaxRetries(0), job.WithTimeout(reportsJobTimeout)}
}

// BackfillReportsJob backfills the reports of each workspace orgs names
// (agentsettings.Service.EnabledOrgs: those whose agent is on) that has
// knowledge on, one replica at a time. A workspace at its budget stops
// there and the next goes on. It does nothing while knowledge is
// unavailable, so a schedule left from a boot that had it costs nothing.
func BackfillReportsJob(s *Service, r Reports, orgs func(ctx context.Context) ([]string, error)) *job.Definition[struct{}] {
	return job.NewDefinition(JobBackfillReports, func(ctx context.Context, _ struct{}) error {
		return s.backfillEvery(ctx, r, orgs)
	}, ReportsJobOptions()...)
}

func (s *Service) backfillEvery(ctx context.Context, r Reports, orgs func(ctx context.Context) ([]string, error)) error {
	if s.engine() == nil {
		return nil
	}
	return s.d.Lock(ctx, reportsLock, func(ctx context.Context) error {
		if s.engine() == nil {
			// Knowledge turned off while this waited for the lock.
			return nil
		}
		ids, err := orgs(ctx)
		if err != nil {
			return fmt.Errorf("agentknowledge: list the workspaces to backfill: %w", err)
		}
		var errs []error
		for _, orgID := range ids {
			if _, err = s.BackfillReports(ctx, r, orgID); err != nil && !onlyBudget(err) {
				errs = append(errs, fmt.Errorf("workspace %s: %w", orgID, err))
			}
		}
		return errors.Join(errs...)
	})
}

// onlyBudget is whether err says the workspace spent its budget and
// nothing else, which is how a workspace's backfill is meant to stop.
func onlyBudget(err error) bool {
	if _, joined := err.(interface{ Unwrap() []error }); joined {
		return false
	}
	return errors.Is(err, agentsettings.ErrBudgetExhausted) || errors.Is(err, metering.ErrBlocked)
}
