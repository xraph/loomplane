package catalogconnect

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/xraph/loomplane/internal/recipes"
)

func parseMySQLDSN(e *Endpoint, dsn string) error {
	invalid := func() error {
		return fmt.Errorf("%w: use mysql://user:password@host:3306/database?sslmode=verify-full or sslmode=require", ErrInvalid)
	}
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil || u.Scheme != "mysql" || u.Opaque != "" || u.Fragment != "" || u.Hostname() == "" || strings.ContainsAny(u.Host, ",/\\") || u.User == nil || u.User.Username() == "" {
		return invalid()
	}
	e.Host, e.Port, e.Username, e.Database = u.Hostname(), 3306, u.User.Username(), strings.TrimPrefix(u.Path, "/")
	if strings.ContainsAny(e.Database, "/\\\x00") {
		return invalid()
	}
	if p := u.Port(); p != "" {
		port, portErr := strconv.Atoi(p)
		if portErr != nil || port < 1 || port > 65535 {
			return invalid()
		}
		e.Port = port
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return invalid()
	}
	for name, values := range q {
		if len(values) != 1 || (name != "sslmode" && name != "connect_timeout") {
			return invalid()
		}
	}
	switch q.Get("sslmode") {
	case TLSVerifyFull, TLSRequire:
		e.TLS = q.Get("sslmode")
	default:
		return invalid()
	}
	if q.Has("connect_timeout") {
		timeout, err := strconv.Atoi(q.Get("connect_timeout"))
		if err != nil || timeout < 1 || timeout > int(probeTimeout/time.Second) {
			return invalid()
		}
	}
	return nil
}

// MySQLProber authenticates through the target dialer and reads SELECT VERSION().
type MySQLProber struct{}

var _ Prober = MySQLProber{}

// Probe implements Prober. It never changes server data or connection settings.
func (MySQLProber) Probe(ctx context.Context, dial DialFunc, dsn string, req recipes.Requirement) (*Report, error) {
	if dial == nil {
		return nil, errors.New("the MySQL check requires a target dialer")
	}
	e := &Endpoint{Engine: EngineMySQL}
	if err := parseMySQLDSN(e, dsn); err != nil {
		return nil, err
	}
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil {
		return nil, errors.New("the stored MySQL URL cannot be read")
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cfg := mysql.NewConfig()
	cfg.Net, cfg.Addr, cfg.User, cfg.DBName = "tcp", e.address(), e.Username, e.Database
	cfg.Passwd, _ = u.User.Password()
	cfg.DialFunc = dial
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = probeTimeout, probeTimeout, probeTimeout
	cfg.Logger = &mysql.NopLogger{}
	cfg.TLS = &tls.Config{ServerName: e.Host, MinVersion: tls.VersionTLS12}
	if e.TLS == TLSRequire {
		cfg.TLS.InsecureSkipVerify = true
	} // Explicit sslmode=require encrypts without certificate verification, with no plaintext fallback.
	if timeout := u.Query().Get("connect_timeout"); timeout != "" {
		seconds, parseErr := strconv.Atoi(timeout)
		if parseErr != nil {
			return nil, errors.New("the MySQL timeout cannot be read")
		}
		cfg.Timeout = time.Duration(seconds) * time.Second
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, errors.New("the MySQL connection settings cannot be used")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	var raw string
	if err = db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&raw); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errTimedOut
		}
		var server *mysql.MySQLError
		if errors.As(err, &server) {
			return nil, refused("MySQL authentication or the version query was refused")
		}
		return nil, unreachable(errors.New("the MySQL connection or TLS handshake failed"))
	}
	report := &Report{Version: version(raw)}
	if report.Version == "" {
		return nil, refused("MySQL did not report a usable version")
	}
	checkVersion(report, EngineMySQL, req)
	if len(req.Extensions) > 0 || len(req.Preload) > 0 || req.CreateRole {
		report.Problems = append(report.Problems, "the MySQL check does not verify PostgreSQL extensions, preload libraries or role creation")
	}
	return report, nil
}
