package deployments

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

// History reads an environment's releases from its deployments and the
// plans they carried out: which release came before which, and what each
// service ran after one.
type History struct {
	Store *PgStore
	Plans Plans
	// Providers names each provider's cloud, which is how a plan that
	// pins no connection for an action names the one it deployed with.
	// ReleasedImages needs it.
	Providers *provider.Registry
}

var _ planning.ReleaseHistory = (*History)(nil)

// releaseScan is how many releases ReleasedImages looks back through for
// a service a newer release left as it was.
const releaseScan = 50

// release is one succeeded deployment with the steps it carried out.
type release struct {
	planning.ReleaseRecord
	steps []Step
}

// Releases implements planning.ReleaseHistory.
func (h *History) Releases(ctx context.Context, orgID, environmentID string, before time.Time, limit int) ([]planning.ReleaseRecord, error) {
	rs, err := h.releases(ctx, orgID, environmentID, before, false, limit)
	if err != nil {
		return nil, err
	}
	out := make([]planning.ReleaseRecord, len(rs))
	for i, r := range rs {
		out[i] = r.ReleaseRecord
	}
	return out, nil
}

// releases reads up to limit of an environment's succeeded deployments
// that finished before bound, or at it too when inclusive, newest first.
// A Cloud sync or catalog plan's deployment changes where or what
// infrastructure runs, not the application, so it isn't a release.
func (h *History) releases(ctx context.Context, orgID, environmentID string, bound time.Time, inclusive bool, limit int) ([]release, error) {
	if limit <= 0 {
		return []release{}, nil
	}
	var at *time.Time
	if !bound.IsZero() {
		at = &bound
	}
	rows, err := h.Store.db.Query(ctx, `SELECT `+recordColumns+`, d.steps
		FROM loomplane_deployments d JOIN loomplane_plans p ON p.org_id = d.org_id AND p.id = d.plan_id
		WHERE d.org_id = $1 AND d.environment_id = $2 AND d.status = 'succeeded' AND d.finished_at IS NOT NULL
			AND ($3::timestamptz IS NULL OR d.finished_at < $3 OR ($5 AND d.finished_at = $3))
			AND `+releasePlans+`
		ORDER BY d.finished_at DESC, d.id DESC LIMIT $4`, orgID, environmentID, at, limit, inclusive)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []release{}
	for rows.Next() {
		var r release
		var raw []byte
		var finished *time.Time
		if scanErr := rows.Scan(&r.DeploymentID, &r.PlanID, &r.Kind, &r.Status, &r.CanaryStatus, &r.StartedAt, &finished, &raw); scanErr != nil {
			return nil, fmt.Errorf("scan release: %w", scanErr)
		}
		if r.steps, err = decodeSteps(raw); err != nil {
			return nil, fmt.Errorf("decode steps of %s: %w", r.DeploymentID, err)
		}
		r.settle(finished)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	return out, nil
}

// recordColumns are a planning.ReleaseRecord's columns, in its order, of
// deployment d and its plan p.
const recordColumns = `d.id, d.plan_id, p.kind, d.status, coalesce(d.canary->>'status', ''), d.started_at, d.finished_at`

// releasePlans leaves out plan p when it's a Cloud sync or catalog plan,
// whose deployment changes where or what infrastructure runs, not the
// application.
const releasePlans = `coalesce(p.result->>'cloud_sync_id', '') = '' AND NOT p.catalog`

// settle stores the times in UTC, finished zero while the deployment runs.
func (r *release) settle(finished *time.Time) {
	r.StartedAt = r.StartedAt.UTC()
	if finished != nil {
		r.FinishedAt = finished.UTC()
	}
}

// Latest implements planning.ReleaseHistory.
func (h *History) Latest(ctx context.Context, orgID, environmentID string) (*planning.ReleaseRecord, error) {
	var r release
	var finished *time.Time
	err := h.Store.db.QueryRow(ctx, `SELECT `+recordColumns+`
		FROM loomplane_deployments d JOIN loomplane_plans p ON p.org_id = d.org_id AND p.id = d.plan_id
		WHERE d.org_id = $1 AND d.environment_id = $2 AND `+releasePlans+` AND NOT (d.status = 'failed' AND d.error = $3)
		ORDER BY d.started_at DESC, d.id DESC LIMIT 1`, orgID, environmentID, StaleRollback).
		Scan(&r.DeploymentID, &r.PlanID, &r.Kind, &r.Status, &r.CanaryStatus, &r.StartedAt, &finished)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: nothing has deployed to this environment", planning.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get the newest deployment: %w", err)
	}
	r.settle(finished)
	return &r.ReleaseRecord, nil
}

// Deployment implements planning.ReleaseHistory.
func (h *History) Deployment(ctx context.Context, orgID, deploymentID string) (*planning.ReleaseRecord, error) {
	var r release
	var finished *time.Time
	err := h.Store.db.QueryRow(ctx, `SELECT `+recordColumns+`
		FROM loomplane_deployments d JOIN loomplane_plans p ON p.org_id = d.org_id AND p.id = d.plan_id
		WHERE d.org_id = $1 AND d.id = $2`, orgID, deploymentID).
		Scan(&r.DeploymentID, &r.PlanID, &r.Kind, &r.Status, &r.CanaryStatus, &r.StartedAt, &finished)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: no deployment %s", planning.ErrNotFound, deploymentID)
	}
	if err != nil {
		return nil, fmt.Errorf("get deployment %s: %w", deploymentID, err)
	}
	r.settle(finished)
	return &r.ReleaseRecord, nil
}

// ReleasedImages implements planning.ReleaseHistory. It walks back from
// the newest release at or before upTo (zero: the newest of all), and
// takes each service from the newest release that deployed it: a release
// that kept a service as it was has no step for it, so an older one says
// what it ran. A service a release deleted ran nothing after it.
func (h *History) ReleasedImages(ctx context.Context, orgID, environmentID string, upTo time.Time) (map[string]planning.ReleasedImage, error) {
	if h.Providers == nil {
		return nil, errors.New("release history has no provider registry to name the connection each image was deployed with")
	}
	// Postgres keeps microseconds, so upTo is rounded the way Postgres
	// rounds a time it stores.
	rs, err := h.releases(ctx, orgID, environmentID, upTo.Round(time.Microsecond), true, releaseScan)
	if err != nil {
		return nil, err
	}
	out := map[string]planning.ReleasedImage{}
	settled := map[string]bool{}
	for _, r := range rs {
		var plan *planning.Plan
		for _, s := range r.steps {
			if s.Status != StepSucceeded || s.Subject != string(planning.ServiceSubject) || settled[s.Name] {
				continue
			}
			if plan == nil {
				if plan, err = h.Plans.GetPlan(ctx, orgID, r.PlanID); err != nil {
					return nil, fmt.Errorf("read the plan of release %s: %w", r.DeploymentID, err)
				}
			}
			a := planAction(plan, s.ActionID)
			// Adopting a placement records where a service runs without
			// deploying it.
			if a == nil || a.Op == planning.Keep {
				continue
			}
			settled[s.Name] = true
			if a.Op == planning.Delete || a.Service == nil {
				continue
			}
			img := planning.ReleasedImage{
				Service: s.Name, Provider: a.Provider, ConnectionID: h.connection(plan, a), Region: a.Region,
				CommitSHA: a.Service.CommitSHA, Architecture: a.Service.BuildArchitecture(), Inputs: a.Service.BuildInputs(),
				Image: s.Image, Revision: s.Revision,
				DeploymentID: r.DeploymentID, PlanID: r.PlanID, FinishedAt: r.FinishedAt,
			}
			img.Registry = a.Service.ImageRegistry()
			out[s.Name] = img
		}
	}
	return out, nil
}

// connection is the connection a plan deployed an action with: the one
// the action pins, or the plan's for the provider's cloud, as the runner
// chooses.
func (h *History) connection(plan *planning.Plan, a *planning.Action) string {
	if a.ConnectionID != "" {
		return a.ConnectionID
	}
	prov, err := h.Providers.Get(a.Provider)
	if err != nil {
		return ""
	}
	return plan.Connections[prov.Cloud]
}

func planAction(plan *planning.Plan, id string) *planning.Action {
	for i := range plan.Actions {
		if plan.Actions[i].ID == id {
			return &plan.Actions[i]
		}
	}
	return nil
}
