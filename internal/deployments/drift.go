package deployments

import (
	"context"
	"fmt"
	"time"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/telemetry"
)

// driftPage is how many bindings a check reads at a time.
const driftPage = 200

// DriftStore pages through bindings and records what checks find.
// *planning.PgStore is the real one.
type DriftStore interface {
	AllBindings(ctx context.Context, after string, limit int) ([]*planning.Binding, error)
	RecordDrift(ctx context.Context, orgID, id, drift, message string, at time.Time) error
}

// DriftChecker compares what each binding says is deployed with what its
// provider has live, and records the difference. It only reads clouds:
// fixing drift is a plan, approved like any other, so there's one
// authority that changes anything.
type DriftChecker struct {
	Store       DriftStore
	Credentials Credentials
	Executors   map[string]Executor
	// Warn logs a binding that couldn't be checked. It may be nil.
	Warn func(msg string, err error)
	// Telemetry counts what drifted. It may be nil.
	Telemetry *telemetry.Recorder
	now       func() time.Time
}

// DriftReport counts what a check did.
type DriftReport struct {
	Checked int
	Drifted int
	Skipped int
}

// Check checks every binding once.
func (c *DriftChecker) Check(ctx context.Context) (DriftReport, error) {
	now := c.now
	if now == nil {
		now = time.Now
	}
	warn := c.Warn
	if warn == nil {
		warn = func(string, error) {}
	}
	var rep DriftReport
	after := ""
	for {
		page, err := c.Store.AllBindings(ctx, after, driftPage)
		if err != nil {
			return rep, err
		}
		for _, b := range page {
			drift, msg, ok := c.check(ctx, b, warn)
			if !ok {
				rep.Skipped++
				continue
			}
			rep.Checked++
			if drift != "" {
				rep.Drifted++
				c.Telemetry.Drift(b.Provider, drift)
			}
			if err := c.Store.RecordDrift(ctx, b.OrgID, b.ID, drift, msg, now().UTC()); err != nil {
				return rep, err
			}
		}
		if len(page) < driftPage {
			return rep, nil
		}
		after = page[len(page)-1].ID
	}
}

// check observes one binding. It reports false when it couldn't tell:
// no adapter, no connection on record, a connection that isn't ready, or
// a cloud that didn't answer. Those change nothing.
func (c *DriftChecker) check(ctx context.Context, b *planning.Binding, warn func(string, error)) (drift, msg string, ok bool) {
	exec := c.Executors[b.Provider]
	if exec == nil || b.ConnectionID == "" {
		return "", "", false
	}
	conn, cred, err := c.Credentials.Credential(ctx, b.OrgID, b.ConnectionID)
	if err != nil {
		return "", "", false
	}
	obs, err := exec.Observe(ctx, conn, cred, b)
	if err != nil {
		warn("check "+b.Name+" for drift", err)
		return "", "", false
	}
	drift, msg = classify(b, obs)
	return drift, msg, true
}

// classify says how what's live differs from what was deployed.
func classify(b *planning.Binding, obs *Observation) (drift, msg string) {
	switch {
	case !obs.Exists:
		return planning.DriftMissing, fmt.Sprintf("%s is gone from %s", b.Name, b.Provider)
	case obs.Revision != "" && b.Revision != "" && obs.Revision != b.Revision:
		return planning.DriftChanged, fmt.Sprintf("%s is live, not %s", obs.Revision, b.Revision)
	}
	return "", ""
}

// Run implements orchestration.DriftChecker.
func (c *DriftChecker) Run(ctx context.Context) error {
	_, err := c.Check(ctx)
	return err
}
