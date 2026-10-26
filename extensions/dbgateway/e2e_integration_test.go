//go:build integration

package dbgateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/dbgateway/plane"
)

// TestE2EOverWebSocket runs the gateway program built from dbgateway.Source()
// against the real control plane and a real Postgres, and talks to it with
// pgx over the WebSocket a Neon-style driver uses.
func TestE2EOverWebSocket(t *testing.T) {
	// Stand-in instances shrink each one's share of the backend to two
	// connections, so two clients have to share them.
	maxConns := backendMaxConnections(t)
	live := (maxConns - plane.Reserve(maxConns)) / 2
	g := newE2ERig(t, live-1)
	state := filepath.Join(t.TempDir(), "state")
	gw := g.start(state)
	ctx := t.Context()

	// 1. A query.
	a := gw.connect(g)
	var one int
	if err := a.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("SELECT 1: %d %v", one, err)
	}
	if got := g.ownInstance(15*time.Second, func(in dbgateway.InstanceRecord) bool { return len(in.Metrics) == 1 }); got.Metrics[0].RouteID != g.route.ID {
		t.Fatalf("the gateway reports %+v", got)
	}

	// 2. A named prepared statement survives 20 transactions while two other
	// clients churn leases on the pool's two backend connections: each time
	// they take both, then hand back the one the statement didn't run on
	// last, so every transaction runs on the other backend.
	if _, err := a.Prepare(ctx, "e2e_next", "SELECT $1::int + 1, pg_backend_pid()"); err != nil {
		t.Fatal(err)
	}
	b, b2 := gw.connect(g), gw.connect(g)
	hold := func(c *pgx.Conn) (pgx.Tx, int) {
		t.Helper()
		tx, err := c.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var pid int
		if err = tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
			t.Fatal(err)
		}
		return tx, pid
	}
	backends := map[int]bool{}
	last := 0
	for i := range 20 {
		tx1, p1 := hold(b)
		tx2, p2 := hold(b2)
		backends[p1], backends[p2] = true, true
		free, keep, want := tx1, tx2, p1
		if p1 == last {
			free, keep, want = tx2, tx1, p2
		}
		if err := free.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		tx, err := a.Begin(ctx)
		if err != nil {
			t.Fatalf("transaction %d: begin: %v", i, err)
		}
		var next, pid int
		if err = tx.QueryRow(ctx, "e2e_next", i).Scan(&next, &pid); err != nil || next != i+1 {
			t.Fatalf("transaction %d: the prepared statement gave %d %v", i, next, err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatalf("transaction %d: commit: %v", i, err)
		}
		if pid != want || pid == last {
			t.Fatalf("transaction %d ran on backend %d, want %d, the one handed back (the last was %d)", i, pid, want, last)
		}
		if err = keep.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		last = pid
	}
	if len(backends) != 2 {
		t.Fatalf("the clients ran on backends %v, want exactly two", backends)
	}
	t.Logf("the prepared statement ran 20 times, switching between backends %v", backends)

	// 3. LISTEN on one client hears NOTIFY from another, through the
	// gateway's shared listener.
	if _, err := a.Exec(ctx, "LISTEN e2e"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Exec(ctx, "NOTIFY e2e, 'x'"); err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	n, err := a.WaitForNotification(wctx)
	cancel()
	if err != nil || n.Channel != "e2e" || n.Payload != "x" {
		t.Fatalf("notification %+v %v", n, err)
	}

	// 4. A session advisory lock pins the session, and the gateway's next
	// report says so. Nothing is pinned before the lock.
	if before := g.ownInstance(5*time.Second, func(in dbgateway.InstanceRecord) bool { return len(in.Metrics) == 1 }); before.Metrics[0].Pinned != 0 {
		t.Fatalf("before the lock the gateway reported %+v", before.Metrics[0])
	}
	pinned := gw.connect(g)
	if _, err = pinned.Exec(ctx, "SELECT pg_advisory_lock(1)"); err != nil {
		t.Fatal(err)
	}
	pinStart := time.Now()
	in := g.ownInstance(25*time.Second, func(in dbgateway.InstanceRecord) bool {
		return len(in.Metrics) == 1 && in.Metrics[0].Pinned >= 1
	})
	if m := in.Metrics[0]; m.RouteID != g.route.ID || !m.Reachable || m.MaxConnections != maxConns {
		t.Fatalf("the pinned report %+v", m)
	}
	t.Logf("the gateway reported %d pinned after %s", in.Metrics[0].Pinned, time.Since(pinStart).Round(time.Millisecond))
	// While the lock is held, the project's members see the pin through the
	// management route: the instance's route has a pinned session.
	out := callAs(t, g.router, http.MethodGet, "/api/v1/projects/proj_1/db-gateways", "usr_admin")
	var list GatewayList
	if out.Code != http.StatusOK || json.Unmarshal(out.Body.Bytes(), &list) != nil || len(list.Gateways) != 1 {
		t.Fatalf("list: %d %s", out.Code, out.Body.String())
	}
	if rs := list.Gateways[0].Routes; len(rs) != 1 || !rs[0].Reported || !rs[0].Reachable || rs[0].State != "active" {
		t.Fatalf("listed routes %+v", rs)
	}
	listedPin := false
	for _, li := range list.Gateways[0].Instances {
		if li.ID != in.InstanceID {
			continue
		}
		for _, r := range li.Routes {
			listedPin = listedPin || (r.RouteID == g.route.ID && r.Pinned >= 1)
		}
	}
	if !listedPin {
		t.Fatalf("the list doesn't show the pinned session on instance %s: %s", in.InstanceID, out.Body.String())
	}
	if _, err = pinned.Exec(ctx, "SELECT pg_advisory_unlock(1)"); err != nil {
		t.Fatal(err)
	}
	_ = pinned.Close(ctx)

	// 5. SIGTERM during an open transaction: new connections are refused
	// while the gateway drains, the transaction still commits, and then the
	// gateway exits cleanly.
	direct, err := pgx.Connect(ctx, g.direct)
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close(context.Background())
	if _, err = direct.Exec(ctx, "CREATE TABLE e2e_drain (n int)"); err != nil {
		t.Fatal(err)
	}
	c := gw.connect(g)
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO e2e_drain VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	gw.terminate()
	if body := gw.waitHealth(http.StatusServiceUnavailable, 10*time.Second); body != "draining" {
		t.Fatalf("/healthz while draining said %q", body)
	}
	late, lerr := pgx.ConnectConfig(ctx, gw.pgConfig(g.user, g.password, g.database))
	if lerr == nil {
		_ = late.Close(ctx)
		t.Fatal("a new connection was accepted while the gateway drained")
	}
	var refused *handshakeError
	if !errors.As(lerr, &refused) || refused.status != http.StatusServiceUnavailable {
		t.Fatalf("while draining, a new connection got %v, want the WebSocket upgrade refused with 503", lerr)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatalf("COMMIT after SIGTERM: %v", err)
	}
	var rows int
	if err = direct.QueryRow(ctx, "SELECT count(*) FROM e2e_drain").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("committed rows %d %v", rows, err)
	}
	if err = gw.wait(40 * time.Second); err != nil {
		t.Fatalf("the gateway's exit after SIGTERM: %v", err)
	}
	if late, lerr := pgx.ConnectConfig(ctx, gw.pgConfig(g.user, g.password, g.database)); lerr == nil {
		_ = late.Close(ctx)
		t.Fatal("a new connection was accepted after the gateway exited")
	}
	g.ownInstance(5*time.Second, func(in dbgateway.InstanceRecord) bool { return in.Draining })

	// 6. A new instance, starting from the last-good snapshot the first one
	// saved. Pausing the route makes the next transaction wait, and resuming
	// lets it through.
	gw = g.start(state)
	d := gw.connect(g)
	if _, err = d.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	before := gw.applied()
	pause := "/api/v1/projects/proj_1/db-gateways/" + g.gw.ID + "/routes/" + g.route.ID
	if out = callAs(t, g.router, http.MethodPost, pause+"/pause", "usr_admin"); out.Code != http.StatusOK {
		t.Fatalf("pause: %d %s", out.Code, out.Body.String())
	}
	t.Logf("paused route in snapshot version %d", gw.waitApplied(before, 10*time.Second))
	held := make(chan error, 1)
	go func() {
		_, qerr := d.Exec(ctx, "SELECT 1")
		held <- qerr
	}()
	select {
	case err = <-held:
		t.Fatalf("a query on a paused route ran at once: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if out = callAs(t, g.router, http.MethodPost, pause+"/resume", "usr_admin"); out.Code != http.StatusOK {
		t.Fatalf("resume: %d %s", out.Code, out.Body.String())
	}
	select {
	case err = <-held:
		if err != nil {
			t.Fatalf("the held query after resuming: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("resuming didn't release the held query")
	}
	gw.terminate()
	if err = gw.wait(40 * time.Second); err != nil {
		t.Fatalf("the second gateway's exit: %v", err)
	}
}

// backendMaxConnections is the test Postgres's max_connections, which the
// gateway sizes its pools from.
func backendMaxConnections(t *testing.T) int {
	t.Helper()
	dsn := testDSN(t)
	c, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	var n int
	if err = c.QueryRow(t.Context(), "SELECT current_setting('max_connections')::int").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
