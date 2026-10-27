//go:build integration

package integration

import (
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
)

func TestReservationIsTakenOnce(t *testing.T) {
	store, _ := signInStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	in := &connections.Reservation{ID: "conn_1", OrgID: "org_1", UserID: "usr_1", Cloud: "aws", ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now}
	if err := store.PutReservation(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := store.PutReservation(ctx, in); err == nil {
		t.Fatal("an ID was reserved twice")
	}
	// Another organization's try finds nothing and leaves it.
	if _, err := store.TakeReservation(ctx, "org_2", "conn_1"); !errors.Is(err, connections.ErrNotFound) {
		t.Fatalf("other org: %v", err)
	}
	got, err := store.TakeReservation(ctx, "org_1", "conn_1")
	if err != nil || *got != *in {
		t.Fatalf("take: %+v %v", got, err)
	}
	if got.ExpiresAt.Location() != time.UTC || got.CreatedAt.Location() != time.UTC {
		t.Fatalf("times aren't UTC: %v %v", got.ExpiresAt, got.CreatedAt)
	}
	if _, err := store.TakeReservation(ctx, "org_1", "conn_1"); !errors.Is(err, connections.ErrNotFound) {
		t.Fatalf("second take: %v", err)
	}
	if _, err := store.TakeReservation(ctx, "org_1", "conn_unknown"); !errors.Is(err, connections.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestSweepReservationsDropsOnlyExpired(t *testing.T) {
	store, _ := signInStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for id, at := range map[string]time.Time{"old_1": now.Add(-time.Hour), "old_2": now.Add(-time.Minute), "live": now.Add(time.Hour)} {
		if err := store.PutReservation(ctx, &connections.Reservation{ID: id, OrgID: "org_1", UserID: "usr_1", Cloud: "gcp", ExpiresAt: at, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := store.SweepReservations(ctx, now)
	if err != nil || n != 2 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	for _, id := range []string{"old_1", "old_2"} {
		if _, err = store.TakeReservation(ctx, "org_1", id); !errors.Is(err, connections.ErrNotFound) {
			t.Fatalf("%s should be gone: %v", id, err)
		}
	}
	if _, err = store.TakeReservation(ctx, "org_1", "live"); err != nil {
		t.Fatalf("live reservation was swept: %v", err)
	}
}

// Two creates of one keyless connection at once: exactly one takes its
// reservation.
func TestReservationIsTakenOnceUnderConcurrentCreates(t *testing.T) {
	store, _ := signInStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	in := &connections.Reservation{ID: "conn_race", OrgID: "org_1", UserID: "usr_1", Cloud: "gcp", ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now}
	if err := store.PutReservation(ctx, in); err != nil {
		t.Fatal(err)
	}
	const tries = 8
	results := make(chan error, tries)
	for range tries {
		go func() {
			_, err := store.TakeReservation(ctx, "org_1", "conn_race")
			results <- err
		}()
	}
	took := 0
	for range tries {
		switch err := <-results; {
		case err == nil:
			took++
		case !errors.Is(err, connections.ErrNotFound):
			t.Errorf("take: %v", err)
		}
	}
	if took != 1 {
		t.Fatalf("%d creates took the reservation, want 1", took)
	}
}
