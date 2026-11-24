package catalogconnect

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

func (m *memStore) SaveOwnUser(_ context.Context, _ string, u *OwnUser) error {
	if m.users == nil {
		m.users = map[string]OwnUser{}
	}
	key := u.EndpointID + "/" + u.User
	next := *u
	if prev, ok := m.users[key]; ok {
		next.CreatedAt, next.CreatedDatabase = prev.CreatedAt, prev.CreatedDatabase || u.CreatedDatabase
		next.Environments = prev.Environments
		for _, env := range u.Environments {
			if !slices.Contains(next.Environments, env) {
				next.Environments = append(next.Environments, env)
			}
		}
	}
	next.RevokedAt = nil
	m.users[key] = next
	return nil
}

func (m *memStore) ReleaseOwnUser(_ context.Context, _, endpointID, user, environmentID string) (int, error) {
	u, ok := m.users[endpointID+"/"+user]
	if !ok {
		return 0, nil
	}
	u.Environments = slices.DeleteFunc(u.Environments, func(e string) bool { return e == environmentID })
	m.users[endpointID+"/"+user] = u
	return len(u.Environments), nil
}

func (m *memStore) RevokeOwnUser(_ context.Context, _, endpointID, user string, at time.Time) error {
	if u, ok := m.users[endpointID+"/"+user]; ok && len(u.Environments) == 0 {
		u.RevokedAt = &at
		m.users[endpointID+"/"+user] = u
	}
	return nil
}

func (m *memStore) OwnUsers(_ context.Context, _, endpointID string) ([]OwnUser, error) {
	out := []OwnUser{}
	for _, u := range m.users {
		if u.EndpointID == endpointID {
			out = append(out, u)
		}
	}
	slices.SortFunc(out, func(a, b OwnUser) int { return strings.Compare(a.User, b.User) })
	return out, nil
}

func (m *memStore) DeleteOwnUser(_ context.Context, _, endpointID, user string) error {
	delete(m.users, endpointID+"/"+user)
	return nil
}

// fakeServer is a SQL server's databases and users, as the admin
// connections opened to it see them.
type fakeServer struct {
	databases map[string]string // name to owner
	users     map[string]string // name to password
	locked    map[string]bool
	grants    []string
	// dsns are the connection strings each connection was opened with.
	dsns []string
	// refuseCreate refuses CREATE DATABASE.
	refuseCreate bool
}

func newFakeServer() *fakeServer {
	return &fakeServer{databases: map[string]string{"app": ""}, users: map[string]string{}, locked: map[string]bool{}}
}

type fakeAdmin struct{ s *fakeServer }

func (f *fakeServer) open(_ context.Context, _, dsn string, _ DialFunc) (sqlAdmin, error) {
	f.dsns = append(f.dsns, dsn)
	return fakeAdmin{f}, nil
}

func (a fakeAdmin) DatabaseExists(_ context.Context, name string) (bool, error) {
	_, ok := a.s.databases[name]
	return ok, nil
}

func (a fakeAdmin) CreateDatabase(_ context.Context, name, owner string) error {
	if a.s.refuseCreate {
		return errors.New("permission denied to create database")
	}
	a.s.databases[name] = owner
	return nil
}

func (a fakeAdmin) EnsureUser(_ context.Context, user, password string) error {
	a.s.users[user], a.s.locked[user] = password, false
	return nil
}

func (a fakeAdmin) GrantDatabase(_ context.Context, database, user string) error {
	a.s.grants = append(a.s.grants, database+":"+user)
	return nil
}

func (a fakeAdmin) LockUser(_ context.Context, user string) error {
	a.s.locked[user] = true
	return nil
}

func (a fakeAdmin) DropDatabase(_ context.Context, name string) error {
	delete(a.s.databases, name)
	return nil
}

func (a fakeAdmin) DropUser(_ context.Context, user, _ string) error {
	delete(a.s.users, user)
	return nil
}

func (fakeAdmin) Close() error { return nil }

const adminDSN = "postgres://root:rootpw@db.example:5432/postgres?sslmode=require"

func ownAccessService(t *testing.T) (*Service, *memStore, memSecrets, *fakeServer, *Endpoint) {
	t.Helper()
	store, secrets := newMemStore(), memSecrets{}
	svc := NewService(store, secrets, roleAuthz{})
	server := newFakeServer()
	svc.OpenAdmin = server.open
	e := registerOrders(t, svc, RegisterInput{})
	if _, err := svc.SetAdmin(t.Context(), admin, e.ID, adminDSN, &LastCheck{OK: true, Capabilities: &Capabilities{CreateDatabase: true, CreateUser: true}}); err != nil {
		t.Fatal(err)
	}
	return svc, store, secrets, server, e
}

func TestAServiceGetsItsOwnDatabaseWithTheSharedCredentials(t *testing.T) {
	svc, _, _, server, e := ownAccessService(t)
	got, err := svc.GrantOwnAccess(t.Context(), "org", e.ID, OwnAccess{Access: AccessOwnDatabase, Database: "api"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://u:p@db.example:5432/api?sslmode=require" {
		t.Fatalf("url: %s", got)
	}
	if _, ok := server.databases["api"]; !ok || server.dsns[0] != testDSN {
		t.Fatalf("made with %v: %v", server.dsns, server.databases)
	}
	// Again, it's there already; credentials that can't create one find it.
	server.refuseCreate = true
	if _, err = svc.GrantOwnAccess(t.Context(), "org", e.ID, OwnAccess{Access: AccessOwnDatabase, Database: "api"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.GrantOwnAccess(t.Context(), "org", e.ID, OwnAccess{Access: AccessOwnDatabase, Database: "worker"}, nil); err == nil || !strings.Contains(err.Error(), "couldn't create it") {
		t.Fatalf("a database it can't make: %v", err)
	}
}

func TestAServiceGetsAUserOfItsOwn(t *testing.T) {
	svc, store, secrets, server, e := ownAccessService(t)
	access := OwnAccess{Access: AccessOwnUser, Database: "api", User: "lp_shop_api", ProjectID: "project_1", Service: "api", EnvironmentID: "env_production"}
	got, err := svc.GrantOwnAccess(t.Context(), "org", e.ID, access, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if u.User.Username() != "lp_shop_api" || u.Path != "/api" || u.Host != "db.example:5432" || len(password) != 32 || password != server.users["lp_shop_api"] {
		t.Fatalf("url: %s, server %v", got, server.users)
	}
	if server.databases["api"] != "lp_shop_api" || !slices.Contains(server.grants, "api:lp_shop_api") || server.dsns[0] != adminDSN {
		t.Fatalf("server: %+v", server)
	}
	if string(secrets[OwnUserKey("org", e.ID, "lp_shop_api")]) != password {
		t.Fatal("the password wasn't kept")
	}
	// Every release gets the same password.
	again, err := svc.GrantOwnAccess(t.Context(), "org", e.ID, access, nil)
	if err != nil || again != got {
		t.Fatalf("again: %v", err)
	}
	users, _ := store.OwnUsers(t.Context(), "org", e.ID)
	if len(users) != 1 || !users[0].CreatedDatabase || users[0].Service != "api" || users[0].RevokedAt != nil {
		t.Fatalf("recorded: %+v", users)
	}

	// Released, it can't log in but keeps its data; it can't be dropped
	// while it's in use, or without typing its name.
	if err = svc.DropOwnUser(t.Context(), admin, e.ID, "lp_shop_api", "lp_shop_api", nil); !errors.Is(err, ErrOwnUserInUse) {
		t.Fatalf("dropped in use: %v", err)
	}
	// Staging uses the same server, and the same user: production letting
	// go of it leaves it to staging.
	staging := access
	staging.EnvironmentID = "env_staging"
	if _, err = svc.GrantOwnAccess(t.Context(), "org", e.ID, staging, nil); err != nil {
		t.Fatal(err)
	}
	if err = svc.RevokeOwnUser(t.Context(), "org", e.ID, "lp_shop_api", "env_production", nil); err != nil {
		t.Fatal(err)
	}
	if server.locked["lp_shop_api"] {
		t.Fatal("locked a user staging still uses")
	}
	if err = svc.RevokeOwnUser(t.Context(), "org", e.ID, "lp_shop_api", "env_staging", nil); err != nil {
		t.Fatal(err)
	}
	if !server.locked["lp_shop_api"] || server.databases["api"] == "" {
		t.Fatalf("revoked: %+v", server)
	}
	if err = svc.DropOwnUser(t.Context(), admin, e.ID, "lp_shop_api", "api", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("dropped without the name: %v", err)
	}
	if err = svc.DropOwnUser(t.Context(), developer, e.ID, "lp_shop_api", "lp_shop_api", nil); err == nil {
		t.Fatal("a developer dropped it")
	}
	if err = svc.DropOwnUser(t.Context(), admin, e.ID, "lp_shop_api", "lp_shop_api", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := server.users["lp_shop_api"]; ok {
		t.Fatal("the user is still there")
	}
	if _, ok := server.databases["api"]; ok {
		t.Fatal("the database Loomplane made is still there")
	}
	if _, ok := secrets[OwnUserKey("org", e.ID, "lp_shop_api")]; ok {
		t.Fatal("the password stayed in Vault")
	}
}

func TestAUserGivenADatabaseThatWasThereLeavesItWhenDropped(t *testing.T) {
	svc, _, _, server, e := ownAccessService(t)
	access := OwnAccess{Access: AccessOwnUser, Database: "app", User: "lp_shop_api", EnvironmentID: "env"}
	if _, err := svc.GrantOwnAccess(t.Context(), "org", e.ID, access, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeOwnUser(t.Context(), "org", e.ID, "lp_shop_api", "env", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.DropOwnUser(t.Context(), admin, e.ID, "lp_shop_api", "lp_shop_api", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := server.databases["app"]; !ok {
		t.Fatal("dropped a database Loomplane didn't make")
	}
}

func TestOwnAccessIsRefusedWhereItCantWork(t *testing.T) {
	svc, _, _, _, e := ownAccessService(t)
	for name, a := range map[string]OwnAccess{
		"a quoted database": {Access: AccessOwnDatabase, Database: `api"; DROP`},
		"a long user":       {Access: AccessOwnUser, Database: "api", User: strings.Repeat("u", 33)},
		"no mode":           {Database: "api"},
	} {
		if _, err := svc.GrantOwnAccess(t.Context(), "org", e.ID, a, nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Without an admin credential, no user.
	if _, err := svc.ClearAdmin(t.Context(), admin, e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GrantOwnAccess(t.Context(), "org", e.ID, OwnAccess{Access: AccessOwnUser, Database: "api", User: "lp_api"}, nil); err == nil {
		t.Fatal("made a user without an admin credential")
	}
}

func TestWithDatabaseReplacesTheDatabaseOnly(t *testing.T) {
	for _, tc := range []struct{ engine, dsn, database, want string }{
		{EnginePostgres, "postgres://u:p%40x@db:5432/app?sslmode=require", "api", "postgres://u:p%40x@db:5432/api?sslmode=require"},
		{EngineMySQL, "mysql://u:p@db:3306/app?sslmode=require", "api", "mysql://u:p@db:3306/api?sslmode=require"},
		{EngineRedis, "rediss://:p@cache:6380/0", "2", "rediss://:p@cache:6380/2"},
		{EngineMongoDB, "mongodb://u:p@m:27017/app?authSource=admin", "api", "mongodb://u:p@m:27017/api?authSource=admin"},
	} {
		if got, err := WithDatabase(tc.engine, tc.dsn, tc.database); err != nil || got != tc.want {
			t.Errorf("%s: %q %v", tc.engine, got, err)
		}
	}
	if _, err := WithDatabase(EnginePostgres, "not a url", "api"); err == nil || strings.Contains(err.Error(), "not a url") {
		t.Fatalf("a bad connection string: %v", err)
	}
}

func TestMySQLCapabilitiesComeFromItsGrants(t *testing.T) {
	for name, tc := range map[string]struct {
		grants []string
		want   Capabilities
	}{
		"an admin": {[]string{"GRANT ALL PRIVILEGES ON *.* TO `root`@`%` WITH GRANT OPTION"}, Capabilities{CreateDatabase: true, CreateUser: true}},
		"rds master": {[]string{"GRANT SELECT, INSERT, CREATE, DROP, RELOAD, CREATE USER, EVENT, TRIGGER ON *.* TO `admin`@`%` WITH GRANT OPTION"},
			Capabilities{CreateDatabase: true, CreateUser: true}},
		"create, no grant option": {[]string{"GRANT CREATE, CREATE USER ON *.* TO `ops`@`%`"}, Capabilities{CreateDatabase: true}},
		"one database":            {[]string{"GRANT USAGE ON *.* TO `app`@`%`", "GRANT ALL PRIVILEGES ON `app`.* TO `app`@`%`"}, Capabilities{}},
	} {
		if got := mysqlCapabilities(tc.grants); got != tc.want {
			t.Errorf("%s: %+v", name, got)
		}
	}
}
