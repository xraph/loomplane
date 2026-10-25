package metrics

import (
	"errors"
	"testing"
	"time"
)

func TestShortWindowFallsOnTheMinute(t *testing.T) {
	start := time.Date(2026, 10, 24, 10, 3, 40, 0, time.UTC)
	end := time.Date(2026, 10, 24, 10, 13, 20, 0, time.UTC)

	w, err := ShortWindow(start, end)
	if err != nil {
		t.Fatal(err)
	}
	if !w.Start.Equal(time.Date(2026, 10, 24, 10, 3, 0, 0, time.UTC)) || !w.End.Equal(time.Date(2026, 10, 24, 10, 13, 0, 0, time.UTC)) {
		t.Errorf("window = %v to %v", w.Start, w.End)
	}
	if w.Step != time.Minute || w.Buckets() != 10 {
		t.Errorf("step %v, buckets %d", w.Step, w.Buckets())
	}
}

func TestShortWindowStepGrowsPastThreeHours(t *testing.T) {
	start := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	for span, want := range map[time.Duration]time.Duration{
		3 * time.Hour:  time.Minute,
		4 * time.Hour:  5 * time.Minute,
		24 * time.Hour: 5 * time.Minute,
	} {
		w, err := ShortWindow(start, start.Add(span))
		if err != nil || w.Step != want {
			t.Errorf("span %v: step %v, err %v; want %v", span, w.Step, err, want)
		}
	}
}

func TestShortWindowRefuses(t *testing.T) {
	start := time.Date(2026, 10, 24, 10, 0, 30, 0, time.UTC)
	for name, end := range map[string]time.Time{
		"under a minute after truncation": start.Add(20 * time.Second),
		"empty":                           start,
		"over a day":                      start.Add(24*time.Hour + time.Minute),
		"end before start":                start.Add(-time.Hour),
	} {
		if _, err := ShortWindow(start, end); !errors.Is(err, ErrShortWindow) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
