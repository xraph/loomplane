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

// Queue is the Dispatch queue Loomplane's jobs run on. The dispatch
// extension must be told to work it (WithQueues).
const Queue = "loomplane"

// Job names. Keep them stable: queued jobs refer to them by name.
const (
	JobProjectInitialize = "loomplane.project.initialize"
)

const (
	projectInitRetries = 5
	projectInitTimeout = 2 * time.Minute
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
