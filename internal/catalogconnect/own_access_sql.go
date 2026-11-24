package catalogconnect

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// adminTimeout bounds one connection that makes or removes a database or a
// user: longer than a check, since creating a database copies a template.
const adminTimeout = 2 * time.Minute

// postgresAdmin is a Postgres connection that makes databases and roles.
type postgresAdmin struct{ conn *pgx.Conn }

func openPostgresAdmin(ctx context.Context, dsn string, dial DialFunc) (sqlAdmin, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err == nil {
		err = pinConfig(cfg, dsn)
	}
	if err != nil {
		return nil, errors.New("the stored DSN can't be read as a postgres URL")
	}
	cfg.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	cfg.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		return dial(ctx, network, address)
	}
	cfg.ConnectTimeout = probeTimeout
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, pgFailure(ctx, err, nil)
	}
	return postgresAdmin{conn}, nil
}

// pgIdent quotes a name for Postgres.
func pgIdent(name string) string { return pgx.Identifier{name}.Sanitize() }

// pgLiteral quotes a string for Postgres, for the statements that take no
// parameters, such as CREATE ROLE.
func pgLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// exec runs a statement that takes no parameters: Postgres takes none in
// CREATE DATABASE, CREATE ROLE or GRANT, so names are quoted into it.
func (a postgresAdmin) exec(ctx context.Context, statement string) error {
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	_, err := a.conn.Exec(ctx, statement)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// The server's message names objects, never a password.
		return errors.New(pgErr.Message)
	}
	return err
}

func (a postgresAdmin) DatabaseExists(ctx context.Context, name string) (bool, error) {
	var exists bool
	err := a.conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists)
	return exists, err
}

func (a postgresAdmin) CreateDatabase(ctx context.Context, name, owner string) error {
	if owner == "" {
		return a.exec(ctx, "CREATE DATABASE "+pgIdent(name))
	}
	// Making a database for a role takes being able to act as it, which
	// the role's creator may grant itself; a superuser already can.
	_ = a.exec(ctx, "GRANT "+pgIdent(owner)+" TO CURRENT_USER") //nolint:errcheck // CREATE DATABASE says what's missing
	if err := a.exec(ctx, "CREATE DATABASE "+pgIdent(name)+" OWNER "+pgIdent(owner)); err != nil {
		return err
	}
	// Its own database is the service's alone.
	return a.exec(ctx, "REVOKE CONNECT, TEMPORARY ON DATABASE "+pgIdent(name)+" FROM PUBLIC")
}

func (a postgresAdmin) EnsureUser(ctx context.Context, user, password string) error {
	var exists bool
	if err := a.conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", user).Scan(&exists); err != nil {
		return err
	}
	verb := "CREATE"
	if exists {
		verb = "ALTER"
	}
	return a.exec(ctx, verb+" ROLE "+pgIdent(user)+" LOGIN PASSWORD "+pgLiteral(password))
}

func (a postgresAdmin) GrantDatabase(ctx context.Context, database, user string) error {
	return a.exec(ctx, "GRANT CONNECT, CREATE, TEMPORARY ON DATABASE "+pgIdent(database)+" TO "+pgIdent(user))
}

func (a postgresAdmin) LockUser(ctx context.Context, user string) error {
	var exists bool
	if err := a.conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", user).Scan(&exists); err != nil || !exists {
		return err
	}
	return a.exec(ctx, "ALTER ROLE "+pgIdent(user)+" NOLOGIN")
}

func (a postgresAdmin) DropDatabase(ctx context.Context, name string) error {
	return a.exec(ctx, "DROP DATABASE IF EXISTS "+pgIdent(name))
}

func (a postgresAdmin) DropUser(ctx context.Context, user, database string) error {
	var exists bool
	if err := a.conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", user).Scan(&exists); err != nil || !exists {
		return err
	}
	if ok, err := a.DatabaseExists(ctx, database); err != nil {
		return err
	} else if ok {
		if err := a.exec(ctx, "REVOKE ALL ON DATABASE "+pgIdent(database)+" FROM "+pgIdent(user)); err != nil {
			return err
		}
	}
	return a.exec(ctx, "DROP ROLE "+pgIdent(user))
}

func (a postgresAdmin) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	return a.conn.Close(ctx)
}

// mysqlConfig is the driver's configuration for a registered MySQL URL,
// connecting only through dial.
func mysqlConfig(dsn string, dial DialFunc) (*mysql.Config, error) {
	e := &Endpoint{Engine: EngineMySQL}
	if err := parseMySQLDSN(e, dsn); err != nil {
		return nil, err
	}
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil {
		return nil, errors.New("the stored MySQL URL cannot be read")
	}
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
	return cfg, nil
}

// mysqlAdmin is a MySQL connection that makes databases and users.
type mysqlAdmin struct{ db *sql.DB }

func openMySQLAdmin(ctx context.Context, dsn string, dial DialFunc) (sqlAdmin, error) {
	cfg, err := mysqlConfig(dsn, dial)
	if err != nil {
		return nil, err
	}
	// CREATE USER takes no server-side parameters; the driver fills them
	// in, quoted, before it sends the statement.
	cfg.InterpolateParams = true
	cfg.ReadTimeout, cfg.WriteTimeout = adminTimeout, adminTimeout
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, errors.New("the MySQL connection settings cannot be used")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, mysqlFailure(err)
	}
	return mysqlAdmin{db}, nil
}

// mysqlFailure is the server's message, which names objects, never a
// password, or that the connection failed.
func mysqlFailure(err error) error {
	var server *mysql.MySQLError
	if errors.As(err, &server) {
		return errors.New(server.Message)
	}
	return unreachable(errors.New("the MySQL connection or TLS handshake failed"))
}

// myIdent quotes a name Loomplane made for MySQL. Names are letters,
// digits and underscores (ownNameRE), so backticks are all it needs.
func myIdent(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }

func (a mysqlAdmin) exec(ctx context.Context, query string, args ...any) error {
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	if _, err := a.db.ExecContext(ctx, query, args...); err != nil {
		return mysqlFailure(err)
	}
	return nil
}

func (a mysqlAdmin) DatabaseExists(ctx context.Context, name string) (bool, error) {
	var n int
	if err := a.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?", name).Scan(&n); err != nil {
		return false, mysqlFailure(err)
	}
	return n > 0, nil
}

func (a mysqlAdmin) CreateDatabase(ctx context.Context, name, _ string) error {
	// MySQL databases have no owner: GrantDatabase gives the user it.
	return a.exec(ctx, "CREATE DATABASE IF NOT EXISTS "+myIdent(name))
}

func (a mysqlAdmin) EnsureUser(ctx context.Context, user, password string) error {
	if err := a.exec(ctx, "CREATE USER IF NOT EXISTS ?@'%' IDENTIFIED BY ?", user, password); err != nil {
		return err
	}
	return a.exec(ctx, "ALTER USER ?@'%' IDENTIFIED BY ? ACCOUNT UNLOCK", user, password)
}

func (a mysqlAdmin) GrantDatabase(ctx context.Context, database, user string) error {
	return a.exec(ctx, "GRANT ALL PRIVILEGES ON "+myIdent(database)+".* TO ?@'%'", user)
}

func (a mysqlAdmin) LockUser(ctx context.Context, user string) error {
	return a.exec(ctx, "ALTER USER IF EXISTS ?@'%' ACCOUNT LOCK", user)
}

func (a mysqlAdmin) DropDatabase(ctx context.Context, name string) error {
	return a.exec(ctx, "DROP DATABASE IF EXISTS "+myIdent(name))
}

func (a mysqlAdmin) DropUser(ctx context.Context, user, _ string) error {
	// Dropping a MySQL user takes its grants with it.
	return a.exec(ctx, "DROP USER IF EXISTS ?@'%'", user)
}

func (a mysqlAdmin) Close() error { return a.db.Close() }

// mysqlCapabilities reads what SHOW GRANTS says the connection's user may
// do across the server: create databases (CREATE, or ALL PRIVILEGES, on
// *.*) and make users it can give a database to (CREATE USER, or ALL
// PRIVILEGES, on *.*, WITH GRANT OPTION). Rights through roles aren't
// listed there, so they count as missing.
func mysqlCapabilities(grants []string) Capabilities {
	var out Capabilities
	createUser, grantOption := false, false
	for _, line := range grants {
		upper := strings.ToUpper(line)
		privs, rest, ok := strings.Cut(strings.TrimPrefix(upper, "GRANT "), " ON ")
		if !ok || !strings.HasPrefix(strings.TrimSpace(rest), "*.* TO ") {
			continue
		}
		if strings.Contains(rest, "WITH GRANT OPTION") {
			grantOption = true
		}
		for p := range strings.SplitSeq(privs, ",") {
			switch strings.TrimSpace(p) {
			case "ALL", "ALL PRIVILEGES":
				out.CreateDatabase, createUser = true, true
			case "CREATE":
				out.CreateDatabase = true
			case "CREATE USER":
				createUser = true
			}
		}
	}
	out.CreateUser = createUser && grantOption
	return out
}

// showGrants is SHOW GRANTS for the connection's user.
func showGrants(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SHOW GRANTS")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // read-only
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, err
		}
		out = append(out, line)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the grants: %w", err)
	}
	return out, nil
}
