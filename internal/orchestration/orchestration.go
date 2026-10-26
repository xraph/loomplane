// Package orchestration defines Loomplane's Dispatch jobs and how they're
// enqueued.
//
// Dispatch v1.8.0 has no job deduplication, and it ignores the options set
// on a job definition when a job is enqueued (queue, retries, timeout all
// come from the enqueue call). So every Loomplane job is enqueued through
// this package, which always passes them, and every handler is idempotent:
// a retried or duplicated job must do no harm.
//
// Multi-step deployment workflows will use Dispatch's durable runtime
// (durable/runtime), whose handlers have to be given to the dispatch
// extension when it's built; see docs/forge-integration.md.
package orchestration

import (
	"context"
	"fmt"
	"time"

	"github.com/xraph/dispatch/engine"
	"github.com/xraph/dispatch/job"
)

// Queue is the Dispatch queue the API process works. The dispatch
// extension must be told to work it (WithQueues).
const Queue = "loomplane"

// AnalysisQueue is the queue only loomplane-worker works. Repository
// analysis reads untrusted code, so it stays out of the API process.
const AnalysisQueue = "loomplane-analysis"

// Job names. Keep them stable: queued jobs refer to them by name.
const (
	JobProjectInitialize  = "loomplane.project.initialize"
	JobRepositoryAnalysis = "loomplane.repository.analyze"
)

const (
	projectInitRetries = 5
	projectInitTimeout = 2 * time.Minute

	// AnalysisRetries is how often an analysis job is retried after a
	// transient failure. The runner fails the analysis on the last try.
	AnalysisRetries = 3
	analysisTimeout = 20 * time.Minute
)

// ProjectInitPayload is the payload of JobProjectInitialize.
type ProjectInitPayload struct {
	OrgID     string `json:"org_id"`
	ProjectID string `json:"project_id"`
}

// ProjectInitializer does the work of JobProjectInitialize.
type ProjectInitializer interface {
	Initialize(ctx context.Context, orgID, projectID string) error
}

// ProjectInitJob is the job that gives a new project its environments.
func ProjectInitJob(p ProjectInitializer) *job.Definition[ProjectInitPayload] {
	return job.NewDefinition(JobProjectInitialize, func(ctx context.Context, in ProjectInitPayload) error {
		return p.Initialize(ctx, in.OrgID, in.ProjectID)
	})
}

// AnalysisPayload is the payload of JobRepositoryAnalysis.
type AnalysisPayload struct {
	OrgID      string `json:"org_id"`
	AnalysisID string `json:"analysis_id"`
}

// AnalysisRunner does the work of JobRepositoryAnalysis.
type AnalysisRunner interface {
	Run(ctx context.Context, orgID, analysisID string) error
}

// AnalysisJob is the job that analyzes a repository. Only the worker
// registers it.
func AnalysisJob(r AnalysisRunner) *job.Definition[AnalysisPayload] {
	return job.NewDefinition(JobRepositoryAnalysis, func(ctx context.Context, in AnalysisPayload) error {
		return r.Run(ctx, in.OrgID, in.AnalysisID)
	})
}

// Enqueuer enqueues Loomplane's jobs on a Dispatch engine.
type Enqueuer struct {
	eng *engine.Engine
}

// NewEnqueuer returns an Enqueuer over eng.
func NewEnqueuer(eng *engine.Engine) *Enqueuer { return &Enqueuer{eng: eng} }

// InitializeProject enqueues JobProjectInitialize.
func (q *Enqueuer) InitializeProject(ctx context.Context, orgID, projectID string) error {
	_, err := engine.Enqueue(ctx, q.eng, JobProjectInitialize,
		ProjectInitPayload{OrgID: orgID, ProjectID: projectID},
		job.WithQueue(Queue),
		job.WithMaxRetries(projectInitRetries),
		job.WithTimeout(projectInitTimeout),
	)
	if err != nil {
		return fmt.Errorf("enqueue %s: %w", JobProjectInitialize, err)
	}
	return nil
}

// AnalyzeRepository enqueues JobRepositoryAnalysis on the analysis queue.
func (q *Enqueuer) AnalyzeRepository(ctx context.Context, orgID, analysisID string) error {
	_, err := engine.Enqueue(ctx, q.eng, JobRepositoryAnalysis,
		AnalysisPayload{OrgID: orgID, AnalysisID: analysisID},
		job.WithQueue(AnalysisQueue),
		job.WithMaxRetries(AnalysisRetries),
		job.WithTimeout(analysisTimeout),
	)
	if err != nil {
		return fmt.Errorf("enqueue %s: %w", JobRepositoryAnalysis, err)
	}
	return nil
}

// JobDriftCheck checks everything deployed against what its provider has
// live. Its cron entry runs it on the leader only.
const JobDriftCheck = "loomplane.drift.check"

// CronDriftCheck names the cron entry that enqueues JobDriftCheck.
const CronDriftCheck = "loomplane.drift"

// DriftPayload is the payload of JobDriftCheck. It has nothing in it: a
// check covers every binding.
type DriftPayload struct{}

// DriftChecker does the work of JobDriftCheck.
type DriftChecker interface {
	Run(ctx context.Context) error
}

// JobConnectionRefresh renews the cloud sign-ins that are close to expiring.
// Its cron entry runs it on the leader only.
const JobConnectionRefresh = "loomplane.connections.refresh"

// CronConnectionRefresh names the cron entry that enqueues
// JobConnectionRefresh.
const CronConnectionRefresh = "loomplane.connections.refresh"

// ConnectionRefreshPayload is the payload of JobConnectionRefresh. It has
// nothing in it: a pass covers every signed-in connection.
type ConnectionRefreshPayload struct{}

// ConnectionRefreshJob is JobConnectionRefresh's definition. It takes the
// refresher as an interface so this package does not import connections.
func ConnectionRefreshJob(r interface{ RefreshAll(context.Context) error }) *job.Definition[ConnectionRefreshPayload] {
	return job.NewDefinition(JobConnectionRefresh, func(ctx context.Context, _ ConnectionRefreshPayload) error {
		return r.RefreshAll(ctx)
	})
}

// DriftJob is JobDriftCheck's definition.
func DriftJob(c DriftChecker) *job.Definition[DriftPayload] {
	return job.NewDefinition(JobDriftCheck, func(ctx context.Context, _ DriftPayload) error {
		return c.Run(ctx)
	})
}
