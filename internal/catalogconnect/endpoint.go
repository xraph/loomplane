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
	"slices"
	"strconv"
	"strings"
	"time"
)

// Engines Loomplane connects.
const (
	EnginePostgres = "postgres"
	EngineMySQL    = "mysql"
	EngineRedis    = "redis"
	EngineMongoDB  = "mongodb"
)

// knownEngine reports whether Loomplane can register and check an endpoint
// of engine.
func knownEngine(engine string) bool {
	switch engine {
	case EnginePostgres, EngineRedis, EngineMySQL, EngineMongoDB, EngineS3, EngineGCS, EngineR2:
		return true
	}
	return IsAPIEngine(engine) || IsEngineAccessEngine(engine)
}

// Where an endpoint came from: a person registered it by its connection
// string, or a provider resource's Connect made it for that resource.
const (
	OriginRegistered = "registered"
	OriginProvider   = "provider"
)

// Capabilities are what an endpoint's credentials may do beyond using it,
// which decide how a service can share it: in a database of its own, or
// as a user of its own.
type Capabilities struct {
	// CreateDatabase is whether the credentials may create a database.
	CreateDatabase bool `json:"create_database"`
	// CreateUser is whether they may create users (roles) and grant them
	// access.
	CreateUser bool `json:"create_user"`
}

// LastCheck is the newest check of a stored endpoint, from the target it
// was checked from.
type LastCheck struct {
	At       time.Time `json:"at"`
	OK       bool      `json:"ok"`
	Version  string    `json:"version,omitempty"`
	Problems []string  `json:"problems,omitempty"`
	Notes    []string  `json:"notes,omitempty"`
	// Target is the connection the check dialed from.
	Target string `json:"target,omitempty"`
	// Capabilities are the stored credentials', and Admin the admin
	// credential's when there is one.
	Capabilities *Capabilities `json:"capabilities,omitempty"`
	Admin        *Capabilities `json:"admin,omitempty"`
}

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

// Endpoint is a database, cache or bucket outside Loomplane. Its credentials are not here.
type Endpoint struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Engine    string    `json:"engine"` // postgres, mysql, redis, s3 or gcs
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	Database  string    `json:"database,omitempty"`
	Username  string    `json:"username,omitempty"`
	TLS       string    `json:"tls"` // "disable", "require" or "verify-full"
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	// Revision moves each time the credentials are replaced.
	Revision int `json:"revision"`

	// Origin is OriginRegistered or OriginProvider. Only registered
	// endpoints are edited, shared and rotated through the workspace.
	Origin string `json:"origin"`
	// Description says what it is for, in a line.
	Description string `json:"description"`
	// EnvironmentLabel is the environment it serves, such as production.
	EnvironmentLabel string `json:"environment_label"`
	// Restricted limits it to Projects. Unrestricted, every project in the
	// organization may use it.
	Restricted bool     `json:"restricted"`
	Projects   []string `json:"projects"`
	// AdminSet is whether an admin credential is kept beside the
	// endpoint's own, for creating databases and users for services.
	AdminSet  bool       `json:"admin_set"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	LastCheck *LastCheck `json:"last_check,omitempty"`
}

// SharedWith reports whether projectID may use the endpoint.
func (e *Endpoint) SharedWith(projectID string) bool {
	return !e.Restricted || slices.Contains(e.Projects, projectID)
}

// sameConnection reports whether a and b are one endpoint reached the same
// way at the same revision. Its name, description, sharing and last check may
// differ: none of them change what a connection to it reaches.
func sameConnection(a, b *Endpoint) bool {
	return a.ID == b.ID && a.OrgID == b.OrgID && a.Engine == b.Engine &&
		a.Host == b.Host && a.Port == b.Port && a.Database == b.Database && a.Username == b.Username &&
		a.TLS == b.TLS && a.CreatedBy == b.CreatedBy && a.CreatedAt.Equal(b.CreatedAt) && a.Revision == b.Revision
}

// Registered reports whether a person registered the endpoint, rather than
// a provider resource's Connect.
func (e *Endpoint) Registered() bool {
	return e.Origin == "" || e.Origin == OriginRegistered
}

// CreateInput registers an endpoint.
type CreateInput struct {
	Name string `json:"name"`
	// Engine is postgres, mysql, redis, s3 or gcs.
	Engine string `json:"engine"`
	// DSN is a postgres:// or postgresql:// URL with a user and sslmode
	// disable, require or verify-full, a mysql:// URL with sslmode=require or
	// verify-full, or a redis:// or rediss:// URL.
	// Bucket engines use the encoded BucketAccess contract instead of a URL.
	// Access material is stored in Vault and never returned.
	DSN string `json:"dsn"`
}

// DSNKey is where an endpoint's DSN is kept in Vault.
func DSNKey(orgID, id string) string {
	return orgID + "/catalog-endpoints/" + id + "/dsn"
}

// AdminDSNKey is where an endpoint's admin credential is kept in Vault.
func AdminDSNKey(orgID, id string) string {
	return orgID + "/catalog-endpoints/" + id + "/admin-dsn"
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
	if IsEngineAccessEngine(e.Engine) {
		return parseEngineEndpoint(e, dsn)
	}
	if IsAPIEngine(e.Engine) {
		return parseAPIEndpoint(e, dsn)
	}
	if e.Engine == EngineS3 || e.Engine == EngineGCS || e.Engine == EngineR2 {
		return parseBucketEndpoint(e, dsn)
	}
	if e.Engine == EngineMySQL {
		return parseMySQLDSN(e, dsn)
	}
	if e.Engine == EngineMongoDB {
		return parseMongoDSN(e, dsn)
	}
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
