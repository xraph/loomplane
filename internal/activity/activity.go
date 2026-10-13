// Package activity is what an organization's Loomplane has been doing:
// deployments and how they went, plans and what was decided, analyses,
// and what's drifted. It reads Loomplane's own records, so it needs no
// metrics stack, and it only counts: anything it shows has a page of its
// own that says more.
package activity

import (
	"context"
	"fmt"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/security"
)

// MaxDays is the longest window a summary covers.
const MaxDays = 90

// ActivitySummary is an organization's activity over a window of days.
// The stutter keeps the OpenAPI component's name, and so the dashboard's
// type, telling.
type ActivitySummary struct { //nolint:revive // see above
	Days  int       `json:"days"`
	Since time.Time `json:"since"`
	// Deployments, plans and analyses count those started in the window.
	Deployments DeploymentCounts `json:"deployments"`
	Plans       PlanCounts       `json:"plans"`
	Analyses    AnalysisCounts   `json:"analyses"`
	// Drift is what's deployed now, whenever it was deployed.
	Drift DriftCounts `json:"drift"`
	// Daily has every day of the window, oldest first, empty days too.
	Daily []ActivityDay `json:"daily"`
}

// DeploymentCounts are deployments by how they went.
type DeploymentCounts struct {
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Running   int `json:"running"`
	// Retried are the deployments retried from a failed step at least once.
	Retried int `json:"retried"`
	// MedianSeconds is how long a successful one took, half the time.
	MedianSeconds int `json:"median_seconds"`
}

// PlanCounts are plans by what became of them.
type PlanCounts struct {
	Approved   int `json:"approved"`
	Rejected   int `json:"rejected"`
	Expired    int `json:"expired"`
	Superseded int `json:"superseded"`
	Failed     int `json:"failed"`
	// Waiting are plans ready for a decision right now.
	Waiting int `json:"waiting"`
}

// AnalysisCounts are analyses by how they went.
type AnalysisCounts struct {
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
}

// DriftCounts are the services and resources deployed now, and the ones
// the last drift check found gone or changed.
type DriftCounts struct {
	Deployed int `json:"deployed"`
	Missing  int `json:"missing"`
	Changed  int `json:"changed"`
}

// ActivityDay is one day's deployments, by UTC date.
type ActivityDay struct { //nolint:revive // named like ActivitySummary
	Date      string `json:"date"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
}

// Authorizer decides whether a principal may read the summary.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// Service answers for an organization's activity.
type Service struct {
	db    *pgdriver.PgDB
	authz Authorizer
	now   func() time.Time
}

// NewService returns a Service on db.
func NewService(db *pgdriver.PgDB, authz Authorizer) *Service {
	return &Service{db: db, authz: authz, now: time.Now}
}

// Summary is p's organization's activity over the last days days, today
// included. days is held to between 1 and MaxDays.
func (s *Service) Summary(ctx context.Context, p security.Principal, days int) (*ActivitySummary, error) {
	if err := s.authz.Require(ctx, p, security.DeploymentRead, ""); err != nil {
		return nil, err
	}
	days = max(1, min(days, MaxDays))
	today := s.now().UTC().Truncate(24 * time.Hour)
	since := today.AddDate(0, 0, -(days - 1))
	out := &ActivitySummary{Days: days, Since: since}
	org := p.OrgID

	var median float64
	err := s.db.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE status = 'succeeded'),
			count(*) FILTER (WHERE status = 'failed'),
			count(*) FILTER (WHERE status = 'running'),
			count(*) FILTER (WHERE retries > 0),
			COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM finished_at - started_at))
				FILTER (WHERE status = 'succeeded' AND finished_at IS NOT NULL), 0)
		FROM loomplane_deployments WHERE org_id = $1 AND started_at >= $2`, org, since).
		Scan(&out.Deployments.Succeeded, &out.Deployments.Failed, &out.Deployments.Running, &out.Deployments.Retried, &median)
	if err != nil {
		return nil, fmt.Errorf("count deployments: %w", err)
	}
	out.Deployments.MedianSeconds = int(median + 0.5)

	err = s.db.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE status = 'approved'),
			count(*) FILTER (WHERE status = 'rejected'),
			count(*) FILTER (WHERE status = 'expired'),
			count(*) FILTER (WHERE status = 'superseded'),
			count(*) FILTER (WHERE status = 'failed'),
			(SELECT count(*) FROM loomplane_plans WHERE org_id = $1 AND status = 'ready')
		FROM loomplane_plans WHERE org_id = $1 AND created_at >= $2`, org, since).
		Scan(&out.Plans.Approved, &out.Plans.Rejected, &out.Plans.Expired, &out.Plans.Superseded, &out.Plans.Failed, &out.Plans.Waiting)
	if err != nil {
		return nil, fmt.Errorf("count plans: %w", err)
	}

	err = s.db.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE status = 'succeeded'),
			count(*) FILTER (WHERE status = 'failed')
		FROM loomplane_analyses WHERE org_id = $1 AND created_at >= $2`, org, since).
		Scan(&out.Analyses.Succeeded, &out.Analyses.Failed)
	if err != nil {
		return nil, fmt.Errorf("count analyses: %w", err)
	}

	err = s.db.QueryRow(ctx, `SELECT count(*),
			count(*) FILTER (WHERE drift = 'missing'),
			count(*) FILTER (WHERE drift = 'changed')
		FROM loomplane_bindings WHERE org_id = $1`, org).
		Scan(&out.Drift.Deployed, &out.Drift.Missing, &out.Drift.Changed)
	if err != nil {
		return nil, fmt.Errorf("count drift: %w", err)
	}

	byDate := map[string]*ActivityDay{}
	out.Daily = make([]ActivityDay, days)
	for i := range out.Daily {
		out.Daily[i].Date = since.AddDate(0, 0, i).Format(time.DateOnly)
		byDate[out.Daily[i].Date] = &out.Daily[i]
	}
	rows, err := s.db.Query(ctx, `SELECT to_char(started_at AT TIME ZONE 'UTC', 'YYYY-MM-DD'),
			count(*) FILTER (WHERE status = 'succeeded'),
			count(*) FILTER (WHERE status = 'failed')
		FROM loomplane_deployments WHERE org_id = $1 AND started_at >= $2
		GROUP BY 1`, org, since)
	if err != nil {
		return nil, fmt.Errorf("count deployments by day: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var date string
		var ok, failed int
		if err := rows.Scan(&date, &ok, &failed); err != nil {
			return nil, err
		}
		if d := byDate[date]; d != nil {
			d.Succeeded, d.Failed = ok, failed
		}
	}
	return out, rows.Err()
}
