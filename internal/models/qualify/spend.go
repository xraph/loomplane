package qualify

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/digitalocean"
)

// Spend reads the qualification account's spend and identity.
type Spend interface {
	MonthToDateUSD(ctx context.Context) (string, error) // GET /v2/customers/my/balance month_to_date_usage
	AccountUUID(ctx context.Context) (string, error)    // GET /v2/account
}

// ErrOverCap is a Droplet that could take spend past a cap.
var ErrOverCap = errors.New("over the qualification spend cap")

// OverCapError says which cap a Droplet could break, and by how much.
type OverCapError struct {
	Cap       string  // "monthly" or "per-run"
	CapUSD    float64 // the cap
	SpentUSD  float64 // the month to date, or this run so far
	HourlyUSD float64
	Hours     float64
}

func (e *OverCapError) Error() string {
	cost := e.HourlyUSD * e.Hours
	return fmt.Sprintf("A GPU Droplet at %s an hour for %s hours could cost %s, taking %s qualification spend from %s to %s, past the %s cap.",
		usd(e.HourlyUSD), strconv.FormatFloat(e.Hours, 'f', -1, 64), usd(cost), e.whose(), usd(e.SpentUSD), usd(e.SpentUSD+cost), usd(e.CapUSD))
}

func (e *OverCapError) whose() string {
	if e.Cap == "monthly" {
		return "this month's"
	}
	return "this run's"
}

// Unwrap makes errors.Is(err, ErrOverCap) true.
func (e *OverCapError) Unwrap() error { return ErrOverCap }

// ErrSpendUnknown is spend Guard can't read. Unknown is never zero, so
// nothing starts.
var ErrSpendUnknown = errors.New("the qualification account's spend is unknown")

// Guard decides whether a Droplet at hourlyUSD for hours may start now.
// It checks the per-run cap first, against spentThisRun plus the
// Droplet's worst case, then reads the month to date and checks the
// monthly cap the same way. A price, a number of hours or a balance it
// can't read refuses: an unknown is never treated as zero.
func Guard(ctx context.Context, s Spend, cfg Config, hourlyUSD, hours, spentThisRun float64) error {
	monthly, run, err := cfg.caps()
	if err != nil {
		return err
	}
	if !finite(hourlyUSD) || hourlyUSD <= 0 {
		return fmt.Errorf("%w: the Droplet's hourly price is unknown, so nothing starts", ErrSpendUnknown)
	}
	if !finite(hours) || hours <= 0 {
		return fmt.Errorf("%w: the run has no time left", ErrOverCap)
	}
	if !finite(spentThisRun) || spentThisRun < 0 {
		return fmt.Errorf("%w: this run's spend so far is unknown", ErrSpendUnknown)
	}
	cost := hourlyUSD * hours
	if cents(spentThisRun+cost) > cents(run) {
		return &OverCapError{Cap: "per-run", CapUSD: run, SpentUSD: spentThisRun, HourlyUSD: hourlyUSD, Hours: hours}
	}
	mtd, err := MonthToDate(ctx, s)
	if err != nil {
		return err
	}
	if cents(mtd+cost) > cents(monthly) {
		return &OverCapError{Cap: "monthly", CapUSD: monthly, SpentUSD: mtd, HourlyUSD: hourlyUSD, Hours: hours}
	}
	return nil
}

// MonthToDate reads and parses the month's spend. A failure or an answer
// that isn't a dollar amount is ErrSpendUnknown.
func MonthToDate(ctx context.Context, s Spend) (float64, error) {
	raw, err := s.MonthToDateUSD(ctx)
	if err != nil {
		return 0, fmt.Errorf("%w: couldn't read this month's spend, so nothing starts: %w", ErrSpendUnknown, err)
	}
	mtd, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || !finite(mtd) || mtd < 0 {
		return 0, fmt.Errorf("%w: DigitalOcean answered a month to date of %q", ErrSpendUnknown, raw)
	}
	return mtd, nil
}

// AtCap reports whether the month to date has reached the monthly cap.
func AtCap(mtd float64, cfg Config) bool {
	monthly, _, err := cfg.caps()
	return err != nil || cents(mtd) >= cents(monthly)
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// cents rounds up to a whole cent, so float noise never lets a cost slip
// under a cap.
func cents(v float64) int64 { return int64(math.Ceil(v*100 - 1e-6)) }

// DigitalOceanSpend is Spend on DigitalOcean's API.
type DigitalOceanSpend struct {
	client *digitalocean.Client
	token  string
}

// NewDigitalOceanSpend reads spend with token. The token is kept out of
// every error and string.
func NewDigitalOceanSpend(client *digitalocean.Client, token string) *DigitalOceanSpend {
	return &DigitalOceanSpend{client: client, token: token}
}

func (d *DigitalOceanSpend) String() string { return "DigitalOcean spend" }

// MonthToDateUSD implements Spend.
func (d *DigitalOceanSpend) MonthToDateUSD(ctx context.Context) (string, error) {
	spend, err := d.client.MonthToDate(ctx, d.token, time.Now())
	if err != nil {
		return "", err
	}
	return strconv.FormatFloat(spend.Total, 'f', 2, 64), nil
}

// AccountUUID implements Spend. A token belongs to one team; its UUID is
// the account, as Loomplane's server connections read it.
func (d *DigitalOceanSpend) AccountUUID(ctx context.Context) (string, error) {
	a, err := d.client.Account(ctx, d.token)
	if err != nil {
		return "", err
	}
	if a.Team != nil && a.Team.UUID != "" {
		return a.Team.UUID, nil
	}
	return a.UUID, nil
}
