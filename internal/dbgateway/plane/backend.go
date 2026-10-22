package plane

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

// ErrWaking means the backend is still waking and cannot take a connection
// yet. Phase 1 dialers never return it.
var ErrWaking = errors.New("backend is waking")

// BackendSecret is the role and password the gateway uses to open its own
// connections to a backend. Neither field is ever logged or put in an error.
type BackendSecret struct{ User, Password string }

// BackendConn is one authenticated connection from the gateway to a backend,
// taken over from pgconn so later code can proxy protocol messages through
// Frontend. Statements records the prepared statement names created on it.
// Broken marks a connection that must not go back into a pool.
type BackendConn struct {
	Frontend   *pgproto3.Frontend
	Conn       net.Conn
	PID        uint32
	Params     map[string]string
	Statements map[string]bool
	Broken     bool
}

// Close closes the underlying connection. Closing twice is not an error.
func (c *BackendConn) Close() error {
	if c == nil || c.Conn == nil {
		return nil
	}
	if err := c.Conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

// Dialer opens connections to a backend. params are sent as startup
// parameters.
type Dialer interface {
	Dial(ctx context.Context, b Backend, s BackendSecret, params map[string]string) (*BackendConn, error)
}

// PgDialer dials a Postgres backend with pgconn and then hijacks the
// connection.
type PgDialer struct{}

var _ Dialer = PgDialer{}

// Dial connects and authenticates to b as s. Errors name the backend as
// host:port and never include the password.
func (PgDialer) Dial(ctx context.Context, b Backend, s BackendSecret, params map[string]string) (*BackendConn, error) {
	addr := net.JoinHostPort(b.Host, strconv.Itoa(b.Port))
	cfg, err := backendConfig(b, s, params)
	if err != nil {
		// ParseConfig errors can echo the connection string, so say nothing
		// about the cause.
		return nil, fmt.Errorf("configure backend %s: invalid connection settings", addr)
	}
	pc, err := pgconn.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to backend %s: %w", addr, err)
	}
	h, err := pc.Hijack()
	if err != nil {
		_ = pc.Close(ctx)
		return nil, fmt.Errorf("take over backend connection %s: %w", addr, err)
	}
	statuses := make(map[string]string, len(h.ParameterStatuses))
	for k, v := range h.ParameterStatuses {
		statuses[k] = v
	}
	return &BackendConn{
		Frontend:   h.Frontend,
		Conn:       h.Conn,
		PID:        h.PID,
		Params:     statuses,
		Statements: map[string]bool{},
	}, nil
}

// backendConfig builds the pgconn.Config for one dial. The connection string
// is parsed with TLS off, and the TLS settings are then set explicitly, so
// pgconn never builds plaintext fallbacks of its own.
func backendConfig(b Backend, s BackendSecret, params map[string]string) (*pgconn.Config, error) {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(s.User, s.Password),
		Host:     net.JoinHostPort(b.Host, strconv.Itoa(b.Port)),
		Path:     "/" + b.Database,
		RawQuery: url.Values{"sslmode": {"disable"}, "passfile": {"/dev/null"}}.Encode(),
	}
	cfg, err := pgconn.ParseConfig(u.String())
	if err != nil {
		return nil, err
	}
	cfg.Fallbacks = nil
	// Replace, not merge: the gateway's own PGOPTIONS or PGAPPNAME must not
	// reach a customer's backend.
	cfg.RuntimeParams = make(map[string]string, len(params))
	for k, v := range params {
		cfg.RuntimeParams[k] = v
	}

	serverName := b.SNI
	if serverName == "" {
		serverName = b.Host
	}
	switch b.SSLMode {
	case "disable":
		cfg.TLSConfig = nil
	case "require":
		// Matches libpq's require: encrypt, but do not verify the
		// certificate. ServerName is still sent so a shared endpoint can pick
		// the right backend by SNI.
		cfg.TLSConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			ServerName:         serverName,
			InsecureSkipVerify: true, //nolint:gosec // libpq "require" does not verify.
		}
	default: // "verify-full" and anything unrecognised.
		cfg.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: serverName,
		}
	}
	return cfg, nil
}

// MaxConnections asks the backend for its max_connections setting. It returns
// with the connection idle at ReadyForQuery. If the backend answers with an
// error, the returned error unwraps to a *pgconn.PgError carrying the SQLSTATE.
func MaxConnections(ctx context.Context, c *BackendConn) (int, error) {
	if dl, ok := ctx.Deadline(); ok {
		_ = c.Conn.SetDeadline(dl) //nolint:errcheck // a dead connection fails the read below
	}
	// Cancelling ctx must unblock a read that is waiting on the backend.
	stop := context.AfterFunc(ctx, func() { _ = c.Conn.SetDeadline(time.Unix(1, 0)) }) //nolint:errcheck // a dead connection is already failing the read
	defer func() {
		if stop() {
			_ = c.Conn.SetDeadline(time.Time{}) //nolint:errcheck // a dead connection is dropped by its owner
		}
	}()

	c.Frontend.Send(&pgproto3.Query{String: "SHOW max_connections"})
	if err := c.Frontend.Flush(); err != nil {
		c.Broken = true
		return 0, fmt.Errorf("ask backend for max_connections: %w", ctxOr(ctx, err))
	}

	var (
		value  string
		gotRow bool
		pgErr  error
	)
	for {
		msg, err := c.Frontend.Receive()
		if err != nil {
			c.Broken = true
			return 0, fmt.Errorf("read max_connections: %w", ctxOr(ctx, err))
		}
		switch m := msg.(type) {
		case *pgproto3.DataRow:
			if len(m.Values) == 1 && m.Values[0] != nil {
				value, gotRow = string(m.Values[0]), true
			}
		case *pgproto3.ErrorResponse:
			pgErr = pgconn.ErrorResponseToPgError(m)
		case *pgproto3.ReadyForQuery:
			if m.TxStatus != 'I' {
				c.Broken = true
				return 0, fmt.Errorf("backend left the connection in transaction state %q", m.TxStatus)
			}
			if pgErr != nil {
				return 0, fmt.Errorf("ask backend for max_connections: %w", pgErr)
			}
			if !gotRow {
				return 0, errors.New("backend returned no value for max_connections")
			}
			n, err := strconv.Atoi(value)
			if err != nil {
				return 0, fmt.Errorf("backend returned max_connections %q: %w", value, err)
			}
			return n, nil
		}
	}
}

// ctxOr prefers the context's error when the context caused the I/O failure.
func ctxOr(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	return err
}

// Reserve is how many of a backend's max connections the gateway leaves for
// everything else: max(5, ceil(10% of max)).
func Reserve(maxConns int) int {
	r := (maxConns + 9) / 10
	if r < 5 {
		return 5
	}
	return r
}

// Budget is one gateway instance's share of the connections a backend can
// take once the reserve is set aside. It is never below 1, and fewer than one
// instance counts as one.
func Budget(maxConns, instances int) int {
	if instances < 1 {
		instances = 1
	}
	b := (maxConns - Reserve(maxConns)) / instances
	if b < 1 {
		return 1
	}
	return b
}
