package cloudinfra

import (
	"context"
	"errors"
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
