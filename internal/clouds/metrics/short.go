package metrics

import (
	"errors"
	"time"
)

// ErrShortWindow is a release window that isn't one minute to one day.
var ErrShortWindow = errors.New("a release window is one minute to one day, on the minute")

// Release windows run from a minute to a day, and stay at one-minute
// buckets until the span passes three hours.
const (
	shortLeast    = time.Minute
	shortMost     = 24 * time.Hour
	shortFineSpan = 3 * time.Hour
)

// ShortWindow is the window from start to end for judging a release, both
// cut down to the minute in UTC. Its buckets are a minute wide up to three
// hours and five minutes wide beyond, and it spans at most a day.
func ShortWindow(start, end time.Time) (Window, error) {
	start, end = start.UTC().Truncate(time.Minute), end.UTC().Truncate(time.Minute)
	span := end.Sub(start)
	if span < shortLeast || span > shortMost {
		return Window{}, ErrShortWindow
	}
	step := time.Minute
	if span > shortFineSpan {
		step = 5 * time.Minute
	}
	return Window{Start: start, End: end, Step: step}, nil
}
