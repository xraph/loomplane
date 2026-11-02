package metrics

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestParseRangeDefaultsToAWeekAndRefusesTheUnknown(t *testing.T) {
	for in, want := range map[string]Range{"": Week, "24h": Day, "7d": Week, "30d": Month, "90d": Quarter} {
		got, err := ParseRange(in)
		if err != nil || got != want {
			t.Errorf("ParseRange(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseRange("1y"); !errors.Is(err, ErrRange) {
		t.Fatalf("an unknown range was accepted: %v", err)
	}
}

func TestWindowIsAlignedToItsStepAndEndsAfterNow(t *testing.T) {
	now := time.Date(2026, 10, 24, 14, 47, 12, 0, time.FixedZone("WAT", 3600))
	cases := map[Range]struct {
		step    time.Duration
		buckets int
	}{
		Day:     {30 * time.Minute, 48},
		Week:    {3 * time.Hour, 56},
		Month:   {12 * time.Hour, 60},
		Quarter: {24 * time.Hour, 90},
	}
	for r, want := range cases {
		w := r.Window(now)
		if w.Step != want.step || w.Buckets() != want.buckets {
			t.Errorf("%s: step %v, %d buckets; want %v, %d", r, w.Step, w.Buckets(), want.step, want.buckets)
		}
		if w.End.Location() != time.UTC || !w.End.Equal(w.End.Truncate(w.Step)) {
			t.Errorf("%s: end %v isn't a UTC bucket edge", r, w.End)
		}
		if !w.Start.Before(now) || !w.End.After(now) || w.End.Sub(now) > w.Step {
			t.Errorf("%s: %v to %v doesn't end in the bucket now is in", r, w.Start, w.End)
		}
		if p := w.Previous(); !p.End.Equal(w.Start) || p.Step != w.Step || p.Buckets() != w.Buckets() {
			t.Errorf("%s: previous window %v to %v doesn't sit right before", r, p.Start, p.End)
		}
	}
}

func TestGridSumsPointsIntoBucketsAndFillsTheGaps(t *testing.T) {
	start := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	w := Window{Start: start, End: start.Add(4 * time.Hour), Step: time.Hour}
	got := Grid(w, []Point{
		{T: start.Add(10 * time.Minute), V: 2},
		{T: start.Add(50 * time.Minute), V: 3},
		{T: start.Add(3 * time.Hour), V: 7},
		{T: start.Add(-time.Minute), V: 100}, // before the window
		{T: w.End, V: 100},                   // the end is outside
	})
	want := []float64{5, 0, 0, 7}
	if len(got) != len(want) {
		t.Fatalf("got %d buckets, want %d: %+v", len(got), len(want), got)
	}
	for i, p := range got {
		if !p.T.Equal(start.Add(time.Duration(i)*time.Hour)) || p.V != want[i] {
			t.Errorf("bucket %d = %+v, want %v at %v", i, p, want[i], start.Add(time.Duration(i)*time.Hour))
		}
	}
	if Total(got) != 12 {
		t.Errorf("total %v, want 12", Total(got))
	}
}

func TestSumAddsSeriesOnTheWindowsGrid(t *testing.T) {
	start := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	w := Window{Start: start, End: start.Add(2 * time.Hour), Step: time.Hour}
	got := Sum(w, []Point{{T: start, V: 1}}, []Point{{T: start.Add(90 * time.Minute), V: 4}}, nil)
	if len(got) != 2 || got[0].V != 1 || got[1].V != 4 {
		t.Fatalf("sum = %+v", got)
	}
}

func TestMonthIsTheCalendarMonthInUTC(t *testing.T) {
	start, end := CalendarMonth(time.Date(2026, 12, 31, 23, 30, 0, 0, time.FixedZone("PST", -8*3600)))
	if !start.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) || !end.Equal(time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("month = %v to %v", start, end)
	}
}

func TestUnavailableSurvivesWrapping(t *testing.T) {
	err := fmt.Errorf("read spend: %w", Unavailablef("give the key %s", "ce:GetCostAndUsage"))
	reason, ok := IsUnavailable(err)
	if !ok || reason != "give the key ce:GetCostAndUsage" {
		t.Fatalf("IsUnavailable = %q, %v", reason, ok)
	}
	if _, ok := IsUnavailable(errors.New("timeout")); ok {
		t.Fatal("a plain error read as unavailable")
	}
}
