package deployments

import (
	"context"
	"fmt"
	"time"

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
	// Without it, an image records only the connection its action pins.
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
	rs, err := h.releases(ctx, orgID, environmentID, before, limit)
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
// that finished before `before`, newest first. A Cloud sync or catalog
// plan's deployment changes where or what infrastructure runs, not the
// application, so it isn't a release.
func (h *History) releases(ctx context.Context, orgID, environmentID string, before time.Time, limit int) ([]release, error) {
	if limit <= 0 {
		return []release{}, nil
	}
	var bound *time.Time
	if !before.IsZero() {
		bound = &before
	}
	rows, err := h.Store.db.Query(ctx, `SELECT d.id, d.plan_id, p.kind, d.status, d.started_at, d.finished_at, d.steps
		FROM loomplane_deployments d JOIN loomplane_plans p ON p.org_id = d.org_id AND p.id = d.plan_id
		WHERE d.org_id = $1 AND d.environment_id = $2 AND d.status = 'succeeded' AND d.finished_at IS NOT NULL
			AND ($3::timestamptz IS NULL OR d.finished_at < $3)
			AND coalesce(p.result->>'cloud_sync_id', '') = '' AND NOT p.catalog
		ORDER BY d.finished_at DESC, d.id DESC LIMIT $4`, orgID, environmentID, bound, limit)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []release{}
	for rows.Next() {
		var r release
		var raw []byte
		if scanErr := rows.Scan(&r.DeploymentID, &r.PlanID, &r.Kind, &r.Status, &r.StartedAt, &r.FinishedAt, &raw); scanErr != nil {
			return nil, fmt.Errorf("scan release: %w", scanErr)
		}
		if r.steps, err = decodeSteps(raw); err != nil {
			return nil, fmt.Errorf("decode steps of %s: %w", r.DeploymentID, err)
		}
		r.StartedAt, r.FinishedAt = r.StartedAt.UTC(), r.FinishedAt.UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	return out, nil
}

// ReleasedImages implements planning.ReleaseHistory. It walks back from
// the newest release at or before upTo (zero: the newest of all), and
// takes each service from the newest release that deployed it: a release
// that kept a service as it was has no step for it, so an older one says
// what it ran. A service a release deleted ran nothing after it.
func (h *History) ReleasedImages(ctx context.Context, orgID, environmentID string, upTo time.Time) (map[string]planning.ReleasedImage, error) {
	// Postgres keeps microseconds, so the release that finished at upTo is
	// the last one before the next microsecond.
	before := upTo
	if !before.IsZero() {
		before = before.Truncate(time.Microsecond).Add(time.Microsecond)
	}
	rs, err := h.releases(ctx, orgID, environmentID, before, releaseScan)
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
				CommitSHA: a.Service.CommitSHA, Image: s.Image, Revision: s.Revision,
				DeploymentID: r.DeploymentID, PlanID: r.PlanID, FinishedAt: r.FinishedAt,
			}
			if a.Service.BuildTarget != nil {
				img.Registry = a.Service.BuildTarget.Registry
			}
			out[s.Name] = img
		}
	}
	return out, nil
}

// connection is the connection a plan deployed an action with: the one
// the action pins, or the plan's for the provider's cloud, as the runner
// chooses.
func (h *History) connection(plan *planning.Plan, a *planning.Action) string {
	if a.ConnectionID != "" || h.Providers == nil {
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
