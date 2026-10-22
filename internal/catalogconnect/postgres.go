package catalogconnect

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
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
	if err == nil {
		err = pinConfig(cfg, dsn)
	}
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

// pinConfig sets every connection setting from the DSN alone.
// pgx.ParseConfig fills what a DSN leaves out from Loomplane's own PG*
// environment variables, ~/.pgpass, a service file and the certificates
// under ~/.postgresql. None of that may reach a server someone registered:
// a DSN without a password sends none, and only the DSN's TLS mode applies.
func pinConfig(cfg *pgx.ConnConfig, dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return err
	}
	if u.User == nil || u.User.Username() == "" || u.Hostname() == "" {
		return errors.New("the DSN needs a user and a host")
	}
	port := uint64(5432)
	if p := u.Port(); p != "" {
		if port, err = strconv.ParseUint(p, 10, 16); err != nil || port == 0 {
			return errors.New("the DSN's port is out of range")
		}
	}
	q := u.Query()
	cfg.Host, cfg.Port = u.Hostname(), uint16(port)
	cfg.Database = strings.TrimPrefix(u.Path, "/")
	cfg.User = u.User.Username()
	cfg.Password, _ = u.User.Password()
	cfg.Fallbacks = nil
	cfg.SSLNegotiation = ""
	cfg.KerberosSrvName, cfg.KerberosSpn = "", ""
	cfg.ValidateConnect = nil
	cfg.MinProtocolVersion, cfg.MaxProtocolVersion = "3.0", "3.0"
	cfg.ChannelBinding, cfg.RequireAuth = "prefer", ""
	cfg.RuntimeParams = map[string]string{}
	for _, name := range []string{"application_name", "options", "search_path"} {
		if q.Has(name) {
			cfg.RuntimeParams[name] = q.Get(name)
		}
	}
	switch q.Get("sslmode") {
	case TLSDisable:
		cfg.TLSConfig = nil
	case TLSRequire:
		// Encrypted, certificate unchecked: what sslmode=require means.
		cfg.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // sslmode=require doesn't verify, as in libpq
	case TLSVerifyFull:
		cfg.TLSConfig = &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	default:
		return errors.New("the DSN's sslmode isn't disable, require or verify-full")
	}
	return nil
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
