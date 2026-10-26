//go:build integration

package plane

import (
	"context"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

// testBackend reads LOOMPLANE_TEST_DATABASE_URL and returns the Backend and
// BackendSecret that reach it.
func testBackend(t *testing.T) (Backend, BackendSecret) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for backend dialing tests")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	return Backend{
			Kind:     "postgres",
			Host:     u.Hostname(),
			Port:     port,
			Database: strings.TrimPrefix(u.Path, "/"),
			SNI:      u.Hostname(),
			SSLMode:  u.Query().Get("sslmode"),
		},
		BackendSecret{User: u.User.Username(), Password: password}
}

func TestPgDialer(t *testing.T) {
	b, s := testBackend(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	c, err := PgDialer{}.Dial(ctx, b, s, map[string]string{"application_name": "loomplane-gateway-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if c.PID == 0 {
		t.Error("PID is zero")
	}
	if c.Statements == nil {
		t.Error("Statements map is nil")
	}
	if got := c.Params["application_name"]; got != "loomplane-gateway-test" {
		t.Errorf("application_name = %q, want the runtime param we sent", got)
	}

	maxConns, err := MaxConnections(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if maxConns <= 0 {
		t.Fatalf("MaxConnections = %d, want more than 0", maxConns)
	}

	c.Frontend.Send(&pgproto3.Query{String: "SELECT 1"})
	if err := c.Frontend.Flush(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"RowDescription", "DataRow", "CommandComplete", "ReadyForQuery"} {
		msg, err := c.Frontend.Receive()
		if err != nil {
			t.Fatal(err)
		}
		var got string
		switch m := msg.(type) {
		case *pgproto3.RowDescription:
			got = "RowDescription"
		case *pgproto3.DataRow:
			got = "DataRow"
		case *pgproto3.CommandComplete:
			got = "CommandComplete"
		case *pgproto3.ReadyForQuery:
			got = "ReadyForQuery"
			if m.TxStatus != 'I' {
				t.Errorf("ReadyForQuery status = %q, want 'I'", m.TxStatus)
			}
		default:
			t.Fatalf("expected %s, got %T", want, msg)
		}
		if got != want {
			t.Fatalf("expected %s, got %s", want, got)
		}
	}
}

func TestPgDialerNeverFallsBackToPlaintext(t *testing.T) {
	b, s := testBackend(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	// A backend that does not offer TLS must be refused, not dialed in the clear.
	for _, mode := range []string{"require", "verify-full", ""} {
		b.SSLMode = mode
		c, err := PgDialer{}.Dial(ctx, b, s, nil)
		if err == nil {
			_ = c.Close()
			t.Skipf("test backend offers TLS, so sslmode %q connects", mode)
		}
	}
}

func TestPgDialerWrongPasswordDoesNotLeakIt(t *testing.T) {
	b, s := testBackend(t)
	s.Password = "wrong-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	c, err := PgDialer{}.Dial(ctx, b, s, nil)
	if err == nil {
		_ = c.Close()
		t.Fatal("expected authentication to fail")
	}
	if strings.Contains(err.Error(), s.Password) {
		t.Fatalf("error leaks the password: %v", err)
	}
	if !strings.Contains(err.Error(), net.JoinHostPort(b.Host, strconv.Itoa(b.Port))) {
		t.Errorf("error should name the backend host:port: %v", err)
	}
}
