//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// microUSDPerCent is how many micro-USD make a cent.
const microUSDPerCent = 10_000

// Rates in micro-USD per native unit, one for every feature in Features.
// Every rate starts at 0, so nothing is charged until Rex sets one.
func (s *Service) Rates(ctx context.Context) (map[string]int64, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	rows, err := s.pg.Query(ctx, `SELECT feature, micro_usd_per_unit FROM loomplane_billing_rates`)
	if err != nil {
		return nil, fmt.Errorf("billing: read the rates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int64, len(Features))
	for rows.Next() {
		var (
			feature string
			rate    int64
		)
		if err = rows.Scan(&feature, &rate); err != nil {
			return nil, fmt.Errorf("billing: read the rates: %w", err)
		}
		if slices.Contains(Features, feature) {
			out[feature] = rate
		}
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("billing: read the rates: %w", err)
	}
	for _, f := range Features {
		if _, ok := out[f]; !ok {
			return nil, fmt.Errorf("billing: feature %s has no rate; the billing migration seeds one for every feature", f)
		}
	}
	return out, nil
}

// SetRate sets what one native unit of feature costs, in micro-USD, from
// now on. Usage already recorded keeps the price it was recorded at. by
// names who changed it.
func (s *Service) SetRate(ctx context.Context, feature string, microUSD int64, by string) error {
	if err := s.ready(); err != nil {
		return err
	}
	if !slices.Contains(Features, feature) {
		return fmt.Errorf("billing: there is no feature %q to price", feature)
	}
	if microUSD < 0 {
		return fmt.Errorf("billing: the rate for %s can't be negative", feature)
	}
	by = strings.TrimSpace(by)
	if by == "" {
		return errors.New("billing: a rate change needs to say who made it")
	}
	if _, err := s.pg.Exec(ctx,
		`INSERT INTO loomplane_billing_rates (feature, micro_usd_per_unit, updated_at, updated_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (feature) DO UPDATE SET micro_usd_per_unit=EXCLUDED.micro_usd_per_unit, updated_at=EXCLUDED.updated_at, updated_by=EXCLUDED.updated_by`,
		feature, microUSD, s.now().UTC(), by); err != nil {
		return fmt.Errorf("billing: set the rate for %s: %w", feature, err)
	}
	return nil
}

// rate is one feature's current rate.
func (s *Service) rate(ctx context.Context, feature string) (int64, error) {
	var rate int64
	err := s.pg.QueryRow(ctx, `SELECT micro_usd_per_unit FROM loomplane_billing_rates WHERE feature=$1`, feature).Scan(&rate)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("billing: feature %s has no rate; the billing migration seeds one for every feature", feature)
	}
	if err != nil {
		return 0, fmt.Errorf("billing: read the rate for %s: %w", feature, err)
	}
	return rate, nil
}
