package cloudinfra

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/projects"
)

// pastGrace moves the clock past the window in which an unanswered write
// may still be landing.
func (env *proofEnv) pastGrace() { env.now = env.now.Add(zoneProofCheckEvery + time.Second) }

func TestZoneProofCheckDuringAnInFlightCreateWritesNothing(t *testing.T) {
	for _, check := range []string{"check again", "background"} {
		t.Run(check, func(t *testing.T) {
			env := newProofEnv(t, "vercel")
			// While CreateZone's write is in flight, a check sees its marker.
			env.dns.during = func() {
				if check == "background" {
					if err := env.svc.CheckZoneProofs(t.Context()); err != nil {
						t.Error(err)
					}
					return
				}
				_, _ = env.svc.VerifyZone(t.Context(), proofManager, env.store.zones[0].ID)
			}
			zone := env.create(t, "managed")
			proofs := env.dns.proofs()
			if env.dns.writes != 1 || len(proofs) != 1 || env.dns.lists != 0 {
				t.Fatalf("racing check wrote: writes=%d lists=%d proofs=%+v", env.dns.writes, env.dns.lists, proofs)
			}
			// CreateZone's receipt survives the check's save.
			stored := env.stored(t, zone.ID)
			if stored.ProofRecord == nil || *stored.ProofRecord != proofs[0] || stored.ProofPublishAttempted {
				t.Fatalf("receipt lost to the racing check: %+v", stored)
			}
		})
	}
}

func TestZoneProofReceiptSurvivesASaveConflict(t *testing.T) {
	env := newProofEnv(t, "vercel")
	env.dns.during = func() {
		// Another writer saves the zone while the record is written.
		z, _ := env.store.Zone(t.Context(), "org", env.store.zones[0].ID)
		z.ProjectIDs = append(z.ProjectIDs, "shop")
		if err := env.store.SaveZone(t.Context(), z); err != nil {
			t.Error(err)
		}
	}
	zone := env.create(t, "managed")
	stored := env.stored(t, zone.ID)
	if stored.ProofRecord == nil || stored.ProofPublishAttempted || len(stored.ProjectIDs) != 2 || zone.Revision != stored.Revision {
		t.Fatalf("receipt or the other write lost: %+v", stored)
	}
}

func TestZoneProofLostRenewIsSettledByTheRecordID(t *testing.T) {
	for _, landed := range []bool{false, true} {
		for _, cloud := range []string{"vercel", "digitalocean"} {
			t.Run(cloud, func(t *testing.T) {
				env := newProofEnv(t, cloud)
				zone := env.create(t, "managed")
				first := env.dns.proofs()[0]
				env.dns.dropWrite, env.dns.loseResponse = !landed, landed
				renewed, err := env.svc.UpdateZone(t.Context(), proofManager, zone.ID, ZoneUpdate{Revision: env.stored(t, zone.ID).Revision, ProjectIDs: zone.ProjectIDs, RenewProof: true})
				if err != nil {
					t.Fatal(err)
				}
				env.dns.dropWrite, env.dns.loseResponse = false, false
				writes := env.dns.writes
				env.pastGrace()
				_, _ = env.svc.VerifyZone(t.Context(), proofManager, zone.ID)
				proofs := env.dns.proofs()
				stored := env.stored(t, zone.ID)
				if len(proofs) != 1 || proofs[0].ID != first.ID || proofs[0].Value != renewed.ProofValue || stored.ProofRecord == nil || *stored.ProofRecord != proofs[0] || stored.ProofPublishAttempted || stored.ProofPublishError != "" {
					t.Fatalf("landed=%v proofs=%+v stored=%+v", landed, proofs, stored)
				}
				// The update that never landed is sent again; one that landed is adopted.
				want := writes
				if !landed {
					want++
				}
				if env.dns.writes != want {
					t.Fatalf("landed=%v writes=%d want %d", landed, env.dns.writes, want)
				}
			})
		}
	}
}

func TestZoneProofRenewAfterALostCreateUpdatesItsOwnRecord(t *testing.T) {
	env := newProofEnv(t, "vercel")
	env.dns.loseResponse = true
	zone := env.create(t, "managed")
	env.dns.loseResponse = false
	env.pastGrace()
	renewed, err := env.svc.UpdateZone(t.Context(), proofManager, zone.ID, ZoneUpdate{Revision: env.stored(t, zone.ID).Revision, ProjectIDs: zone.ProjectIDs, RenewProof: true})
	if err != nil {
		t.Fatal(err)
	}
	proofs := env.dns.proofs()
	stored := env.stored(t, zone.ID)
	if len(proofs) != 1 || proofs[0].Value != renewed.ProofValue || stored.ProofRecord == nil || *stored.ProofRecord != proofs[0] || stored.ProofPublishError != "" {
		t.Fatalf("own record not updated: proofs=%+v stored=%+v", proofs, stored)
	}
}

func TestRemoveZoneSettlesAnUnconfirmedCreate(t *testing.T) {
	t.Run("landed", func(t *testing.T) {
		env := newProofEnv(t, "vercel")
		env.dns.loseResponse = true
		zone := env.create(t, "managed")
		if err := env.svc.RemoveZone(t.Context(), proofManager, zone.ID, env.stored(t, zone.ID).Revision); err != nil {
			t.Fatal(err)
		}
		if len(env.dns.proofs()) != 0 || len(env.store.zones) != 0 {
			t.Fatal("landed record orphaned", env.dns.records)
		}
	})
	t.Run("nothing landed", func(t *testing.T) {
		env := newProofEnv(t, "vercel")
		env.dns.dropWrite = true
		zone := env.create(t, "managed")
		// It may still land inside the grace window.
		if err := env.svc.RemoveZone(t.Context(), proofManager, zone.ID, env.stored(t, zone.ID).Revision); err == nil || len(env.store.zones) != 1 {
			t.Fatal("removed while the write may still land", err)
		}
		env.pastGrace()
		if err := env.svc.RemoveZone(t.Context(), proofManager, zone.ID, env.stored(t, zone.ID).Revision); err != nil || len(env.store.zones) != 0 {
			t.Fatal("settled attempt still blocks removal", err)
		}
	})
	for _, variant := range []string{"no owner tokens", "listing fails"} {
		t.Run(variant, func(t *testing.T) {
			cloud := "digitalocean"
			if variant == "listing fails" {
				cloud = "vercel"
			}
			env := newProofEnv(t, cloud)
			env.dns.loseResponse = true
			zone := env.create(t, "managed")
			if variant == "listing fails" {
				env.dns.listErr = errors.New("provider unavailable")
			}
			env.pastGrace()
			err := env.svc.RemoveZone(t.Context(), proofManager, zone.ID, env.stored(t, zone.ID).Revision)
			if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "can't confirm whether its TXT record landed") || len(env.store.zones) != 1 || len(env.dns.proofs()) != 1 {
				t.Fatalf("%v zones=%d", err, len(env.store.zones))
			}
		})
	}
}

func TestZoneProofRotatedIDMovesTheReceipt(t *testing.T) {
	env := newProofEnv(t, "vercel")
	zone := env.create(t, "managed")
	env.dns.records[1].ID = "rotated"
	env.now = env.now.Add(zoneProofRelistAfter)
	_, _ = env.svc.VerifyZone(t.Context(), proofManager, zone.ID)
	stored := env.stored(t, zone.ID)
	if stored.ProofRecord == nil || stored.ProofRecord.ID != "rotated" {
		t.Fatalf("receipt kept the old ID: %+v", stored.ProofRecord)
	}
	if err := env.svc.RemoveZone(t.Context(), proofManager, zone.ID, stored.Revision); err != nil || env.dns.deletes != 1 || len(env.dns.proofs()) != 0 {
		t.Fatal("rotated record left behind", err, env.dns.records)
	}
}

func TestZoneProofBackgroundPassIsTimeBounded(t *testing.T) {
	env := newProofEnv(t, "vercel")
	env.create(t, "managed")
	env.create(t, "managed")
	env.dns.block = true
	pass, perZone := zoneProofPassTimeout, zoneProofZoneTimeout
	zoneProofPassTimeout, zoneProofZoneTimeout = 150*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { zoneProofPassTimeout, zoneProofZoneTimeout = pass, perZone })
	started := time.Now()
	_ = env.svc.CheckZoneProofs(t.Context())
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatal("background pass ran unbounded", elapsed)
	}
}

func TestZoneProofCheckSkipsWorkItDoesNotNeed(t *testing.T) {
	t.Run("no re-list in the pass that wrote", func(t *testing.T) {
		env := newProofEnv(t, "vercel")
		env.dns.upsertErr = errors.New("provider unavailable")
		zone := env.create(t, "managed")
		env.dns.upsertErr = nil
		env.now = env.now.Add(zoneProofRelistAfter)
		_, _ = env.svc.VerifyZone(t.Context(), proofManager, zone.ID)
		if env.dns.writes != 1 || env.dns.lists != 0 {
			t.Fatalf("writes=%d lists=%d", env.dns.writes, env.dns.lists)
		}
	})
	t.Run("no re-list of a fresh receipt", func(t *testing.T) {
		env := newProofEnv(t, "vercel")
		zone := env.create(t, "managed")
		_, _ = env.svc.VerifyZone(t.Context(), proofManager, zone.ID)
		if env.dns.lists != 0 {
			t.Fatal("fresh receipt re-listed", env.dns.lists)
		}
		env.now = env.now.Add(zoneProofRelistAfter)
		_, _ = env.svc.VerifyZone(t.Context(), proofManager, zone.ID)
		if env.dns.lists != 1 {
			t.Fatal("old receipt not re-listed", env.dns.lists)
		}
	})
	t.Run("no publish of an expired value", func(t *testing.T) {
		env := newProofEnv(t, "vercel")
		env.dns.upsertErr = errors.New("provider unavailable")
		zone := env.create(t, "managed")
		env.dns.upsertErr = nil
		env.now = env.now.Add(25 * time.Hour)
		_, _ = env.svc.VerifyZone(t.Context(), proofManager, zone.ID)
		if env.dns.writes != 0 {
			t.Fatal("expired proof published", env.dns.writes)
		}
	})
	t.Run("no publish for a zone verified before receipts", func(t *testing.T) {
		env := newProofEnv(t, "vercel")
		env.dns.upsertErr = errors.New("provider unavailable")
		zone := env.create(t, "managed")
		env.dns.upsertErr = nil
		legacy := env.stored(t, zone.ID)
		// Verified by hand before Loomplane published proofs: never written.
		legacy.VerifiedAt, legacy.ProofPublishError, legacy.ProofPublishAttemptedAt = env.now, "", time.Time{}
		if err := env.store.SaveZone(t.Context(), legacy); err != nil {
			t.Fatal(err)
		}
		env.resolver.values = []string{zone.ProofValue}
		verified, err := env.svc.VerifyZone(t.Context(), proofManager, zone.ID)
		if err != nil || env.dns.writes != 0 || verified.ProofPublishError != "" {
			t.Fatalf("legacy zone published: writes=%d %v %+v", env.dns.writes, err, verified)
		}
	})
}

func TestRemoveZoneDeletesAtTheRevisionItWasGiven(t *testing.T) {
	env := newProofEnv(t, "vercel")
	zone := env.create(t, "managed")
	saves := env.store.saves
	if err := env.svc.RemoveZone(t.Context(), proofManager, zone.ID, zone.Revision); err != nil {
		t.Fatal(err)
	}
	if env.store.saves != saves || len(env.store.zones) != 0 {
		t.Fatal("removal saved the zone in between", env.store.saves-saves)
	}
}

func TestZoneProofBackgroundWorkIsAudited(t *testing.T) {
	env := newProofEnv(t, "vercel")
	audit := &recordingAudit{}
	env.svc.Audit = audit
	env.dns.upsertErr = errors.New("provider unavailable")
	zone := env.create(t, "managed")
	env.dns.upsertErr = nil
	audit.events = nil
	env.resolver.values = []string{zone.ProofValue}
	if err := env.svc.CheckZoneProofs(t.Context()); err != nil {
		t.Fatal(err)
	}
	var found []projects.AuditEvent
	for _, e := range audit.events {
		if e.Action == "domain.zone.write" && e.Principal.UserID == zoneProofSystemUser && e.Principal.OrgID == "org" && e.Attributes["resource_id"] == zone.ID {
			found = append(found, e)
		}
	}
	// One event for a pass that wrote and verified.
	if len(found) != 1 {
		t.Fatalf("background work not audited: %+v", audit.events)
	}
}

func TestZoneProofsDueSkipsAWriteInFlight(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	checkedBefore := now.Add(-zoneProofCheckEvery)
	zone := &Zone{Mode: "managed", Name: "acme.dev", ProofName: "_loomplane.acme.dev", ProofValue: "v", ProofExpiresAt: now.Add(time.Hour), ProofPublishAttempted: true, ProofPublishAttemptedAt: now.Add(-time.Second)}
	if zoneProofDue(zone, now, checkedBefore, now.Add(-zoneProofRecheckEvery)) {
		t.Fatal("zone with a write in flight is due")
	}
	zone.ProofPublishAttemptedAt = checkedBefore.Add(-time.Second)
	if !zoneProofDue(zone, now, checkedBefore, now.Add(-zoneProofRecheckEvery)) {
		t.Fatal("settled attempt not due")
	}
}
