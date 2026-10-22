package catalogconnect

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/xraph/loomplane/internal/recipes"
)

// PostgresProber checks a Postgres endpoint: it connects, reads the
// server's version and looks for the required extensions among the ones
// the server can install.
type PostgresProber struct {
	// connect opens the connection; nil connects with pgx. Tests stand in
	// for a server here.
	connect func(ctx context.Context, cfg *pgx.ConnConfig) (pgConn, error)
}

// pgConn is the part of a Postgres connection the prober uses.
type pgConn interface {
	ServerVersion(ctx context.Context) (string, error)
	AvailableExtensions(ctx context.Context, names []string) ([]string, error)
	Close(ctx context.Context) error
}

var _ Prober = PostgresProber{}

// Probe implements Prober.
func (p PostgresProber) Probe(ctx context.Context, dial DialFunc, dsn string, req recipes.Requirement) (*Report, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// pgx's message quotes the DSN, password masked; say less.
		return nil, errors.New("the stored DSN can't be read as a postgres URL")
	}
	// The dialer resolves the host from where it dials, and is the only
	// way out: no lookup here, no fallback to another address.
	var mu sync.Mutex
	var dialErr error
	cfg.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	cfg.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, derr := dial(ctx, network, address)
		if derr != nil {
			mu.Lock()
			if dialErr == nil {
				dialErr = derr
			}
			mu.Unlock()
		}
		return conn, derr
	}
	cfg.ConnectTimeout = probeTimeout
	connect := p.connect
	if connect == nil {
		connect = connectPgx
	}
	conn, err := connect(ctx, cfg)
	if err != nil {
		mu.Lock()
		defer mu.Unlock()
		return nil, pgFailure(ctx, err, dialErr)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }() // a probe only reads

	raw, err := conn.ServerVersion(ctx)
	if err != nil {
		return nil, pgFailure(ctx, err, nil)
	}
	report := &Report{Version: version(raw)}
	if report.Version == "" {
		report.Version = raw
	}
	checkVersion(report, EnginePostgres, req)
	if len(req.Extensions) > 0 {
		available, err := conn.AvailableExtensions(ctx, req.Extensions)
		if err != nil {
			return nil, pgFailure(ctx, err, nil)
		}
		for _, name := range req.Extensions {
			if !slices.Contains(available, name) {
				report.Missing = append(report.Missing, name)
			}
		}
	}
	return report, nil
}

// pgFailure words a failed connection or query from pieces that can't
// hold the DSN: the dialer's error, the server's message, or what pgx
// says below its "failed to connect to `user=... database=...`" prefix.
func pgFailure(ctx context.Context, err, dialErr error) error {
	var pgErr *pgconn.PgError
	var connectErr *pgconn.ConnectError
	switch {
	case dialErr != nil:
		return unreachable(dialErr)
	case errors.As(err, &pgErr):
		return refused(pgErr.Message)
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || pgconn.Timeout(err):
		return errTimedOut
	case errors.As(err, &connectErr) && errors.Unwrap(connectErr) != nil:
		return unreachable(errors.Unwrap(connectErr))
	}
	return unreachable(err)
}

func connectPgx(ctx context.Context, cfg *pgx.ConnConfig) (pgConn, error) {
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return pgxConn{conn}, nil
}

type pgxConn struct{ conn *pgx.Conn }

func (c pgxConn) ServerVersion(ctx context.Context) (string, error) {
	var v string
	err := c.conn.QueryRow(ctx, "SHOW server_version").Scan(&v)
	return v, err
}

func (c pgxConn) AvailableExtensions(ctx context.Context, names []string) ([]string, error) {
	rows, err := c.conn.Query(ctx, "SELECT name FROM pg_available_extensions WHERE name = ANY($1)", names)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (c pgxConn) Close(ctx context.Context) error { return c.conn.Close(ctx) }
