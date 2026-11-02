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
	refusal, _ := r.s.probeKeyless(t.Context(), &stale, &report)
	if refusal == nil {
		t.Fatal("the refusal wasn't returned")
	}
	r.s.markKeyless(t.Context(), *refusal, &report)
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

// connectAs connects cloud keylessly as name, assuming role.
func (r *keylessRefreshRig) connectAs(t *testing.T, cloud, name, role string) *Connection {
	t.Helper()
	setup, err := r.s.PrepareKeyless(t.Context(), starter, cloud)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	c, err := r.s.CreateKeyless(t.Context(), starter, KeylessInput{ConnectionID: setup.ConnectionID, Name: name, Settings: map[string]string{"role_arn": role, "region": "us-east-1"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return c
}

// unchanged fails t if c's row isn't as it was in before.
func (r *keylessRefreshRig) unchanged(t *testing.T, before Connection) {
	t.Helper()
	got := r.store.rows[before.ID]
	if got.Status != before.Status || got.Message != before.Message || got.Generation != before.Generation ||
		!got.UpdatedAt.Equal(before.UpdatedAt) || !sameTime(got.CheckedAt, before.CheckedAt) {
		t.Fatalf("row changed: %+v, was %+v", got, before)
	}
}

// When Loomplane's own issuer isn't serving its keys at its public URL, no
// cloud can check a token, so a refusal says nothing about the trust. The
// run changes nothing, tells nobody, and warns once, having asked once.
func TestKeylessCheckChangesNothingWhileTheIssuerIsntServing(t *testing.T) {
	r := newKeylessRefreshRig(t)
	a := r.connect(t)
	g := r.connectAs(t, GCP, "Google", roleArn)
	before := []Connection{r.store.rows[a.ID], r.store.rows[g.ID]}
	asked := 0
	r.s.IssuerServing = func(context.Context) error { asked++; return errors.New("the JWKS answered 502") }
	r.refuse = true
	r.clock = r.clock.Add(48 * time.Hour)
	report, err := r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.Failed != 2 || report.Lapsed != 0 || len(r.lapsed) != 0 || asked != 1 {
		t.Fatalf("report %+v, lapsed %v, asked the issuer %d times", report, r.lapsed, asked)
	}
	for _, b := range before {
		r.unchanged(t, b)
	}
	warned := 0
	for _, w := range r.warnings {
		if strings.Contains(w, "the JWKS answered 502") {
			warned++
		}
	}
	if warned != 1 {
		t.Fatalf("warned %d times: %v", warned, r.warnings)
	}
}

// The issuer is asked only when there's something to mark, and a refusal
// it vouches for is marked as before.
func TestKeylessCheckAsksTheIssuerOnlyBeforeMarking(t *testing.T) {
	r := newKeylessRefreshRig(t)
	c := r.connect(t)
	asked := 0
	r.s.IssuerServing = func(context.Context) error { asked++; return nil }
	r.clock = r.clock.Add(48 * time.Hour)
	if report, err := r.s.RefreshDue(t.Context()); err != nil || report.Probed != 1 || asked != 0 {
		t.Fatalf("healthy run: %+v %v, asked %d times", report, err, asked)
	}
	r.refuse = true
	r.clock = r.clock.Add(48 * time.Hour)
	report, err := r.s.RefreshDue(t.Context())
	if err != nil || report.Lapsed != 1 || asked != 1 || r.store.rows[c.ID].Status != StatusFailed || len(r.lapsed) != 1 {
		t.Fatalf("refused run: %+v %v, asked %d times, row %+v", report, err, asked, r.store.rows[c.ID])
	}
}

// Every keyless connection to one cloud refused in one run looks like
// Loomplane's fault, not each trust's, so with two or more of them the run
// changes nothing and warns. One refused among healthy ones is marked.
func TestKeylessCheckTreatsACloudRefusingEveryConnectionAsLoomplanes(t *testing.T) {
	r := newKeylessRefreshRig(t)
	first := r.connectAs(t, AWS, "First", roleArn)
	second := r.connectAs(t, AWS, "Second", otherRoleArn)
	before := []Connection{r.store.rows[first.ID], r.store.rows[second.ID]}
	r.refuse = true
	r.clock = r.clock.Add(48 * time.Hour)
	report, err := r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.Failed != 2 || report.Lapsed != 0 || len(r.lapsed) != 0 {
		t.Fatalf("report %+v, lapsed %v", report, r.lapsed)
	}
	for _, b := range before {
		r.unchanged(t, b)
	}
	if !strings.Contains(strings.Join(r.warnings, "\n"), "aws") {
		t.Fatalf("warnings: %v", r.warnings)
	}

	r.refuse = false
	r.refusing[otherRoleArn] = true
	report, err = r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.Lapsed != 1 || report.Probed != 1 || r.store.rows[second.ID].Status != StatusFailed || r.store.rows[first.ID].Status != StatusReady {
		t.Fatalf("one refused: %+v, rows %+v %+v", report, r.store.rows[first.ID], r.store.rows[second.ID])
	}
}

// A connection the background check failed is checked again a day later,
// and goes back to ready once the cloud trusts Loomplane again, without a
// notice or a new generation.
func TestKeylessCheckHealsWhatItFailedItself(t *testing.T) {
	r := newKeylessRefreshRig(t)
	c := r.connect(t)
	r.refuse = true
	r.clock = r.clock.Add(48 * time.Hour)
	if _, err := r.s.RefreshDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	failed := r.store.rows[c.ID]
	if failed.Status != StatusFailed || !strings.HasPrefix(failed.Message, keylessCheckPrefix) || len(r.lapsed) != 1 {
		t.Fatalf("failed row %+v, lapsed %v", failed, r.lapsed)
	}

	// Still refused a day on: nobody is told again, and it waits a day.
	r.clock = r.clock.Add(25 * time.Hour)
	report, err := r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	still := r.store.rows[c.ID]
	if report.Lapsed != 0 || len(r.lapsed) != 1 || still.Status != StatusFailed || still.CheckedAt == nil || !still.CheckedAt.Equal(r.clock) {
		t.Fatalf("still refused: %+v, row %+v", report, still)
	}
	r.checked = nil
	if _, err = r.s.RefreshDue(t.Context()); err != nil || len(r.checked) != 0 {
		t.Fatalf("checked again within a day: %v", err)
	}

	r.refuse = false
	r.clock = r.clock.Add(25 * time.Hour)
	if report, err = r.s.RefreshDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	healed := r.store.rows[c.ID]
	if report.Probed != 1 || healed.Status != StatusReady || healed.Message != "" || healed.Generation != c.Generation ||
		healed.CheckedAt == nil || !healed.CheckedAt.Equal(r.clock) || len(r.lapsed) != 1 {
		t.Fatalf("healed: %+v, row %+v, lapsed %v", report, healed, r.lapsed)
	}
}

// A Recheck you ran that failed is yours to act on, so the background
// check leaves it failed even once the cloud trusts Loomplane again.
func TestKeylessCheckLeavesARecheckFailureAlone(t *testing.T) {
	r := newKeylessRefreshRig(t)
	c := r.connect(t)
	r.refuse = true
	if _, err := r.s.Recheck(t.Context(), starter, c.ID); err != nil {
		t.Fatal(err)
	}
	failed := r.store.rows[c.ID]
	if failed.Status != StatusFailed || strings.HasPrefix(failed.Message, keylessCheckPrefix) {
		t.Fatalf("recheck: %+v", failed)
	}
	r.refuse = false
	r.checked = nil
	r.clock = r.clock.Add(48 * time.Hour)
	report, err := r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report != (RefreshReport{}) || len(r.checked) != 0 {
		t.Fatalf("report %+v, checked %d bytes", report, len(r.checked))
	}
	r.unchanged(t, failed)
}

func TestKeylessRefusalSeenBeforeANewerCheckMarksNothing(t *testing.T) {
	r := newKeylessRefreshRig(t)
	c := r.connect(t)
	r.refuse = true
	r.clock = r.clock.Add(48 * time.Hour)
	var report RefreshReport
	stale := r.store.rows[c.ID]
	refusal, _ := r.s.probeKeyless(t.Context(), &stale, &report)
	if refusal == nil {
		t.Fatal("the refusal wasn't returned")
	}
	// Someone presses Check again and the cloud answers before the run
	// marks what it saw.
	row := r.store.rows[c.ID]
	later := refusal.at.Add(time.Second)
	row.CheckedAt = &later
	r.store.rows[c.ID] = row
	r.s.markKeyless(t.Context(), *refusal, &report)
	got := r.store.rows[c.ID]
	if got.Status != StatusReady || got.Message != "" || len(r.lapsed) != 0 || report.Lapsed != 0 {
		t.Fatalf("status %s, message %q, lapsed %d, report %+v", got.Status, got.Message, len(r.lapsed), report)
	}
}
