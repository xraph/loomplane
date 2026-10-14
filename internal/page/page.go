// Package page pages through newest-first lists by keyset: a page ends at
// its last row's time and ID, and the next one starts strictly older than
// that. Unlike an offset, a row written while someone pages can't shift
// what they see onto a row they've already read.
package page

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	// Default is how many rows a list returns when the caller doesn't say.
	Default = 30
	// Max is the most a caller can ask for at once.
	Max = 100
)

var (
	// ErrBadCursor is a cursor this package didn't write.
	ErrBadCursor = errors.New("the cursor isn't one a list returned")
	// ErrBadLimit is a limit that isn't a whole number of at least one.
	ErrBadLimit = errors.New("the limit must be a whole number of at least 1")
)

// Cursor is where a page stopped: its last row's time and ID. The zero
// Cursor is the first page.
type Cursor struct {
	At time.Time
	ID string
}

// IsZero reports whether c is the first page.
func (c Cursor) IsZero() bool { return c.ID == "" }

// String is c as the API hands it out, opaque to the caller. The zero
// Cursor is "", so a response's next is empty on the last page.
func (c Cursor) String() string {
	if c.IsZero() {
		return ""
	}
	// Microseconds are what Postgres keeps, and the time can't hold the
	// separator, so the first one always ends it whatever the ID holds.
	raw := strconv.FormatInt(c.At.UnixMicro(), 10) + "|" + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// Parse reads a cursor String wrote. "" is the first page; anything else
// it can't read is ErrBadCursor.
func Parse(raw string) (Cursor, error) {
	if raw == "" {
		return Cursor{}, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, ErrBadCursor
	}
	micros, id, ok := strings.Cut(string(b), "|")
	if !ok || id == "" {
		return Cursor{}, ErrBadCursor
	}
	n, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return Cursor{}, ErrBadCursor
	}
	return Cursor{At: time.UnixMicro(n).UTC(), ID: id}, nil
}

// Limit reads a ?limit= value: Default when it's empty, at most Max.
func Limit(raw string) (int, error) {
	if raw == "" {
		return Default, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, ErrBadLimit
	}
	return min(n, Max), nil
}

// Trim takes the limit+1 rows a store read and returns the page and where
// the next one starts. The extra row only says there is a next page; it
// isn't shown, and the zero Cursor means this was the last.
func Trim[T any](rows []T, limit int, key func(T) Cursor) ([]T, Cursor) {
	if len(rows) <= limit {
		return rows, Cursor{}
	}
	rows = rows[:limit]
	return rows, key(rows[limit-1])
}
