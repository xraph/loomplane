package releaseprep

import (
	"time"

	"github.com/xraph/dispatch/job"

	"github.com/xraph/loomplane/internal/agentruns"
)

// The release preparation's jobs and the settle cron. Task 7 defines what
// they run. They go on the default queue, which every server works with
// the agent on or off: a server with AI off registers no-op handlers for
// them, so a schedule left from AI on never piles up.
const (
	// JobPrepare plans the promotion, collects the evidence and starts
	// the agent's review of one report.
	JobPrepare = "loomplane.release.prepare"
	// JobSettle runs Settle.
	JobSettle = "loomplane.release.settle"
	// CronSettle schedules JobSettle every 30 seconds.
	CronSettle = "loomplane.release.settle"
)

// JobQueue is the queue the jobs and the cron run on.
const JobQueue = agentruns.SweepQueue

// The prepare job's bounds: a few tries, each well within what reading
// the evidence takes.
const (
	prepareRetries = 3
	prepareTimeout = 10 * time.Minute
)

// JobPayload names a report. Its job reads everything else from the
// report's row.
type JobPayload struct {
	OrgID    string `json:"org_id"`
	ReportID string `json:"report_id"`
}

// JobOptions are how JobPrepare is queued.
func JobOptions() []job.Option {
	return []job.Option{job.WithQueue(JobQueue), job.WithMaxRetries(prepareRetries), job.WithTimeout(prepareTimeout)}
}
