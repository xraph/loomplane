// Package catalogconnect keeps the external endpoints a catalog service can
// connect to instead of a dependency Loomplane provisions: a Postgres or a
// Redis that runs somewhere else. The DSN, password included, lives only in
// Vault; the row holds the parts that aren't secret. Probers check an
// endpoint's reachability, version and extensions through a dialer the
// caller supplies, so a check can run from the deployment target's network.
package catalogconnect

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Engines Loomplane connects.
const (
	EnginePostgres = "postgres"
	EngineRedis    = "redis"
)

// TLS modes an endpoint is reached with.
const (
	TLSDisable    = "disable"
	TLSRequire    = "require"
	TLSVerifyFull = "verify-full"
)

var (
	ErrInvalid  = errors.New("invalid catalog endpoint")
	ErrNotFound = errors.New("catalog endpoint not found")
	ErrInUse    = errors.New("this endpoint is used by a deployed service")
	// ErrNameTaken is a Store's answer to a second endpoint with the same
	// name in an organization. It is an ErrInvalid.
	ErrNameTaken = fmt.Errorf("%w: this organization already has an endpoint with that name", ErrInvalid)
)

// Endpoint is a database or cache outside Loomplane. Its DSN isn't here.
type Endpoint struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Engine    string    `json:"engine"` // "postgres" or "redis"
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	Database  string    `json:"database,omitempty"`
	Username  string    `json:"username,omitempty"`
	TLS       string    `json:"tls"` // "disable", "require" or "verify-full"
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	Revision  int       `json:"revision"`
}

// CreateInput registers an endpoint.
type CreateInput struct {
	Name string `json:"name"`
	// Engine is postgres or redis.
	Engine string `json:"engine"`
	// DSN is a postgres:// or postgresql:// URL with a user and sslmode
	// disable, require or verify-full, or a redis:// or rediss:// URL.
	// It's stored in Vault and never returned.
	DSN string `json:"dsn"`
}

// DSNKey is where an endpoint's DSN is kept in Vault.
func DSNKey(orgID, id string) string {
	return orgID + "/catalog-endpoints/" + id + "/dsn"
}

// address is host:port, the only part of an endpoint its problems name.
func (e *Endpoint) address() string {
	return net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

// postgresParams are the query parameters a Postgres DSN may set. Anything
// else could point the connection somewhere the row doesn't say (host,
// port, dbname) or read files on Loomplane's host (sslrootcert, passfile).
var postgresParams = map[string]bool{
	"sslmode": true, "application_name": true, "connect_timeout": true, "options": true, "search_path": true,
}

// parseDSN reads the parts of a DSN that aren't secret into e. Its errors
// never repeat the DSN.
func parseDSN(e *Endpoint, dsn string) error {
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil || u.Opaque != "" || u.Scheme == "" {
		return fmt.Errorf("%w: the DSN must be a URL, such as %s", ErrInvalid, example(e.Engine))
	}
	switch e.Engine {
	case EnginePostgres:
		if u.Scheme != "postgres" && u.Scheme != "postgresql" {
			return fmt.Errorf("%w: a postgres DSN starts with postgres://, such as %s", ErrInvalid, example(e.Engine))
		}
		e.Port = 5432
	case EngineRedis:
		if u.Scheme != "redis" && u.Scheme != "rediss" {
			return fmt.Errorf("%w: a redis DSN starts with redis:// or rediss://, such as %s", ErrInvalid, example(e.Engine))
		}
		e.Port = 6379
	}
	host := u.Hostname()
	if host == "" || strings.Contains(u.Host, ",") || strings.HasPrefix(host, "/") {
		return fmt.Errorf("%w: the DSN must name one host", ErrInvalid)
	}
	if p := u.Port(); p != "" {
		port, perr := strconv.Atoi(p)
		if perr != nil || port < 1 || port > 65535 {
			return fmt.Errorf("%w: the DSN's port must be between 1 and 65535", ErrInvalid)
		}
		e.Port = port
	}
	e.Host = host
	if u.User != nil {
		e.Username = u.User.Username()
	}
	e.Database = strings.TrimPrefix(u.Path, "/")
	q := u.Query()
	if e.Engine == EngineRedis {
		if len(q) > 0 {
			return fmt.Errorf("%w: a redis DSN takes no query parameters", ErrInvalid)
		}
		if _, derr := strconv.Atoi(e.Database); e.Database != "" && derr != nil {
			return fmt.Errorf("%w: a redis DSN's path is a database number, such as /0", ErrInvalid)
		}
		e.TLS = TLSDisable
		if u.Scheme == "rediss" {
			e.TLS = TLSVerifyFull
		}
		return nil
	}
	if e.Username == "" {
		return fmt.Errorf("%w: a postgres DSN must name its user, such as %s", ErrInvalid, example(e.Engine))
	}
	for name := range q {
		if !postgresParams[name] {
			return fmt.Errorf("%w: a postgres DSN can't set %q; Loomplane takes sslmode, application_name, connect_timeout, options and search_path", ErrInvalid, name)
		}
	}
	switch mode := q.Get("sslmode"); mode {
	case TLSDisable, TLSRequire, TLSVerifyFull:
		e.TLS = mode
	default:
		return fmt.Errorf("%w: add sslmode=disable, require or verify-full to the DSN so Loomplane knows how the endpoint is reached", ErrInvalid)
	}
	return nil
}

func example(engine string) string {
	if engine == EngineRedis {
		return "redis://:password@cache.example.com:6379/0"
	}
	return "postgres://user:password@db.example.com:5432/app?sslmode=require"
}
