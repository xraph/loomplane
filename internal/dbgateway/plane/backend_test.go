package plane

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

func TestBudget(t *testing.T) {
	reserves := []struct{ max, want int }{
		{100, 10}, {20, 5}, {101, 11}, {50, 5}, {51, 6}, {0, 5},
	}
	for _, c := range reserves {
		if got := Reserve(c.max); got != c.want {
			t.Errorf("Reserve(%d) = %d, want %d", c.max, got, c.want)
		}
	}
	budgets := []struct{ max, instances, want int }{
		{100, 3, 30}, {20, 4, 3}, {6, 10, 1}, {100, 0, 90}, {100, -2, 90}, {0, 1, 1},
	}
	for _, c := range budgets {
		if got := Budget(c.max, c.instances); got != c.want {
			t.Errorf("Budget(%d, %d) = %d, want %d", c.max, c.instances, got, c.want)
		}
	}
}

// fakeBackend runs a scripted Postgres server on one end of a pipe and returns
// a BackendConn on the other. The script runs after the first client message.
func fakeBackend(t *testing.T, script func(b *pgproto3.Backend)) *BackendConn {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	go func() {
		b := pgproto3.NewBackend(server, server)
		if _, err := b.Receive(); err != nil {
			return
		}
		script(b)
	}()
	return &BackendConn{
		Frontend:   pgproto3.NewFrontend(client, client),
		Conn:       client,
		Statements: map[string]bool{},
	}
}

func TestMaxConnectionsReadsValue(t *testing.T) {
	c := fakeBackend(t, func(b *pgproto3.Backend) {
		b.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("max_connections")}}})
		b.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("250")}})
		b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SHOW")})
		b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		_ = b.Flush()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	got, err := MaxConnections(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != 250 {
		t.Fatalf("MaxConnections = %d, want 250", got)
	}
	if c.Broken {
		t.Fatal("connection marked broken after a clean exchange")
	}
}

func TestMaxConnectionsReturnsSQLState(t *testing.T) {
	c := fakeBackend(t, func(b *pgproto3.Backend) {
		b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "42501", Message: "permission denied"})
		b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		_ = b.Flush()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := MaxConnections(ctx, c)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("err = %v, want a PgError with SQLSTATE 42501", err)
	}
	if c.Broken {
		t.Fatal("an ErrorResponse followed by ReadyForQuery leaves the connection usable")
	}
}

func TestMaxConnectionsMarksBrokenOnIOError(t *testing.T) {
	c := fakeBackend(t, func(b *pgproto3.Backend) {})
	_ = c.Conn.Close()
	if _, err := MaxConnections(t.Context(), c); err == nil {
		t.Fatal("expected an error from a closed connection")
	}
	if !c.Broken {
		t.Fatal("connection should be marked broken")
	}
}

func TestBackendConnCloseIsIdempotent(t *testing.T) {
	c := fakeBackend(t, func(b *pgproto3.Backend) {})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
}
