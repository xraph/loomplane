package usage

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
)

// Amount is what one currency adds up to across accounts.
type Amount struct {
	Currency string  `json:"currency"`
	Total    float64 `json:"total"`
	// Days are each day of the month, zero where nothing was charged.
	Days []metrics.Point `json:"days"`
	// Undated is the part of Total from accounts whose bill has no daily
	// breakdown, such as DigitalOcean's balance. It isn't in Days.
	Undated float64 `json:"undated"`
}

// AccountSpend is what one connected account has cost this month.
type AccountSpend struct {
	ConnectionID string          `json:"connection_id"`
	Cloud        string          `json:"cloud"`
	Name         string          `json:"name"`
	Currency     string          `json:"currency"`
	Total        float64         `json:"total"`
	Days         []metrics.Point `json:"days"`
}

// SpendReport is the body of GET /api/v1/usage/spend.
type SpendReport struct {
	// Month is the first instant of the calendar month, in UTC.
	Month time.Time `json:"month"`
	// Totals are by currency: accounts in different currencies aren't
	// added together.
	Totals   []Amount       `json:"totals"`
	Accounts []AccountSpend `json:"accounts"`
	Sources  []Source       `json:"sources"`
}

// unbilled are clouds that aren't accounts with a bill: what runs on them
// is paid for wherever they are.
var unbilled = map[string]bool{
	connections.Registry:   true,
	connections.Server:     true,
	connections.Kubernetes: true,
}

// noSpend says why a cloud's spend can't be read.
var noSpend = map[string]string{
	connections.Vercel: "Vercel doesn't report what a team costs through its API.",
	connections.Render: "Render doesn't report what a workspace costs through its API.",
}

// Spend reads what each of the organization's accounts has cost this
// month. It needs cloud_account:read, like the connections themselves.
func (s *Service) Spend(ctx context.Context, p security.Principal) (*SpendReport, error) {
	if err := s.Authz.Require(ctx, p, security.CloudAccountRead, ""); err != nil {
		return nil, err
	}
	all, err := s.Connections.All(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	start, end := metrics.CalendarMonth(now)
	month := metrics.Window{Start: start, End: end, Step: 24 * time.Hour}

	var billed []*connections.Connection
	for _, c := range all {
		if !unbilled[c.Cloud] {
			billed = append(billed, c)
		}
	}
	spends := make([]*metrics.Spend, len(billed))
	sources := make([]Source, len(billed))
	g := errgroup.Group{}
	g.SetLimit(fanOut)
	for i, c := range billed {
		g.Go(func() error {
			spend, rerr := s.readSpend(ctx, c, now, start)
			if errors.Is(rerr, connections.ErrNotFound) {
				return nil // removed while we read
			}
			status, reason := classify(rerr)
			sources[i] = Source{ConnectionID: c.ID, Cloud: c.Cloud, Name: c.Name, Status: status, Reason: reason}
			spends[i] = spend
			return nil
		})
	}
	_ = g.Wait() //nolint:errcheck // every read records its own outcome

	rep := &SpendReport{Month: start, Totals: []Amount{}, Accounts: []AccountSpend{}, Sources: []Source{}}
	type sum struct {
		total, undated float64
		days           [][]metrics.Point
	}
	byCurrency := map[string]*sum{}
	for i, c := range billed {
		if sources[i].ConnectionID == "" {
			continue
		}
		rep.Sources = append(rep.Sources, sources[i])
		sp := spends[i]
		if sp == nil {
			continue
		}
		rep.Accounts = append(rep.Accounts, AccountSpend{
			ConnectionID: c.ID, Cloud: c.Cloud, Name: c.Name, Currency: sp.Currency, Total: sp.Total, Days: nonNil(sp.Days),
		})
		if sp.Currency == "" {
			continue
		}
		cur := byCurrency[sp.Currency]
		if cur == nil {
			cur = &sum{}
			byCurrency[sp.Currency] = cur
		}
		cur.total += sp.Total
		if len(sp.Days) == 0 {
			cur.undated += sp.Total
		} else {
			cur.days = append(cur.days, sp.Days)
		}
	}
	for currency, cur := range byCurrency {
		rep.Totals = append(rep.Totals, Amount{Currency: currency, Total: cur.total, Days: metrics.Sum(month, cur.days...), Undated: cur.undated})
	}
	slices.SortFunc(rep.Totals, func(a, b Amount) int { return cmp.Compare(a.Currency, b.Currency) })
	slices.SortStableFunc(rep.Accounts, func(a, b AccountSpend) int {
		return cmp.Or(cmp.Compare(b.Total, a.Total), cmp.Compare(a.Name, b.Name))
	})
	return rep, nil
}

// readSpend reads one account's month, from the cache when it can. The
// key carries the credential's generation, so a replaced credential reads
// afresh.
func (s *Service) readSpend(ctx context.Context, c *connections.Connection, now, month time.Time) (*metrics.Spend, error) {
	reader := s.Spenders[c.Cloud]
	if reader == nil {
		reason := noSpend[c.Cloud]
		if reason == "" {
			reason = s.Title(c.Cloud) + " doesn't report spend through an API Loomplane reads."
		}
		return nil, &unsupportedError{reason: reason}
	}
	key := fmt.Sprintf("spend|%s|%s|%d|%s", c.OrgID, c.ID, c.Generation, month.Format(time.DateOnly))
	return cached(s.cache, key, spendTTL, func() (*metrics.Spend, error) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
		defer cancel()
		live, cred, err := s.credential(rctx, c)
		if err != nil {
			return nil, err
		}
		sp, err := reader.Spend(rctx, live, cred, now)
		s.Telemetry.UsageRead("spend", c.Cloud, outcome(err))
		if err != nil {
			if _, ok := metrics.IsUnavailable(err); !ok {
				s.Warn("read spend for "+c.Name+" ("+strconv.Quote(c.ID)+")", err)
			}
			return nil, err
		}
		return sp, nil
	})
}

func nonNil(p []metrics.Point) []metrics.Point {
	if p == nil {
		return []metrics.Point{}
	}
	return p
}
