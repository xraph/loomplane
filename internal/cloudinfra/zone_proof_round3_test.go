package cloudinfra

import (
	"testing"
	"time"
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
