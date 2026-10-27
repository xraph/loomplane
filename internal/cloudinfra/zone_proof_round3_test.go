package cloudinfra

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/projects"
)

func TestZoneProofForegroundWriteHasADeadline(t *testing.T) {
	prior := zoneProofWriteTimeout
	zoneProofWriteTimeout = 100 * time.Millisecond
	t.Cleanup(func() { zoneProofWriteTimeout = prior })
	if prior >= zoneProofCheckEvery {
		t.Fatal("a foreground write may outlast its grace window", prior)
	}
	env := newProofEnv(t, "vercel")
	env.dns.blockWrite = true
	started := time.Now()
	zone := env.create(t, "managed")
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatal("foreground write ran unbounded", elapsed)
	}
	// Cut short, its outcome is unknown, and that is saved.
	stored := env.stored(t, zone.ID)
	if !stored.ProofPublishAttempted || stored.ProofPublishError == "" {
		t.Fatalf("cut-short write not recorded: %+v", stored)
	}
}

// cancelledAudit drops events whose context has ended, as a real sink can.
type cancelledAudit struct{ events []projects.AuditEvent }

func (a *cancelledAudit) Record(ctx context.Context, e projects.AuditEvent) {
	if ctx.Err() == nil {
		a.events = append(a.events, e)
	}
}

func TestZoneProofBackgroundWriteIsAuditedPastItsDeadline(t *testing.T) {
	env := newProofEnv(t, "vercel")
	audit := &cancelledAudit{}
	env.svc.Audit = audit
	env.dns.upsertErr = errors.New("provider unavailable")
	zone := env.create(t, "managed")
	env.dns.upsertErr = nil
	audit.events = nil
	pass, perZone := zoneProofPassTimeout, zoneProofZoneTimeout
	zoneProofPassTimeout, zoneProofZoneTimeout = time.Second, 100*time.Millisecond
	t.Cleanup(func() { zoneProofPassTimeout, zoneProofZoneTimeout = pass, perZone })
	// The write lands, then the public check runs out of time.
	env.dns.block = true
	_ = env.svc.CheckZoneProofs(t.Context())
	if env.dns.writes != 1 || len(audit.events) != 1 || audit.events[0].Attributes["resource_id"] != zone.ID {
		t.Fatalf("writes=%d events=%+v", env.dns.writes, audit.events)
	}
}

func (env *proofEnv) renew(t *testing.T, id string) *Zone {
	t.Helper()
	stored := env.stored(t, id)
	renewed, err := env.svc.UpdateZone(t.Context(), proofManager, id, ZoneUpdate{Revision: stored.Revision, ProjectIDs: stored.ProjectIDs, RenewProof: true})
	if err != nil {
		t.Fatal(err)
	}
	return renewed
}

func TestRemoveZoneLeavesAnEditedRecordAlone(t *testing.T) {
	env := newProofEnv(t, "digitalocean")
	zone := env.create(t, "managed")
	// A renewal whose answer is lost leaves the write unconfirmed.
	env.dns.dropWrite = true
	env.renew(t, zone.ID)
	env.dns.dropWrite = false
	// Someone edits the record at the receipt's ID by hand.
	env.dns.records[1].Value = "edited-by-hand"
	env.pastGrace()
	err := env.svc.RemoveZone(t.Context(), proofManager, zone.ID, env.stored(t, zone.ID).Revision)
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "can't confirm whether its TXT record landed") || env.dns.deletes != 0 || len(env.store.zones) != 1 {
		t.Fatalf("edited record: %v deletes=%d", err, env.dns.deletes)
	}
}
