//go:build integration

package dbgateway

import (
	"path/filepath"
	"testing"
	"time"
)

// TestE2ERotatesTheSigningKey moves a running gateway from control-plane
// key a to key b the way docs/postgres-services.md says to: b goes into the
// key file beside a with a window that opens later, Loomplane restarts, the
// gateway is redeployed trusting both, b takes over when it opens, and a
// leaves the file. Each step changes the route, and the gateway serving at
// the time applies the change: no snapshot goes undelivered on the way.
func TestE2ERotatesTheSigningKey(t *testing.T) {
	g := newE2ERig(t, 0)
	ctx := t.Context()
	now := time.Now().UTC()
	a := newE2EKey(t, now.Add(-time.Hour), now.Add(24*time.Hour))
	g.restartControlPlane(a.fileV1())
	g.redeploy()
	oldState := filepath.Join(t.TempDir(), "old")
	old := g.start(oldState)
	query := func(p *gatewayProc) {
		t.Helper()
		c := p.connect(g)
		defer c.Close(ctx)
		var one int
		if err := c.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("SELECT 1 on :%d: %d %v", p.port, one, err)
		}
	}
	// change pauses the route and resumes it, and waits for each gateway
	// in gws to apply both.
	change := func(gws ...*gatewayProc) {
		t.Helper()
		for _, state := range []string{"paused", "active"} {
			before := make([]uint64, len(gws))
			for i, p := range gws {
				before[i] = p.applied()
			}
			if err := g.store.SetRouteState(ctx, "org_1", g.gw.ID, g.route.ID, state); err != nil {
				t.Fatal(err)
			}
			for i, p := range gws {
				p.waitApplied(before[i], 10*time.Second)
			}
		}
	}
	signer := func() string {
		t.Helper()
		env, err := g.svc.Snapshot(ctx, &g.gw, 0)
		if err != nil || env == nil {
			t.Fatalf("Snapshot: %+v %v", env, err)
		}
		return env.KeyID
	}
	query(old)

	// 1. b goes in beside a, opening once the redeploy below has had time to
	// finish, and Loomplane restarts. It goes on signing with a, and would
	// hand a new gateway both keys. The gateway that trusts only a still
	// takes every change.
	opens := time.Now().Add(10 * time.Second)
	b := newE2EKey(t, opens, opens.Add(90*24*time.Hour))
	g.restartControlPlane(e2eKeyFile(a, b))
	if got := signer(); got != a.id() {
		t.Fatal("Loomplane signed with b before its window opened")
	}
	if trust := g.svc.TrustKeys(); len(trust) != 2 {
		t.Fatalf("Loomplane hands out %d trust keys, want a and b", len(trust))
	}
	change(old)

	// 2. The redeploy: a gateway trusting a and b comes up, and the old one
	// stops after it, as Cloud Run rolls a revision. Both take a change made
	// while they overlap.
	g.redeploy()
	nextState := filepath.Join(t.TempDir(), "next")
	next := g.start(nextState)
	change(old, next)
	if time.Now().After(opens) {
		t.Fatal("the redeploy finished after b's window opened, so the test can't tell a gap from none; give b more time")
	}
	old.terminate()
	if err := old.wait(20 * time.Second); err != nil {
		t.Fatalf("the old gateway's shutdown: %v", err)
	}
	query(next)

	// 3. b's window opens. Loomplane signs with b from then on, with no
	// restart, and the redeployed gateway takes what it signs.
	time.Sleep(time.Until(opens) + 100*time.Millisecond)
	if got := signer(); got != b.id() {
		t.Fatal("Loomplane didn't move to b when its window opened")
	}
	change(next)
	query(next)

	// 4. a leaves the key file and Loomplane restarts. The gateway is
	// redeployed trusting b alone, starts from the last-good snapshot b
	// signed, and takes the next change.
	g.restartControlPlane(e2eKeyFile(b))
	if trust := g.svc.TrustKeys(); len(trust) != 1 || trust[0].ID() != b.id() {
		t.Fatalf("after a leaves, Loomplane hands out %+v", trust)
	}
	g.redeploy()
	next.terminate()
	if err := next.wait(20 * time.Second); err != nil {
		t.Fatalf("the gateway's shutdown: %v", err)
	}
	last := g.start(nextState)
	if !last.waitLogged("database gateway applied the last-good snapshot", 5*time.Second) {
		t.Fatal("the gateway trusting b alone didn't take the last-good snapshot b signed")
	}
	change(last)
	query(last)
}
