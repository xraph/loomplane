package page

import (
	"errors"
	"testing"
	"time"
)

func TestCursor_readsBackWhatItWrote(t *testing.T) {
	// Postgres keeps microseconds; a cursor that rounds them away would
	// skip or repeat the rows that differ only there.
	at := time.Date(2026, 10, 14, 9, 30, 15, 123456000, time.UTC)
	for _, id := range []string{"plan_01m4xkdh1fe0xvw8755v39j8a9", "dep_page_02", "a|b.c"} {
		c := Cursor{At: at, ID: id}
		back, err := Parse(c.String())
		if err != nil {
			t.Fatalf("parse %q: %v", c.String(), err)
		}
		if !back.At.Equal(at) || back.ID != id {
			t.Fatalf("%+v read back as %+v", c, back)
		}
	}
}

func TestParse_refusesWhatNoCursorWrote(t *testing.T) {
	for _, raw := range []string{"not-a-cursor", "!!!", "2026-10-14T09:30:15Z"} {
		if _, err := Parse(raw); !errors.Is(err, ErrBadCursor) {
			t.Errorf("Parse(%q) = %v, want ErrBadCursor", raw, err)
		}
	}
	if c, err := Parse(""); err != nil || !c.IsZero() {
		t.Fatalf("an empty cursor is the first page, got %+v, %v", c, err)
	}
}

func TestLimit(t *testing.T) {
	for raw, want := range map[string]int{"": Default, "1": 1, "30": 30, "100": Max, "5000": Max} {
		if got, err := Limit(raw); err != nil || got != want {
			t.Errorf("Limit(%q) = %d, %v, want %d", raw, got, err, want)
		}
	}
	for _, raw := range []string{"0", "-3", "lots", "2.5"} {
		if _, err := Limit(raw); !errors.Is(err, ErrBadLimit) {
			t.Errorf("Limit(%q) = %v, want ErrBadLimit", raw, err)
		}
	}
}

func TestTrim_saysWhereTheNextPageStarts(t *testing.T) {
	type row struct {
		at time.Time
		id string
	}
	at := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	rows := []row{{at, "c"}, {at, "b"}, {at, "a"}}
	key := func(r row) Cursor { return Cursor{At: r.at, ID: r.id} }

	// One more than the limit came back: there's a next page, after "b".
	got, next := Trim(rows, 2, key)
	if len(got) != 2 || next.ID != "b" {
		t.Fatalf("got %v, next %+v", got, next)
	}
	// No more than the limit: this is the last page.
	if got, next = Trim(rows, 3, key); len(got) != 3 || !next.IsZero() {
		t.Fatalf("got %v, next %+v", got, next)
	}
}
