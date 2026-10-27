//go:build integration

package plane

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
)

// serveGateway accepts clients on a local listener and runs ServeSession for
// each over route, trusting every client. It returns the listener's address.
func serveGateway(t *testing.T, route SessionRoute) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				be := pgproto3.NewBackend(conn, conn)
				msg, err := be.ReceiveStartupMessage()
				if err != nil {
					return
				}
				sm, ok := msg.(*pgproto3.StartupMessage)
				if !ok {
					return
				}
				be.Send(&pgproto3.AuthenticationOk{})
				_ = ServeSession(ctx, be, route, sm.Parameters)
			}()
		}
	}()
	return ln.Addr().String()
}

// TestStatementCapsAgainstPostgres runs a long-lived pgx client through a
// gateway with one backend connection. The client prepares thousands of
// distinct named statements, with a statement cache smaller than that, so
// it closes the ones it evicts, and the backend connection's cap is smaller
// still. Every query must still return its own result, and the backend must
// never hold more statements than the cap.
func TestStatementCapsAgainstPostgres(t *testing.T) {
	const (
		total     = 2500
		connCap   = 8
		cacheSize = 32
	)
	b, s := testBackend(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	pool := NewPool(PoolConfig{Backend: b, Secret: s, Params: map[string]string{"application_name": "loomplane-gateway-test"}, MaxStatements: connCap}, PgDialer{})
	pool.SetLimit(1)
	t.Cleanup(pool.Close)
	l, err := pool.Acquire(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	params := copyParams(l.Conn().Params)
	l.Release(LeaseClean)
	addr := serveGateway(t, &fakeRoute{pool: pool, removed: make(chan struct{}), params: params})

	host, port, _ := strings.Cut(addr, ":")
	cfg, err := pgx.ParseConfig(fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", s.User, "unused", host, port, b.Database))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeCacheStatement
	cfg.StatementCacheCapacity = cacheSize
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	// held is how many gateway statements the one backend connection holds.
	held := func() int {
		t.Helper()
		var n int
		qerr := conn.QueryRow(ctx, `SELECT count(*)::int FROM pg_prepared_statements WHERE name LIKE 'lp\_%'`,
			pgx.QueryExecModeSimpleProtocol).Scan(&n)
		if qerr != nil {
			t.Fatal(qerr)
		}
		return n
	}

	most := 0
	for i := range total {
		var got int
		// Each text is a distinct statement.
		if err = conn.QueryRow(ctx, fmt.Sprintf("SELECT $1::int + %d", i), i).Scan(&got); err != nil {
			t.Fatalf("query %d: %v", i, err)
		}
		if got != 2*i {
			t.Fatalf("query %d returned %d, want %d", i, got, 2*i)
		}
		// One still in the driver's cache, but long gone from the connection.
		if i >= cacheSize/2 {
			old := i - cacheSize/2
			if err = conn.QueryRow(ctx, fmt.Sprintf("SELECT $1::int + %d", old), old).Scan(&got); err != nil {
				t.Fatalf("query %d again: %v", old, err)
			}
			if got != 2*old {
				t.Fatalf("query %d again returned %d, want %d", old, got, 2*old)
			}
		}
		if i%250 == 0 {
			most = max(most, held())
		}
	}
	most = max(most, held())
	if most > connCap {
		t.Fatalf("the backend connection held %d gateway statements, want at most %d", most, connCap)
	}
	if most == 0 {
		t.Fatal("the backend connection held no gateway statements, so the test proved nothing")
	}
	if n := pool.Stats().Open; n != 1 {
		t.Fatalf("pool opened %d connections, want 1", n)
	}

	// The same client in a transaction that uses more statements than the
	// cap: all of them stay until it commits.
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 * connCap {
		var got int
		if err := tx.QueryRow(ctx, fmt.Sprintf("SELECT $1::int * %d", i+1), 3).Scan(&got); err != nil {
			t.Fatalf("query %d in the transaction: %v", i, err)
		}
		if got != 3*(i+1) {
			t.Fatalf("query %d in the transaction returned %d", i, got)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// The next new statement sheds the excess.
	var got int
	if err := conn.QueryRow(ctx, "SELECT $1::int - 1", 1).Scan(&got); err != nil || got != 0 {
		t.Fatalf("after the transaction: %d, %v", got, err)
	}
	if n := held(); n > connCap {
		t.Fatalf("after the transaction the connection holds %d gateway statements, want at most %d", n, connCap)
	}
}
