//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/metering"
)

var _ metering.Meter = (*Service)(nil)

// aiRequestPrefix starts the id of every claim on the server's AI key.
const aiRequestPrefix = "ai-request:"

// maxMicroUSD bounds one event's price (a billion dollars), well inside an
// int64. A price past it is a wrong rate or quantity, not usage. The push
// checks its own sums as well.
const maxMicroUSD = 1e15

// Record implements metering.Meter. It never fails on a duplicate id and
// runs on its own connection: platform Cloud calls it inside its own
// transaction (meter.go), and billing must not join or block that.
//
// A claim on the server's AI key is kept for pricing later. Usage nobody
// but the workspace pays for (not platform-owned) is not billed. Anything
// else is priced at the feature's rate now and keeps that price.
func (s *Service) Record(ctx context.Context, e metering.Event) error {
	if err := s.ready(); err != nil {
		return err
	}
	if e.Feature == agentsettings.FeatureAIPlatformRequests {
		return s.claim(ctx, e)
	}
	if !e.PlatformOwned {
		return nil
	}
	if !slices.Contains(Features, e.Feature) {
		return fmt.Errorf("billing: event %s is for feature %q, which billing doesn't price", e.ID, e.Feature)
	}
	if err := checkEvent(e); err != nil {
		return err
	}
	if math.IsNaN(e.Quantity) || math.IsInf(e.Quantity, 0) || e.Quantity < 0 {
		return fmt.Errorf("billing: event %s has quantity %v; usage is never negative", e.ID, e.Quantity)
	}
	rate, err := s.rate(ctx, e.Feature)
	if err != nil {
		return err
	}
	price := math.Round(e.Quantity * float64(rate))
	if price > maxMicroUSD {
		return fmt.Errorf("billing: event %s costs %.0f micro-USD, more than one event can", e.ID, price)
	}
	micro := int64(price)
	res, err := s.pg.Exec(ctx,
		`INSERT INTO loomplane_billing_usage (id, org_id, feature, quantity, rate_micro_usd, micro_usd, occurred_at, resource_ref)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (id) DO NOTHING`,
		e.ID, e.OrgID, e.Feature, e.Quantity, rate, micro, e.OccurredAt.UTC(), e.ResourceRef)
	if err != nil {
		return fmt.Errorf("billing: record usage %s: %w", e.ID, err)
	}
	if n, rErr := res.RowsAffected(); rErr == nil && n == 0 {
		s.checkDuplicate(ctx, e)
	}
	return nil
}

// checkDuplicate logs a replayed id that carries different usage from the
// first. The first is what bills; a difference means a producer reused an
// id, which nobody should find out about from an invoice.
func (s *Service) checkDuplicate(ctx context.Context, e metering.Event) {
	var (
		org, feature string
		quantity     float64
	)
	if err := s.pg.QueryRow(ctx, `SELECT org_id, feature, quantity FROM loomplane_billing_usage WHERE id=$1`, e.ID).
		Scan(&org, &feature, &quantity); err != nil {
		return
	}
	if org != e.OrgID || feature != e.Feature || quantity != e.Quantity {
		slog.WarnContext(ctx, "billing: a usage id came again with different usage; the first one bills",
			"id", e.ID, "org", e.OrgID, "feature", e.Feature)
	}
}

// claim keeps a call made on the server's AI key, so billing can price it
// from nexus's own cost record. The request id follows "ai-request:" in
// the event's id, and the resource is "<nexus tenant>:<request id>".
func (s *Service) claim(ctx context.Context, e metering.Event) error {
	requestID, ok := strings.CutPrefix(e.ID, aiRequestPrefix)
	if !ok || requestID == "" {
		return fmt.Errorf("billing: AI claim %q has no request id", e.ID)
	}
	tenant, ok := strings.CutSuffix(e.ResourceRef, ":"+requestID)
	if !ok || tenant == "" {
		return fmt.Errorf("billing: AI claim %s doesn't name its nexus tenant", e.ID)
	}
	if err := checkEvent(e); err != nil {
		return err
	}
	if _, err := s.pg.Exec(ctx,
		`INSERT INTO loomplane_billing_ai_claims (request_id, org_id, nexus_tenant, claimed_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (request_id) DO NOTHING`,
		requestID, e.OrgID, tenant, e.OccurredAt.UTC()); err != nil {
		return fmt.Errorf("billing: claim AI request %s: %w", requestID, err)
	}
	return nil
}

// checkEvent refuses an event billing can't place: one with no id, no
// workspace or no time.
func checkEvent(e metering.Event) error {
	switch {
	case e.ID == "":
		return errors.New("billing: a usage event needs an id")
	case e.OrgID == "":
		return fmt.Errorf("billing: event %s names no workspace", e.ID)
	case e.OccurredAt.IsZero():
		return fmt.Errorf("billing: event %s has no time", e.ID)
	}
	return nil
}
