package qualify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Spend reads the qualification account's spend and identity.
type Spend interface {
	MonthToDateUSD(ctx context.Context) (string, error) // GET /v2/customers/my/balance month_to_date_usage
	AccountUUID(ctx context.Context) (string, error)    // GET /v2/account
	// BalanceGeneratedAt is when DigitalOcean last worked out the balance
	// (generated_at). Usage after it isn't in MonthToDateUSD yet.
	BalanceGeneratedAt(ctx context.Context) (time.Time, error)
	// LastDropletActivity is the latest time any Droplet action in the
	// account finished (or started, when it hasn't); zero when the account
	// has no actions at all.
	LastDropletActivity(ctx context.Context) (time.Time, error)
}

// ErrStaleBalance is a balance that may not hold the last run yet: the
// month to date may be missing it, so the monthly cap can't be checked.
var ErrStaleBalance = errors.New("the balance may not hold the last qualification run yet")

// CheckBalanceCurrent refuses a run while DigitalOcean's balance may still
// leave out the last run. That's when the balance was generated before the
// account's last Droplet activity, or when that activity was less than lag
// ago: generated_at can be the time of the response rather than of the
// usage it counts, so only time settles it. Weekly runs are far past the
// lag; a same-day rerun waits. Anything it can't read refuses. The run in
// progress is never in the balance either, which is why Guard adds
// spentThisRun to the month to date itself.
func CheckBalanceCurrent(ctx context.Context, s Spend, now time.Time, lag time.Duration) error {
	generated, err := s.BalanceGeneratedAt(ctx)
	if err != nil {
		return fmt.Errorf("%w: couldn't read when the balance was generated: %w", ErrSpendUnknown, err)
	}
	last, err := s.LastDropletActivity(ctx)
	if err != nil {
		return fmt.Errorf("%w: couldn't tell when the last Droplet stopped: %w", ErrSpendUnknown, err)
	}
	if last.IsZero() {
		return nil
	}
	if last.After(generated) {
		return fmt.Errorf("%w: DigitalOcean generated the balance at %s, before the last Droplet activity at %s. Try again once the balance is regenerated",
			ErrStaleBalance, generated.UTC().Format(time.RFC3339), last.UTC().Format(time.RFC3339))
	}
	if wait := last.Add(lag); now.Before(wait) {
		return fmt.Errorf("%w: the last Droplet activity was at %s, and the balance can trail usage by up to %s, so a run can start from %s",
			ErrStaleBalance, last.UTC().Format(time.RFC3339), lag, wait.UTC().Format(time.RFC3339))
	}
	return nil
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
// monthly cap against it plus spentThisRun plus the worst case: the
// balance never holds the run in progress, so this run's spend so far is
// added to it. A price, a number of hours or a
// balance it can't read refuses: an unknown is never treated as zero.
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
	if cents(mtd+spentThisRun+cost) > cents(monthly) {
		return &OverCapError{Cap: "monthly", CapUSD: monthly, SpentUSD: mtd + spentThisRun, HourlyUSD: hourlyUSD, Hours: hours}
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
	api doAPI
}

// NewDigitalOceanSpend reads spend from endpoint, or DigitalOcean's own API
// when it's empty, with token. The token is kept out of every error and
// string.
func NewDigitalOceanSpend(endpoint, token string) *DigitalOceanSpend {
	return &DigitalOceanSpend{api: newDOAPI(endpoint, token)}
}

func (d *DigitalOceanSpend) String() string { return "DigitalOcean spend" }

type balance struct {
	MonthToDateUsage string    `json:"month_to_date_usage"`
	GeneratedAt      time.Time `json:"generated_at"`
}

func (d *DigitalOceanSpend) balance(ctx context.Context) (balance, error) {
	var b balance
	if err := d.api.do(ctx, http.MethodGet, "/v2/customers/my/balance", nil, nil, &b); err != nil {
		return b, fmt.Errorf("read the balance (the token needs billing:read and an owner, admin or biller): %w", err)
	}
	return b, nil
}

// MonthToDateUSD implements Spend.
func (d *DigitalOceanSpend) MonthToDateUSD(ctx context.Context) (string, error) {
	b, err := d.balance(ctx)
	return b.MonthToDateUsage, err
}

// BalanceGeneratedAt implements Spend.
func (d *DigitalOceanSpend) BalanceGeneratedAt(ctx context.Context) (time.Time, error) {
	b, err := d.balance(ctx)
	if err == nil && b.GeneratedAt.IsZero() {
		err = errors.New("the balance doesn't say when it was generated")
	}
	return b.GeneratedAt, err
}

// LastDropletActivity implements Spend from the account's actions: the
// latest time any Droplet action finished, or started when it hasn't,
// whatever its type. The account holds nothing but qualification
// Droplets, so a run's create, power and destroy actions all land here and
// the last of them is no earlier than when its Droplet stopped billing.
//
// It fails closed: an action list with actions but none on a Droplet, a
// Droplet action without a type or a start time, or a failed call is an
// error, never "no activity".
func (d *DigitalOceanSpend) LastDropletActivity(ctx context.Context) (time.Time, error) {
	var last time.Time
	var actions, droplets int
	err := d.api.pages(ctx, "/v2/actions", nil, func(raw json.RawMessage) error {
		var page struct {
			Actions []struct {
				Type         string     `json:"type"`
				ResourceType string     `json:"resource_type"`
				StartedAt    time.Time  `json:"started_at"`
				CompletedAt  *time.Time `json:"completed_at"`
			} `json:"actions"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		for _, a := range page.Actions {
			actions++
			if a.ResourceType != "droplet" {
				continue
			}
			droplets++
			if a.Type == "" || a.StartedAt.IsZero() {
				return errors.New("a Droplet action has no type or start time")
			}
			at := a.StartedAt
			if a.CompletedAt != nil && !a.CompletedAt.IsZero() {
				at = *a.CompletedAt
			}
			if at.After(last) {
				last = at
			}
		}
		return nil
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("read the account's actions: %w", err)
	}
	if actions > 0 && droplets == 0 {
		return time.Time{}, fmt.Errorf("the account has %d actions and none on a Droplet, so Loomplane can't tell when the last run ended", actions)
	}
	return last, nil
}

// AccountUUID implements Spend. A token belongs to one team; its UUID is
// the account, as Loomplane's server connections read it.
func (d *DigitalOceanSpend) AccountUUID(ctx context.Context) (string, error) {
	var out struct {
		Account struct {
			UUID string `json:"uuid"`
			Team *struct {
				UUID string `json:"uuid"`
			} `json:"team"`
		} `json:"account"`
	}
	if err := d.api.do(ctx, http.MethodGet, "/v2/account", nil, nil, &out); err != nil {
		return "", err
	}
	if t := out.Account.Team; t != nil && t.UUID != "" {
		return t.UUID, nil
	}
	return out.Account.UUID, nil
}
