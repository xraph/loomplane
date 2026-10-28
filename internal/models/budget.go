package models

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/loomplane/internal/catalogstate"
	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/notifications"
)

// GPUPrices reads the GPU targets of an organization's connected servers
// and clusters, with their prices, and no principal. *gpus.Service is the
// real one.
type GPUPrices interface {
	ConnectedTargets(ctx context.Context, orgID string) ([]gpus.GPUTarget, error)
}

// BudgetNotices queues a model's budget notice for the organization's
// admins, inside the transaction that records it went out.
// *notifications.Service is the real one.
type BudgetNotices interface {
	EnqueueModelBudgetNoticeIn(ctx context.Context, tx notifications.Execer, n notifications.ModelBudgetNotice) error
}

var (
	_ GPUPrices     = (*gpus.Service)(nil)
	_ BudgetNotices = (*notifications.Service)(nil)
)

// Accrue adds what each model deployment's GPUs cost since its last
// accrual to its month, and queues its budget notices. The Dispatch cron
// "ai-models.accrue" runs it every five minutes, for every organization
// with model deployments, one organization's rows at a time.
//
// It never stops or changes a model: a budget is an alert, never a
// cutoff. It writes only the row's month, cost and the months its notices
// went out for.
//
// A model counts while its deployment state is ready or deploying, at the
// price of the GPUs its deployed engine holds. A model on CPU adds 0, and
// its cost is its server's. One whose price isn't known adds nothing and
// sends no notice: an unknown price is never $0. Either way the row's
// accrual time moves on, so a price that becomes known later isn't
// charged for the time before. The first time a row is seen it only
// starts the clock.
//
// Two runs over the same rows count each interval once: Store.Accrue
// refuses a row accrued since it was read, and the later run skips it.
// A model added to the manifest by hand has no row, so it isn't counted
// here; the page still shows what it costs.
func (s *Service) Accrue(ctx context.Context, now time.Time) error {
	if s.d.Waiting == nil || s.d.Store == nil || s.d.Deployed == nil {
		return nil
	}
	orgs, err := s.d.Waiting.Organizations(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, org := range orgs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if oerr := s.accrueOrg(ctx, org, now.UTC()); oerr != nil {
			errs = append(errs, fmt.Errorf("organization %s: %w", org, oerr))
		}
	}
	return errors.Join(errs...)
}

// accrueOrg accrues one organization's rows. Its GPU prices are read
// once, the first time a row needs them.
func (s *Service) accrueOrg(ctx context.Context, orgID string, now time.Time) error {
	rows, err := s.d.Store.List(ctx, orgID)
	if err != nil {
		return err
	}
	var (
		targets []gpus.GPUTarget
		read    bool
	)
	prices := func() ([]gpus.GPUTarget, error) {
		if read {
			return targets, nil
		}
		if s.d.Prices == nil {
			read = true
			return nil, nil
		}
		t, perr := s.d.Prices.ConnectedTargets(ctx, orgID)
		if perr != nil {
			return nil, perr
		}
		targets, read = t, true
		return targets, nil
	}
	var errs []error
	for _, d := range rows {
		rerr := s.accrueRow(ctx, d, now, prices)
		// Stale: another run counted this interval, or a later month.
		if rerr != nil && !errors.Is(rerr, ErrStale) && !errors.Is(rerr, ErrNotFound) {
			errs = append(errs, fmt.Errorf("model deployment %s: %w", d.ID, rerr))
		}
	}
	return errors.Join(errs...)
}

// accrueRow adds what d cost from its last accrual, or the start of the
// month when that's later, up to now, and sends its budget notices. A
// read that fails accrues nothing, so the next run counts the time.
func (s *Service) accrueRow(ctx context.Context, d *Deployment, now time.Time, prices func() ([]gpus.GPUTarget, error)) error {
	if !d.AccruedAt.IsZero() && !now.After(d.AccruedAt) {
		return nil
	}
	cost, err := s.runningCost(ctx, d, prices)
	if err != nil {
		return err
	}
	var add int64
	at := now
	if !d.AccruedAt.IsZero() && cost.Known {
		hourly, perr := costAmount.parse(cost.HourlyUSD)
		if perr != nil {
			return perr
		}
		from := d.AccruedAt
		if start := monthStart(now); from.Before(start) {
			from = start
		}
		// Whole seconds are counted, and the clock stops at the last one, so
		// the part of a second left over counts in the next run.
		seconds := int64(now.Sub(from) / time.Second)
		if seconds <= 0 {
			return nil
		}
		add = (hourly*seconds + 1800) / 3600
		at = from.Add(time.Duration(seconds) * time.Second)
	}
	if aerr := s.d.Store.Accrue(ctx, d, monthOf(now), formatUSD(add), at); aerr != nil {
		return aerr
	}
	return s.budgetNotice(ctx, d, cost, now)
}

// runningCost is what d's GPUs cost per hour while it runs, from the
// binding of its deployed engine: unknown while it isn't ready or
// deploying, or when its GPUs or their price aren't known (Ruling M25). A
// CPU engine's cost is its server's, "0.00" with source cpu. An override
// wins once the model runs. On a server Loomplane created for d, with no
// other workload holding its GPUs, it's the whole server (Ruling M42).
func (s *Service) runningCost(ctx context.Context, d *Deployment, prices func() ([]gpus.GPUTarget, error)) (Cost, error) {
	unknown := Cost{Source: CostSourceUnknown}
	fact, err := s.d.Deployed.State(ctx, d.OrgID, d.ProjectID, d.EnvironmentID, d.Resource)
	if err != nil {
		return Cost{}, err
	}
	if fact.Value != catalogstate.Ready && fact.Value != catalogstate.Deploying {
		return unknown, nil
	}
	override := HourlyCost(gpus.GPUTarget{}, 0, d.HourlyUSDOverride)
	bindings, err := s.d.Deployed.Bindings(ctx, d.OrgID, d.EnvironmentID)
	if err != nil {
		return Cost{}, err
	}
	b, engine := engineBinding(bindings, d.Resource)
	switch {
	case engine == nil:
		// Nothing deployed it yet, so what it holds isn't known.
		if override.Source == CostSourceOverride {
			return override, nil
		}
		return unknown, nil
	case !gpuKind(engine.Kind):
		return override, nil // cpu, or the override
	case engine.GPU == nil || engine.GPU.Count <= 0:
		if override.Source == CostSourceOverride {
			return override, nil
		}
		return unknown, nil
	}
	targets, err := prices()
	if err != nil {
		return Cost{}, err
	}
	var host gpus.GPUTarget
	for _, t := range targets {
		if t.Source == gpus.SourceConnection && t.ConnectionID == b.ConnectionID && t.Accelerator.Kind == engine.GPU.Kind {
			host = t
			break
		}
	}
	if ownsServer(d) {
		// A server Loomplane created for the model is all the model's until
		// another workload holds a GPU there.
		if c, ok := wholeServerCost(host, engine.GPU.Count, d.HourlyUSDOverride); ok {
			return c, nil
		}
	}
	return HourlyCost(host, engine.GPU.Count, d.HourlyUSDOverride), nil
}

// budgetNotice queues the one budget notice d is due this month, if any:
// "crossed" once what it cost passes its budget, else "projected" once
// what it cost plus its price for the hours left reaches it. Each goes out
// once a month, recorded in the same transaction that queues it. A model
// on CPU, or whose price isn't known, gets none.
func (s *Service) budgetNotice(ctx context.Context, d *Deployment, cost Cost, now time.Time) error {
	if s.d.Budget == nil || d.MonthlyBudgetUSD == "" || !cost.Known || cost.Source == CostSourceCPU {
		return nil
	}
	month := monthOf(now)
	if d.Month != month {
		return nil
	}
	budget, err := budgetAmount.parse(d.MonthlyBudgetUSD)
	if err != nil || budget <= 0 {
		return err
	}
	spent, err := costAmount.parse(d.MonthCostUSD)
	if err != nil {
		return err
	}
	n := notifications.ModelBudgetNotice{
		OrgID: d.OrgID, ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, DeploymentID: d.ID, Resource: d.Resource,
		Month: month, BudgetUSD: centsText(budget),
	}
	switch {
	case spent >= budget:
		if d.AlertedCrossed == month {
			return nil
		}
		n.Kind, n.MonthToDateUSD = notifications.ModelBudgetCrossed, centsText(spent)
	default:
		total := projected(cost, d.MonthCostUSD, now)
		units, perr := costAmount.parse(total)
		if total == "" || perr != nil || units < budget || d.AlertedProjected == month {
			return nil
		}
		n.Kind, n.ProjectedUSD = notifications.ModelBudgetProjected, total
	}
	_, err = s.d.Store.MarkAlerted(ctx, d.OrgID, d.ID, n.Kind, month, func(ctx context.Context, tx notifications.Execer) error {
		return s.d.Budget.EnqueueModelBudgetNoticeIn(ctx, tx, n)
	})
	return err
}

// monthOf is t's month in UTC, as rows keep it: "2026-11".
func monthOf(t time.Time) string { return t.UTC().Format("2006-01") }

// monthStart is the first instant of t's month in UTC.
func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// centsText writes ten-thousandths of a dollar to the cent, half up.
func centsText(units int64) string {
	cents := (units + 50) / 100
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}
