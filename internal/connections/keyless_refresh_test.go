package connections

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// keylessRefreshRig is the keyless rig with a store that can lock, so the
// scheduled job has something to run against.
type keylessRefreshRig struct {
	*keylessRig
	locked *lockingStore
	lapsed []*Connection
}

func (r *reservationStore) SweepSignIns(context.Context, time.Time) (int64, error) { return 0, nil }

func newKeylessRefreshRig(t *testing.T) *keylessRefreshRig {
	t.Helper()
	base := newKeylessRig(t)
	r := &keylessRefreshRig{keylessRig: base}
	old := base.s
	locked := &lockingStore{memStore: base.store}
	r.locked = locked
	r.s = NewService(locked, old.secrets, old.checkers, old.authz, old.audit, func(msg string, err error) {
		r.warnings = append(r.warnings, msg+": "+err.Error())
	})
	r.s.now = old.now
	r.s.Apps, r.s.Keyless, r.s.KeylessClouds = old.Apps, old.Keyless, old.KeylessClouds
	r.s.Lapsed = func(_ context.Context, c *Connection) { r.lapsed = append(r.lapsed, c) }
	return r
}

func TestKeylessCheckMarksARefusedTrustOnce(t *testing.T) {
	r := newKeylessRefreshRig(t)
	c := r.connect(t)
	r.refuse = true
	r.clock = r.clock.Add(48 * time.Hour)
	for run := 1; run <= 2; run++ {
		report, err := r.s.RefreshDue(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if want := 1 - (run - 1); report.Lapsed != want || report.Failed != 0 {
			t.Fatalf("run %d: %+v", run, report)
		}
	}
	got := r.store.rows[c.ID]
	if got.Status != StatusFailed || !strings.Contains(got.Message, "AssumeRoleWithWebIdentity") || got.Generation != c.Generation {
		t.Fatalf("row: %+v", got)
	}
	if len(r.lapsed) != 1 || r.lapsed[0].ID != c.ID {
		t.Fatalf("lapsed: %v", r.lapsed)
	}
	if r.secrets.reveals != 0 {
		t.Fatalf("read Vault %d times", r.secrets.reveals)
	}
}

func TestKeylessCheckStampsAHealthyConnection(t *testing.T) {
	r := newKeylessRefreshRig(t)
	c := r.connect(t)
	r.clock = r.clock.Add(48 * time.Hour)
	report, err := r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := r.store.rows[c.ID]
	if report.Probed != 1 || report.Lapsed != 0 || got.Status != StatusReady || got.Generation != c.Generation ||
		got.CheckedAt == nil || !got.CheckedAt.Equal(r.clock) || len(r.lapsed) != 0 {
		t.Fatalf("report %+v, row %+v, lapsed %v", report, got, r.lapsed)
	}
	if !strings.Contains(string(r.checked), `"ticket":"`) {
		t.Fatalf("checked without a handout")
	}
}

func TestKeylessCheckSkipsARecentlyChecked(t *testing.T) {
	r := newKeylessRefreshRig(t)
	c := r.connect(t)
	row := r.store.rows[c.ID]
	recent := r.clock.Add(-time.Hour)
	row.CheckedAt = &recent
	r.store.rows[c.ID] = row
	r.refuse, r.checked = true, nil
	report, err := r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report != (RefreshReport{}) || r.store.rows[c.ID].Status != StatusReady || len(r.checked) != 0 {
		t.Fatalf("report %+v, row %+v", report, r.store.rows[c.ID])
	}
}

func TestKeylessCheckThatCantReachTheCloudChangesNothing(t *testing.T) {
	r := newKeylessRefreshRig(t)
	c := r.connect(t)
	r.issuer.usable, r.issuer.reason = false, "the issuer is down"
	r.clock = r.clock.Add(48 * time.Hour)
	report, err := r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := r.store.rows[c.ID]
	if report.Failed != 1 || got.Status != StatusReady || got.CheckedAt != nil && got.CheckedAt.After(c.UpdatedAt) || len(r.lapsed) != 0 {
		t.Fatalf("report %+v, row %+v", report, got)
	}
}

func TestRefreshAllRotatesKeysAndSweepsReservations(t *testing.T) {
	r := newKeylessRefreshRig(t)
	r.held.held["old"] = &Reservation{ID: "old", ExpiresAt: r.clock.Add(-time.Minute)}
	r.held.held["live"] = &Reservation{ID: "live", ExpiresAt: r.clock.Add(time.Hour)}
	rotations := 0
	r.s.Rotate = func(context.Context) error { rotations++; return errors.New("vault is down") }
	if err := r.s.RefreshAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if rotations != 1 || r.held.held["old"] != nil || r.held.held["live"] == nil {
		t.Fatalf("%d rotations, reservations %v", rotations, r.held.held)
	}
	if !strings.Contains(strings.Join(r.warnings, "\n"), "rotate the issuer's signing keys: vault is down") {
		t.Fatalf("warnings: %v", r.warnings)
	}
	r.s.Rotate = nil
	if err := r.s.RefreshAll(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestKeylessCheckThatFailsAtTheCloudChangesNothing(t *testing.T) {
	for name, down := range map[string]error{
		"plain":       errors.New("connection reset"),
		"unavailable": ErrUnavailable,
	} {
		t.Run(name, func(t *testing.T) {
			r := newKeylessRefreshRig(t)
			c := r.connect(t)
			before := r.store.rows[c.ID]
			r.down = down
			r.clock = r.clock.Add(48 * time.Hour)
			report, err := r.s.RefreshDue(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(r.checked) == 0 {
				t.Fatal("the handout never reached the checker")
			}
			got := r.store.rows[c.ID]
			if report.Failed != 1 || report.Lapsed != 0 || len(r.lapsed) != 0 || got.Status != StatusReady ||
				got.Generation != before.Generation || !got.UpdatedAt.Equal(before.UpdatedAt) || !sameTime(got.CheckedAt, before.CheckedAt) {
				t.Fatalf("report %+v, row %+v, lapsed %v", report, got, r.lapsed)
			}
		})
	}
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func TestKeylessCheckOfAStaleCopyOfAFailedRowTellsNothing(t *testing.T) {
	r := newKeylessRefreshRig(t)
	c := r.connect(t)
	stale := r.store.rows[c.ID]
	row := r.store.rows[c.ID]
	row.Status, row.Message = StatusFailed, "refused earlier"
	r.store.rows[c.ID] = row
	r.refuse = true
	r.clock = r.clock.Add(48 * time.Hour)
	var report RefreshReport
	r.s.probeKeyless(t.Context(), &stale, &report)
	got := r.store.rows[c.ID]
	if report != (RefreshReport{}) || len(r.lapsed) != 0 || got.Message != "refused earlier" ||
		!got.UpdatedAt.Equal(row.UpdatedAt) || !sameTime(got.CheckedAt, row.CheckedAt) {
		t.Fatalf("report %+v, row %+v, lapsed %v", report, got, r.lapsed)
	}
}

func TestKeylessRefusalIsToldEvenWhenTheLockFails(t *testing.T) {
	r := newKeylessRefreshRig(t)
	c := r.connect(t)
	r.s.grants = failingLock{r.locked}
	r.refuse = true
	r.clock = r.clock.Add(48 * time.Hour)
	report, err := r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if r.store.rows[c.ID].Status != StatusFailed || len(r.lapsed) != 1 || report.Failed != 1 || report.Lapsed != 0 {
		t.Fatalf("report %+v, row %+v, lapsed %v", report, r.store.rows[c.ID], r.lapsed)
	}
}
