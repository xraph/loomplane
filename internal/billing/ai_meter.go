//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	nexuspg "github.com/xraph/nexus/store/postgres"
	"github.com/xraph/nexus/usage"

	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/pglock"
)

// aiPriceLock is the advisory lock one pricing run holds, so the push job
// and the month close never price the same claims side by side.
const aiPriceLock = "loomplane:billing:ai"

// aiClaimWait is how long a claim waits for nexus's usage record before it
// is closed unbilled and logged.
const aiClaimWait = 24 * time.Hour

// aiRecordLead is how long before its claim nexus's record of a request can
// be stamped: nexus stamps the record when the request starts, and the
// claim is made later, when the call reaches the server's key.
const aiRecordLead = time.Minute

// nexusPage is how many usage records one read of nexus asks for, its most.
const nexusPage = 500

// aiUsagePrefix starts the id of every ai_usd usage row: "ai:" and nexus's
// usage record id, so a record is billed once however often it is read.
const aiUsagePrefix = "ai:"

// PriceAIClaims prices each open claim from nexus's usage record for its
// request and records it as ai_usd usage. Claims with no record after 24
// hours are closed and logged.
//
// Nexus's usage records are read per nexus tenant with an open claim, from
// a minute before its oldest open claim. A record whose request was claimed
// bills its CostUSD in dollars at the ai_usd rate, dated when nexus stamped
// it, under the id "ai:<record id>", and settles the claim. A record with
// no claim is a call on the workspace's own key, or an embedding, and is
// never billed. A record nexus couldn't price (no CostUSD) bills nothing
// and is logged with its model.
//
// Safe to run again and on several replicas: one runs at a time, a usage
// row is recorded once per record, and a claim is settled once. One tenant
// failing doesn't hold the others back; the errors come back together.
func (s *Service) PriceAIClaims(ctx context.Context) error {
	if err := s.ready(); err != nil {
		return err
	}
	return pglock.Do(ctx, s.pg, aiPriceLock, s.priceAIClaims)
}

// openTenant is a nexus tenant with open claims, and when its oldest was made.
type openTenant struct {
	tenant string
	oldest time.Time
}

// openClaim is one claim waiting for its record.
type openClaim struct {
	org       string
	claimedAt time.Time
}

func (s *Service) priceAIClaims(ctx context.Context) error {
	now := s.now().UTC()
	tenants, err := s.openClaimTenants(ctx)
	if err != nil {
		return err
	}
	var (
		errs []error
		// unread are the tenants whose records couldn't be read: their
		// claims are never closed unbilled on a failed read.
		unread []string
	)
	for _, t := range tenants {
		if ctxErr := ctx.Err(); ctxErr != nil {
			errs = append(errs, ctxErr)
			unread = append(unread, t.tenant)
			continue
		}
		read, priceErr := s.priceTenant(ctx, t, now)
		if priceErr != nil {
			slog.ErrorContext(ctx, "billing: pricing AI on the server's key failed for a nexus tenant; the next run tries again",
				"tenant", t.tenant, "error", priceErr)
			errs = append(errs, fmt.Errorf("billing: price the AI claims of nexus tenant %s: %w", t.tenant, priceErr))
		}
		if !read {
			unread = append(unread, t.tenant)
		}
	}
	errs = append(errs, s.closeStaleClaims(ctx, now, unread))
	return errors.Join(errs...)
}

// openClaimTenants lists the nexus tenants with open claims.
func (s *Service) openClaimTenants(ctx context.Context) ([]openTenant, error) {
	rows, err := s.pg.Query(ctx, `SELECT nexus_tenant, min(claimed_at) FROM loomplane_billing_ai_claims
		WHERE settled_at IS NULL GROUP BY nexus_tenant ORDER BY nexus_tenant`)
	if err != nil {
		return nil, fmt.Errorf("billing: list the open AI claims: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []openTenant
	for rows.Next() {
		var t openTenant
		if err = rows.Scan(&t.tenant, &t.oldest); err != nil {
			return nil, fmt.Errorf("billing: list the open AI claims: %w", err)
		}
		t.oldest = t.oldest.UTC()
		out = append(out, t)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("billing: list the open AI claims: %w", err)
	}
	return out, nil
}

// openClaims are the tenant's open claims, by nexus request id.
func (s *Service) openClaims(ctx context.Context, tenant string) (map[string]openClaim, error) {
	rows, err := s.pg.Query(ctx, `SELECT request_id, org_id, claimed_at FROM loomplane_billing_ai_claims
		WHERE nexus_tenant = $1 AND settled_at IS NULL`, tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]openClaim{}
	for rows.Next() {
		var (
			requestID string
			c         openClaim
		)
		if scanErr := rows.Scan(&requestID, &c.org, &c.claimedAt); scanErr != nil {
			return nil, scanErr
		}
		c.claimedAt = c.claimedAt.UTC()
		out[requestID] = c
	}
	return out, rows.Err()
}

// priceTenant prices the tenant's open claims from nexus's records. read
// says every page of records was read; a claim that failed to record stays
// open for the next run.
func (s *Service) priceTenant(ctx context.Context, t openTenant, now time.Time) (read bool, err error) {
	claims, err := s.openClaims(ctx, t.tenant)
	if err != nil {
		return false, fmt.Errorf("read the open claims: %w", err)
	}
	if len(claims) == 0 {
		return true, nil // settled by now
	}
	// No end time: a record stamped at this instant, or by a replica whose
	// clock runs ahead, is priced now rather than on the next run.
	opts := &usage.QueryOptions{TenantID: t.tenant, StartTime: t.oldest.Add(-aiRecordLead), Limit: nexusPage}
	store := nexuspg.New(s.db).Usage()
	var errs []error
	for {
		page, qErr := store.Query(ctx, opts)
		if qErr != nil {
			return false, errors.Join(append(errs, fmt.Errorf("read nexus's usage records: %w", qErr))...)
		}
		for _, rec := range page.Items {
			requestID := rec.RequestID.String()
			c, ok := claims[requestID]
			if !ok {
				continue // the workspace's own key, or an embedding: never billed
			}
			if recErr := s.priceRecord(ctx, requestID, c, rec, now); recErr != nil {
				errs = append(errs, fmt.Errorf("price request %s: %w", requestID, recErr))
			}
		}
		if page.NextCursor == "" {
			return true, errors.Join(errs...)
		}
		opts.Cursor = page.NextCursor
	}
}

// priceRecord bills one claimed request at nexus's cost and settles its
// claim.
func (s *Service) priceRecord(ctx context.Context, requestID string, c openClaim, rec *usage.Record, now time.Time) error {
	dollars := 0.0
	if rec.CostUSD == nil {
		slog.WarnContext(ctx, "billing: nexus couldn't price an AI request on the server's key; it bills nothing",
			"org", c.org, "request", requestID, "provider", rec.Provider, "model", rec.Model, "pricing", string(rec.PricingStatus))
	} else if d, err := strconv.ParseFloat(rec.CostUSD.String(), 64); err != nil || math.IsNaN(d) || math.IsInf(d, 0) || d < 0 {
		slog.ErrorContext(ctx, "billing: nexus recorded a cost billing can't read for an AI request; it bills nothing",
			"org", c.org, "request", requestID, "model", rec.Model, "cost_usd", rec.CostUSD.String())
	} else {
		dollars = d
	}
	occurred := rec.CreatedAt.UTC()
	if occurred.IsZero() {
		occurred = c.claimedAt
	}
	usageID := aiUsagePrefix + rec.ID.String()
	if err := s.Record(ctx, metering.Event{ID: usageID, OrgID: c.org, Feature: FeatureAIUSD, ResourceRef: requestID,
		Quantity: dollars, OccurredAt: occurred, PlatformOwned: true}); err != nil {
		return err
	}
	if _, err := s.pg.Exec(ctx, `UPDATE loomplane_billing_ai_claims SET usage_id = $2, settled_at = $3
		WHERE request_id = $1 AND settled_at IS NULL`, requestID, usageID, now); err != nil {
		return fmt.Errorf("settle the claim: %w", err)
	}
	return nil
}

// closeStaleClaims settles, unbilled, every claim still open a day after it
// was made, except those of the tenants whose records couldn't be read
// this run, and logs each one.
func (s *Service) closeStaleClaims(ctx context.Context, now time.Time, unread []string) error {
	if unread == nil {
		unread = []string{}
	}
	rows, err := s.pg.Query(ctx, `UPDATE loomplane_billing_ai_claims SET settled_at = $1
		WHERE settled_at IS NULL AND claimed_at <= $2 AND NOT (nexus_tenant = ANY($3))
		RETURNING request_id, org_id, nexus_tenant, claimed_at`, now, now.Add(-aiClaimWait), unread)
	if err != nil {
		return fmt.Errorf("billing: close the AI claims nexus never recorded: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			requestID, org, tenant string
			claimedAt              time.Time
		)
		if err = rows.Scan(&requestID, &org, &tenant, &claimedAt); err != nil {
			return fmt.Errorf("billing: close the AI claims nexus never recorded: %w", err)
		}
		slog.WarnContext(ctx, "billing: an AI request on the server's key has no nexus usage record after a day; it is closed unbilled",
			"org", org, "request", requestID, "tenant", tenant, "claimed_at", claimedAt.UTC().Format(time.RFC3339))
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("billing: close the AI claims nexus never recorded: %w", err)
	}
	return nil
}
