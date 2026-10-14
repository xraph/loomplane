// Package buildlogs describes output from one provider build operation.
package buildlogs

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Line struct {
	ID, Stream, Message, Source, Level string
	Time                               time.Time
}

type Emit func(Line) error

// Read emits a bounded page or live range. The caller commits Cursor only after
// all emitted records are retained. Replaying a range must use the same IDs.
type Read func(context.Context, string, bool, Emit) (cursor string, more bool, err error)

type Error struct {
	Status     int
	RetryAfter time.Duration
	Message    string
}

func (e *Error) Error() string { return fmt.Sprintf("build logs: %d: %s", e.Status, e.Message) }

func Status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func RetryAfter(err error) time.Duration {
	var e *Error
	if errors.As(err, &e) && e.RetryAfter > 0 {
		return min(e.RetryAfter, time.Minute)
	}
	return 5 * time.Second
}
