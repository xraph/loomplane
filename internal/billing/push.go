//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/xraph/grove/driver"
	"github.com/xraph/ledger/id"
	"github.com/xraph/ledger/meter"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/pglock"
)

// pushLock is the advisory lock one replica holds while it pushes.
const pushLock = "loomplane:billing:push"

// PushUsage moves priced usage into ledger as whole cents per
// (workspace, feature, month), at most once each, and returns the
// workspaces it touched. Safe to run on several replicas and after a crash.
//
// It works as an outbox. The batch step sums the usage not yet pushed into
// one push row per workspace, feature and calendar month (UTC), carrying
// what is left under a cent to the next push of the same month; the send
// step hands every unsent push to ledger under an idempotency key of its
// own, so a push resent after a crash is dropped by ledger and never counts
// twice. A replica that finds another pushing returns at once with nothing
// touched. The workspaces touched are those with a push batched or sent in
// this run, sorted.
func (s *Service) PushUsage(ctx context.Context) (touched []string, err error) {
	if rErr := s.ready(); rErr != nil {
		return nil, rErr
	}
	_, err = pglock.Try(ctx, s.pg, pushLock, func(ctx context.Context) error {
		orgs := map[string]bool{}
		batched, bErr := s.batchUsage(ctx)
		for _, o := range batched {
			orgs[o] = true
		}
		// A failed batch rolls back whole; pushes batched earlier still go.
		sent, sErr := s.sendPushes(ctx)
		for _, o := range sent {
			orgs[o] = true
		}
		for o := range orgs {
			touched = append(touched, o)
		}
		slices.Sort(touched)
		return errors.Join(bErr, sErr)
	})
	return touched, err
}

// usageGroup is one workspace's unpushed usage of one feature in one month.
type usageGroup struct {
	org, feature string
	period       time.Time // 00:00 UTC on the 1st
}

// batchUsage turns every group of unpushed usage into a push row, in one
// transaction, and returns the workspaces it batched.
func (s *Service) batchUsage(ctx context.Context) ([]string, error) {
	tx, err := s.pg.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("billing: begin the usage batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // a no-op after commit

	rows, err := tx.Query(ctx, `SELECT DISTINCT org_id, feature, date_trunc('month', occurred_at AT TIME ZONE 'UTC')::date
		FROM loomplane_billing_usage WHERE push_id IS NULL ORDER BY 1, 2, 3`)
	if err != nil {
		return nil, fmt.Errorf("billing: list the usage to push: %w", err)
	}
	var groups []usageGroup
	for rows.Next() {
		var g usageGroup
		if err = rows.Scan(&g.org, &g.feature, &g.period); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("billing: list the usage to push: %w", err)
		}
		g.period = monthOf(g.period)
		groups = append(groups, g)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, fmt.Errorf("billing: list the usage to push: %w", err)
	}

	var orgs []string
	for _, g := range groups {
		made, gErr := s.batchGroup(ctx, tx, g)
		if gErr != nil {
			return nil, fmt.Errorf("billing: batch %s usage of %s for %s: %w", g.feature, g.org, g.period.Format("2006-01"), gErr)
		}
		if made && !slices.Contains(orgs, g.org) {
			orgs = append(orgs, g.org)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("billing: commit the usage batch: %w", err)
	}
	return orgs, nil
}

// batchGroup makes one push of a group's unpushed rows, adding the
// month's carried remainder, and reports whether it made one. Only the rows
// it summed are marked, by id, so usage recorded meanwhile waits for the
// next push.
func (s *Service) batchGroup(ctx context.Context, tx driver.Tx, g usageGroup) (bool, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO loomplane_billing_carry (org_id, feature, period, micro_usd_remainder)
		VALUES ($1, $2, $3, 0) ON CONFLICT DO NOTHING`, g.org, g.feature, g.period); err != nil {
		return false, err
	}
	var carry int64
	if err := tx.QueryRow(ctx, `SELECT micro_usd_remainder FROM loomplane_billing_carry
		WHERE org_id=$1 AND feature=$2 AND period=$3 FOR UPDATE`, g.org, g.feature, g.period).Scan(&carry); err != nil {
		return false, err
	}

	rows, err := tx.Query(ctx, `SELECT id, micro_usd, occurred_at FROM loomplane_billing_usage
		WHERE org_id=$1 AND feature=$2 AND push_id IS NULL AND occurred_at >= $3 AND occurred_at < $4
		ORDER BY id FOR UPDATE SKIP LOCKED`, g.org, g.feature, g.period, g.period.AddDate(0, 1, 0))
	if err != nil {
		return false, err
	}
	var (
		ids    []string
		sum    int64
		latest time.Time
	)
	for rows.Next() {
		var (
			usageID string
			micro   int64
			at      time.Time
		)
		if err = rows.Scan(&usageID, &micro, &at); err != nil {
			_ = rows.Close()
			return false, err
		}
		if micro < 0 || sum > math.MaxInt64-micro {
			_ = rows.Close()
			return false, fmt.Errorf("usage %s doesn't add up (%d micro-USD on %d)", usageID, micro, sum)
		}
		ids = append(ids, usageID)
		sum += micro
		if at.After(latest) {
			latest = at
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return false, err
	}
	if len(ids) == 0 {
		return false, nil // another push holds them
	}
	if sum > math.MaxInt64-carry {
		return false, fmt.Errorf("the month's usage overflows (%d micro-USD on %d)", sum, carry)
	}

	total := sum + carry
	cents, remainder := total/microUSDPerCent, total%microUSDPerCent
	pid, err := typeid.Generate("lpush")
	if err != nil {
		return false, err
	}
	var sentAt *time.Time
	if cents == 0 {
		now := s.now().UTC()
		sentAt = &now // nothing to send
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_billing_pushes (id, org_id, feature, period, cents, at, sent_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, pid.String(), g.org, g.feature, g.period, cents, latest.UTC(), sentAt); err != nil {
		return false, err
	}
	res, err := tx.Exec(ctx, `UPDATE loomplane_billing_usage SET push_id=$1 WHERE id = ANY($2) AND push_id IS NULL`, pid.String(), ids)
	if err != nil {
		return false, err
	}
	if n, rErr := res.RowsAffected(); rErr != nil || n != int64(len(ids)) {
		return false, errors.Join(fmt.Errorf("marked %d of the %d rows summed", n, len(ids)), rErr)
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_billing_carry SET micro_usd_remainder=$4
		WHERE org_id=$1 AND feature=$2 AND period=$3`, g.org, g.feature, g.period, remainder); err != nil {
		return false, err
	}
	return true, nil
}

// push is a push row not yet sent.
type push struct {
	id, org, feature string
	period, at       time.Time
	cents            int64
}

// sendPushes hands every unsent push to ledger and marks it sent, and
// returns the workspaces it sent for. A crash between the two leaves the
// push unsent; the retry sends the same idempotency key, which ledger
// drops. One push failing doesn't hold the others back.
func (s *Service) sendPushes(ctx context.Context) ([]string, error) {
	rows, err := s.pg.Query(ctx, `SELECT id, org_id, feature, period, cents, at FROM loomplane_billing_pushes
		WHERE sent_at IS NULL ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("billing: list the pushes to send: %w", err)
	}
	var due []push
	for rows.Next() {
		var p push
		if err = rows.Scan(&p.id, &p.org, &p.feature, &p.period, &p.cents, &p.at); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("billing: list the pushes to send: %w", err)
		}
		p.period, p.at = monthOf(p.period), p.at.UTC()
		due = append(due, p)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, fmt.Errorf("billing: list the pushes to send: %w", err)
	}

	var (
		orgs []string
		errs []error
	)
	for _, p := range due {
		if sErr := s.send(ctx, p); sErr != nil {
			errs = append(errs, fmt.Errorf("billing: send push %s: %w", p.id, sErr))
			continue
		}
		if !slices.Contains(orgs, p.org) {
			orgs = append(orgs, p.org)
		}
	}
	return orgs, errors.Join(errs...)
}

func (s *Service) send(ctx context.Context, p push) error {
	if p.cents > 0 {
		var invoiced bool
		if err := s.pg.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM loomplane_billing_invoices
			WHERE org_id=$1 AND period_start >= $2 AND period_start < $3)`, p.org, p.period, p.period.AddDate(0, 1, 0)).Scan(&invoiced); err != nil {
			return err
		}
		if invoiced {
			slog.WarnContext(ctx, "billing: usage for a month already invoiced",
				"org", p.org, "feature", p.feature, "month", p.period.Format("2006-01"), "cents", p.cents)
		}
		if err := s.l.Store().IngestBatch(ctx, []*meter.UsageEvent{{
			ID: id.NewUsageEventID(), TenantID: p.org, AppID: s.appID, FeatureKey: p.feature,
			Quantity: p.cents, Timestamp: p.at, IdempotencyKey: "loomplane:" + p.id,
		}}); err != nil {
			return err
		}
	}
	_, err := s.pg.Exec(ctx, `UPDATE loomplane_billing_pushes SET sent_at=$2 WHERE id=$1 AND sent_at IS NULL`, p.id, s.now().UTC())
	return err
}

// monthOf is 00:00 UTC on the 1st of t's month, read as a date in UTC.
func monthOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
