// Package metrics describes what a cloud reports about the services
// Loomplane runs on it, and about what the account costs: requests and
// latency over a window, and spend this month. Each cloud's package reads
// its own monitoring and billing into these; none of it passes through
// Loomplane, which never sees application traffic.
package metrics

import (
	"errors"
	"fmt"
	"time"
)

// Range is how far back a chart looks.
type Range string

// Ranges.
const (
	Day     Range = "24h"
	Week    Range = "7d"
	Month   Range = "30d"
	Quarter Range = "90d"
)

// ErrRange is a range that isn't one of the four.
var ErrRange = errors.New("the range is 24h, 7d, 30d or 90d")

// ParseRange reads a range, a week when it's empty.
func ParseRange(s string) (Range, error) {
	switch r := Range(s); r {
	case "":
		return Week, nil
	case Day, Week, Month, Quarter:
		return r, nil
	}
	return "", fmt.Errorf("%w, not %q", ErrRange, s)
}

// grids are each range's bucket width and count: enough points to draw,
// few enough that a cloud answers in one page.
var grids = map[Range]struct {
	step    time.Duration
	buckets int
}{
	Day:     {30 * time.Minute, 48},
	Week:    {3 * time.Hour, 56},
	Month:   {12 * time.Hour, 60},
	Quarter: {24 * time.Hour, 90},
}

// Window is a stretch of time cut into equal buckets, in UTC.
type Window struct {
	Start time.Time
	// End is outside the window: the last bucket ends there.
	End  time.Time
	Step time.Duration
}

// Window is the range ending with the bucket now is in. Its edges fall on
// the step, so two readers asking within one bucket ask the same thing.
func (r Range) Window(now time.Time) Window {
	g := grids[r]
	end := now.UTC().Truncate(g.step).Add(g.step)
	return Window{Start: end.Add(-time.Duration(g.buckets) * g.step), End: end, Step: g.step}
}

// Buckets is how many buckets the window has.
func (w Window) Buckets() int { return int(w.End.Sub(w.Start) / w.Step) }

// Previous is the window of the same length just before this one.
func (w Window) Previous() Window {
	return Window{Start: w.Start.Add(-w.End.Sub(w.Start)), End: w.Start, Step: w.Step}
}

// Point is a value at a time: a bucket's start and what fell in it.
type Point struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

// Grid sums points into w's buckets, one point for each and zero where
// nothing fell. Points outside the window are left out.
func Grid(w Window, points []Point) []Point {
	out := make([]Point, w.Buckets())
	for i := range out {
		out[i].T = w.Start.Add(time.Duration(i) * w.Step)
	}
	for _, p := range points {
		if p.T.Before(w.Start) || !p.T.Before(w.End) {
			continue
		}
		out[p.T.Sub(w.Start)/w.Step].V += p.V
	}
	return out
}

// Sum adds series together on w's grid. It's for counts: a percentile
// summed across series means nothing.
func Sum(w Window, series ...[]Point) []Point {
	var all []Point
	for _, s := range series {
		all = append(all, s...)
	}
	return Grid(w, all)
}

// Total adds up a series.
func Total(points []Point) float64 {
	var t float64
	for _, p := range points {
		t += p.V
	}
	return t
}

// Statistics a latency can be.
const (
	// P95 is the 95th percentile over the window.
	P95 = "p95"
	// Average is the mean over the window, for a cloud that reports no
	// percentile.
	Average = "average"
)

// Measurement is one service's traffic over a window, as its cloud
// reports it.
type Measurement struct {
	// Requests are request counts, in the cloud's own buckets.
	Requests []Point
	// Latency is in milliseconds over the whole window, 0 when nothing was
	// served.
	Latency float64
	// Statistic is what Latency is, P95 or Average, or empty when the
	// cloud reports none.
	Statistic string
}

// Spend is what an account has cost this calendar month so far.
type Spend struct {
	Currency string
	Total    float64
	// Days are what each day cost, at UTC midnight. Days with nothing are
	// left out.
	Days []Point
}

// CalendarMonth is the calendar month t falls in, in UTC: its first instant and
// the next month's.
func CalendarMonth(t time.Time) (start, end time.Time) {
	t = t.UTC()
	start = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}

// UnavailableError is a cloud that can't report something, for a reason
// someone can act on, such as a permission to grant.
type UnavailableError struct{ Reason string }

func (u *UnavailableError) Error() string { return u.Reason }

// Unavailablef returns an *UnavailableError.
func Unavailablef(format string, args ...any) error {
	return &UnavailableError{Reason: fmt.Sprintf(format, args...)}
}

// IsUnavailable reports whether err is, or wraps, an *UnavailableError, and
// its reason.
func IsUnavailable(err error) (string, bool) {
	var u *UnavailableError
	if errors.As(err, &u) {
		return u.Reason, true
	}
	return "", false
}
