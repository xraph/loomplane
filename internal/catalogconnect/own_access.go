package catalogconnect

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

// A service connected to a workspace service can get in with its
// credentials as registered, in a database of its own with them, or as a
// user of its own with a database of its own. Loomplane makes the database
// and the user when the service deploys, through the target the service
// runs on, and never drops either on its own: a service that stops using
// its user only loses the login. Dropping is a person's call (DropOwnUser).

// Access modes, as a service's wiring names them.
const (
	AccessOwnDatabase = "own_database"
	AccessOwnUser     = "own_user"
)

// OwnAccess is a service's own database, or its own user and database, on a
// workspace service.
type OwnAccess struct {
	// Access is AccessOwnDatabase or AccessOwnUser.
	Access string
	// Database is the service's own database; a Redis database's number.
	Database string
	// User is the user Loomplane makes under AccessOwnUser.
	User string
	// ProjectID and Service say who the user is for, and EnvironmentID
	// which of the project's environments uses it now.
	ProjectID, Service, EnvironmentID string
}

// OwnUser is a user Loomplane made on a workspace service for one project's
// service.
type OwnUser struct {
	EndpointID string `json:"endpoint_id"`
	User       string `json:"user"`
	Database   string `json:"database"`
	// CreatedDatabase is whether Loomplane made the database, rather than
	// giving the user one that was there. Only a database Loomplane made
	// is dropped with its user.
	CreatedDatabase bool   `json:"created_database"`
	ProjectID       string `json:"project_id"`
	Service         string `json:"service"`
	// Environments are the project's environments whose service uses the
	// user now. One server can serve several.
	Environments []string  `json:"environments"`
	CreatedAt    time.Time `json:"created_at"`
	// RevokedAt is when the user last lost its login, because no service
	// uses it any more; nil while one does.
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// OwnUserStore keeps the users Loomplane made. *PgStore is the real one.
type OwnUserStore interface {
	// SaveOwnUser records a user, or marks one in use again, by the
	// environment in u.Environments.
	SaveOwnUser(ctx context.Context, orgID string, u *OwnUser) error
	// ReleaseOwnUser records that an environment no longer uses a user,
	// and returns how many still do.
	ReleaseOwnUser(ctx context.Context, orgID, endpointID, user, environmentID string) (int, error)
	// RevokeOwnUser marks a user that no environment uses as locked out.
	RevokeOwnUser(ctx context.Context, orgID, endpointID, user string, at time.Time) error
	// OwnUsers lists an endpoint's users by name.
	OwnUsers(ctx context.Context, orgID, endpointID string) ([]OwnUser, error)
	// DeleteOwnUser forgets a user.
	DeleteOwnUser(ctx context.Context, orgID, endpointID, user string) error
}

// OwnUserKey is where a user Loomplane made keeps its password in Vault.
func OwnUserKey(orgID, endpointID, user string) string {
	return orgID + "/catalog-endpoints/" + endpointID + "/users/" + user
}

// ownNameRE is a database or user Loomplane makes: names every engine takes
// without quoting, as a manifest's wiring allows them.
var ownNameRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,63}$`)

// sqlAdmin makes and removes databases and users on a SQL server, as the
// credential it was opened with may.
type sqlAdmin interface {
	DatabaseExists(ctx context.Context, name string) (bool, error)
	// CreateDatabase makes a database, owned by owner when it's set.
	CreateDatabase(ctx context.Context, name, owner string) error
	// EnsureUser makes a user that can log in with password, or gives an
	// existing one that password and its login back.
	EnsureUser(ctx context.Context, user, password string) error
	// GrantDatabase gives a user the use of a database it doesn't own.
	GrantDatabase(ctx context.Context, database, user string) error
	// LockUser takes a user's login away. A user that isn't there is
	// already locked out.
	LockUser(ctx context.Context, user string) error
	DropDatabase(ctx context.Context, name string) error
	// DropUser removes a user, taking back what it was granted on
	// database first.
	DropUser(ctx context.Context, user, database string) error
	Close() error
}

// openAdmin connects to a SQL workspace service as dsn, through dial.
func (s *Service) openAdmin(ctx context.Context, engine, dsn string, dial DialFunc) (sqlAdmin, error) {
	if s.OpenAdmin != nil {
		return s.OpenAdmin(ctx, engine, dsn, dial)
	}
	switch engine {
	case EnginePostgres:
		return openPostgresAdmin(ctx, dsn, dial)
	case EngineMySQL:
		return openMySQLAdmin(ctx, dsn, dial)
	}
	return nil, fmt.Errorf("%w: Loomplane makes databases and users on Postgres and MySQL only", ErrInvalid)
}

// GrantOwnAccess makes sure a service's own database, and under
// AccessOwnUser its own user, are on a workspace service, reached through
// dial from the target the service runs on, and returns the connection
// string the service gets. It makes what's missing and drops nothing, so
// every release of the service can call it. It checks no principal: the
// deployment that calls it was authorized for the project.
func (s *Service) GrantOwnAccess(ctx context.Context, orgID, endpointID string, a OwnAccess, dial DialFunc) (string, error) {
	e, dsn, err := s.Resolve(ctx, orgID, endpointID)
	if err != nil {
		return "", err
	}
	if !e.Registered() {
		return "", fmt.Errorf("%w: only a registered workspace service gives a service its own database", ErrInvalid)
	}
	if err := checkOwnAccess(e.Engine, a); err != nil {
		return "", err
	}
	switch {
	case a.Access == AccessOwnDatabase && (e.Engine == EngineRedis || e.Engine == EngineMongoDB):
		// A Redis database is a number, and MongoDB makes a database when
		// it's first written: there's nothing to make.
		return WithDatabase(e.Engine, dsn, a.Database)
	case a.Access == AccessOwnDatabase:
		if err := s.ensureDatabase(ctx, e, dsn, a.Database, dial); err != nil {
			return "", err
		}
		return WithDatabase(e.Engine, dsn, a.Database)
	}
	return s.ensureUser(ctx, e, dsn, a, dial)
}

// checkOwnAccess refuses what an engine can't give.
func checkOwnAccess(engine string, a OwnAccess) error {
	if !ownNameRE.MatchString(a.Database) {
		return fmt.Errorf("%w: %q isn't a database Loomplane can make", ErrInvalid, a.Database)
	}
	switch a.Access {
	case AccessOwnDatabase:
		if engine != EnginePostgres && engine != EngineMySQL && engine != EngineRedis && engine != EngineMongoDB {
			return fmt.Errorf("%w: a %s workspace service has no databases to give a service", ErrInvalid, engine)
		}
	case AccessOwnUser:
		if engine != EnginePostgres && engine != EngineMySQL {
			return fmt.Errorf("%w: Loomplane makes users on Postgres and MySQL only", ErrInvalid)
		}
		if !ownNameRE.MatchString(a.User) || len(a.User) > 32 {
			return fmt.Errorf("%w: %q isn't a user Loomplane can make", ErrInvalid, a.User)
		}
	default:
		return fmt.Errorf("%w: %q isn't an access mode", ErrInvalid, a.Access)
	}
	return nil
}

// ensureDatabase makes a database with the workspace service's own
// credentials, unless it's there.
func (s *Service) ensureDatabase(ctx context.Context, e *Endpoint, dsn, database string, dial DialFunc) error {
	admin, err := s.openAdmin(ctx, e.Engine, dsn, dial)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", e.Name, err)
	}
	defer func() { _ = admin.Close() }() // nothing to flush
	exists, err := admin.DatabaseExists(ctx, database)
	if err != nil {
		return fmt.Errorf("look for the database %s on %s: %w", database, e.Name, err)
	}
	if exists {
		return nil
	}
	if err := admin.CreateDatabase(ctx, database, ""); err != nil {
		return fmt.Errorf("the database %s isn't on %s, and its credentials couldn't create it: %w", database, e.Name, err)
	}
	return nil
}

// ensureUser makes a service's own user and database with the workspace
// service's admin credential, and returns the connection string they make.
// The password is made once and kept in Vault, so every release, and every
// environment that uses the same server, gets the same one.
func (s *Service) ensureUser(ctx context.Context, e *Endpoint, dsn string, a OwnAccess, dial DialFunc) (string, error) {
	store, ok := s.store.(OwnUserStore)
	if !ok {
		return "", errors.New("this endpoint store keeps no users")
	}
	_, adminDSN, err := s.ResolveAdmin(ctx, e.OrgID, e.ID)
	if err != nil {
		return "", err
	}
	password, err := s.ownPassword(ctx, e, a.User)
	if err != nil {
		return "", err
	}
	admin, err := s.openAdmin(ctx, e.Engine, adminDSN, dial)
	if err != nil {
		return "", fmt.Errorf("connect to %s with its admin credential: %w", e.Name, err)
	}
	defer func() { _ = admin.Close() }() // nothing to flush
	exists, err := admin.DatabaseExists(ctx, a.Database)
	if err != nil {
		return "", fmt.Errorf("look for the database %s on %s: %w", a.Database, e.Name, err)
	}
	created := false
	for _, u := range must(store.OwnUsers(ctx, e.OrgID, e.ID)) {
		if u.User == a.User && u.Database == a.Database {
			created = u.CreatedDatabase
		}
	}
	if err := admin.EnsureUser(ctx, a.User, password); err != nil {
		return "", fmt.Errorf("make the user %s on %s: %w", a.User, e.Name, err)
	}
	if !exists {
		if err := admin.CreateDatabase(ctx, a.Database, a.User); err != nil {
			return "", fmt.Errorf("make the database %s on %s: %w", a.Database, e.Name, err)
		}
		created = true
	}
	if err := admin.GrantDatabase(ctx, a.Database, a.User); err != nil {
		return "", fmt.Errorf("give %s the database %s on %s: %w", a.User, a.Database, e.Name, err)
	}
	if err := store.SaveOwnUser(ctx, e.OrgID, &OwnUser{
		EndpointID: e.ID, User: a.User, Database: a.Database, CreatedDatabase: created,
		ProjectID: a.ProjectID, Service: a.Service, Environments: []string{a.EnvironmentID}, CreatedAt: s.now().UTC(),
	}); err != nil {
		return "", err
	}
	return withUser(e.Engine, dsn, a.User, password, a.Database)
}

// must is the list, or none when it couldn't be read: a user it can't see
// is treated as new, which only decides whether its database is dropped
// with it later.
func must(users []OwnUser, err error) []OwnUser {
	if err != nil {
		return nil
	}
	return users
}

// ownPassword is a user's password, made the first time it's asked for.
func (s *Service) ownPassword(ctx context.Context, e *Endpoint, user string) (string, error) {
	key := OwnUserKey(e.OrgID, e.ID, user)
	if kept, err := s.secrets.Get(ctx, key); err == nil && len(kept) > 0 {
		return string(kept), nil
	}
	password := newPassword()
	if err := s.secrets.Put(ctx, key, []byte(password)); err != nil {
		return "", fmt.Errorf("keep %s's password: %w", user, err)
	}
	return password, nil
}

// newPassword is 32 letters and digits: nothing a connection string or a
// SQL literal has to escape.
func newPassword() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// RevokeOwnUser records that one environment's service no longer uses a
// user Loomplane made, and once none does, takes its login away through
// dial. Its database and data stay. It checks no principal: the deployment
// that calls it was authorized.
func (s *Service) RevokeOwnUser(ctx context.Context, orgID, endpointID, user, environmentID string, dial DialFunc) error {
	store, ok := s.store.(OwnUserStore)
	if !ok {
		return errors.New("this endpoint store keeps no users")
	}
	remaining, err := store.ReleaseOwnUser(ctx, orgID, endpointID, user, environmentID)
	if err != nil || remaining > 0 {
		return err
	}
	e, adminDSN, err := s.ResolveAdmin(ctx, orgID, endpointID)
	if err != nil {
		return err
	}
	admin, err := s.openAdmin(ctx, e.Engine, adminDSN, dial)
	if err != nil {
		return fmt.Errorf("connect to %s with its admin credential: %w", e.Name, err)
	}
	defer func() { _ = admin.Close() }() // nothing to flush
	if err := admin.LockUser(ctx, user); err != nil {
		return fmt.Errorf("take %s's login away on %s: %w", user, e.Name, err)
	}
	return store.RevokeOwnUser(ctx, orgID, endpointID, user, s.now().UTC())
}

// ListOwnUsers lists the users Loomplane made on a workspace service. It
// needs InfrastructureRead.
func (s *Service) ListOwnUsers(ctx context.Context, p security.Principal, endpointID string) ([]OwnUser, error) {
	if err := s.authz.Require(ctx, p, security.InfrastructureRead, ""); err != nil {
		return nil, err
	}
	store, ok := s.store.(OwnUserStore)
	if !ok {
		return []OwnUser{}, nil
	}
	if _, err := s.store.Get(ctx, p.OrgID, endpointID); err != nil {
		return nil, err
	}
	return store.OwnUsers(ctx, p.OrgID, endpointID)
}

// ErrOwnUserInUse refuses to drop a user a service still logs in as.
var ErrOwnUserInUse = errors.New("a service still uses this user; disconnect it first")

// DropOwnUser removes a user Loomplane made, once no service uses it, and
// the database Loomplane made for it with all its data, through dial. It
// needs SecretManage, and confirm must repeat the user's name.
func (s *Service) DropOwnUser(ctx context.Context, p security.Principal, endpointID, user, confirm string, dial DialFunc) (err error) {
	if aerr := s.authz.Require(ctx, p, security.SecretManage, ""); aerr != nil {
		return aerr
	}
	if confirm != user {
		return fmt.Errorf("%w: type the user's name, %s, to drop it and its data", ErrInvalid, user)
	}
	store, ok := s.store.(OwnUserStore)
	if !ok {
		return errors.New("this endpoint store keeps no users")
	}
	defer func() {
		s.record(ctx, AuditEvent{Action: AuditDropUser, Principal: p, EndpointID: endpointID, User: user, Outcome: outcomeOf(err)})
	}()
	users, err := store.OwnUsers(ctx, p.OrgID, endpointID)
	if err != nil {
		return err
	}
	var found *OwnUser
	for i := range users {
		if users[i].User == user {
			found = &users[i]
		}
	}
	if found == nil {
		return ErrNotFound
	}
	if found.RevokedAt == nil {
		return ErrOwnUserInUse
	}
	e, adminDSN, err := s.ResolveAdmin(ctx, p.OrgID, endpointID)
	if err != nil {
		return err
	}
	admin, err := s.openAdmin(ctx, e.Engine, adminDSN, dial)
	if err != nil {
		return fmt.Errorf("connect to %s with its admin credential: %w", e.Name, err)
	}
	defer func() { _ = admin.Close() }() // nothing to flush
	if found.CreatedDatabase {
		if err := admin.DropDatabase(ctx, found.Database); err != nil {
			return fmt.Errorf("drop the database %s on %s: %w", found.Database, e.Name, err)
		}
	}
	if err := admin.DropUser(ctx, user, found.Database); err != nil {
		return fmt.Errorf("drop the user %s on %s: %w", user, e.Name, err)
	}
	if err := s.secrets.Delete(ctx, OwnUserKey(p.OrgID, endpointID, user)); err != nil && s.OrphanedDSN != nil {
		s.OrphanedDSN(ctx, OwnUserKey(p.OrgID, endpointID, user), err)
	}
	return store.DeleteOwnUser(ctx, p.OrgID, endpointID, user)
}

// WithDatabase is a connection string with its database replaced: the
// path of a Postgres, MySQL or MongoDB URL, a Redis URL's database number.
// Its errors never repeat the connection string.
func WithDatabase(engine, dsn, database string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("the connection string isn't a URL with a host")
	}
	switch engine {
	case EnginePostgres, EngineMySQL, EngineRedis, EngineMongoDB:
	default:
		return "", fmt.Errorf("a %s connection string has no database to replace", engine)
	}
	u.Path, u.RawPath = "/"+database, ""
	return u.String(), nil
}

// withUser is a connection string as a user of its own: the user, its
// password and its database in place of the registered ones.
func withUser(engine, dsn, user, password, database string) (string, error) {
	out, err := WithDatabase(engine, dsn, database)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(out)
	if err != nil {
		return "", errors.New("the connection string isn't a URL")
	}
	u.User = url.UserPassword(user, password)
	return u.String(), nil
}
