package agentknowledge

import (
	"context"
	"time"

	"github.com/xraph/dispatch/job"
)

// Knowledge's jobs and the recovery schedule. The platform queues the docs
// job at start while knowledge is available, and schedules recovery then.
const (
	JobIngestDocs = "loomplane.agent.knowledge.docs"
	JobRecover    = "loomplane.agent.knowledge.recover"
	CronRecover   = "loomplane.agent.knowledge.recover"
	// RecoverEvery is how often recovery runs.
	RecoverEvery = "@every 5m"
)

// What the docs job may take: a release's docs embed in a few minutes, and
// a failure that clears (nexus not ready, a provider hiccup) is retried.
const (
	docsJobRetries = 3
	docsJobTimeout = 30 * time.Minute
)

// DocsJobOptions are the docs job's retries and timeout. The caller adds
// the queue.
func DocsJobOptions() []job.Option {
	return []job.Option{job.WithMaxRetries(docsJobRetries), job.WithTimeout(docsJobTimeout)}
}

// IngestDocsJob indexes the docs (IngestDocs).
func IngestDocsJob(s *Service) *job.Definition[struct{}] {
	return job.NewDefinition(JobIngestDocs, func(ctx context.Context, _ struct{}) error {
		_, err := s.IngestDocs(ctx)
		return err
	}, DocsJobOptions()...)
}

// RecoverJob fails documents whose ingest went quiet, and indexes the docs
// again when it failed any (Recover). It does nothing while knowledge is
// unavailable, so a schedule left from a boot that had it costs nothing.
func RecoverJob(s *Service) *job.Definition[struct{}] {
	return job.NewDefinition(JobRecover, func(ctx context.Context, _ struct{}) error {
		return s.Recover(ctx)
	}, job.WithMaxRetries(0))
}
