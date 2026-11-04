//go:build integration

package catalogconnect

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The fixture creates a dedicated database and login for each test.
func instanceProbeDatabase(t *testing.T) (connection *pgx.Conn, resourceDSN, database, principal string) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for instance scope probes")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Fatal("test database URL required")
	}
	admin, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("lp_instance_probe_%d", time.Now().UnixNano())
	role := name + "_role"
	ident := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	if _, err = admin.Exec(t.Context(), "CREATE DATABASE "+ident(name)); err != nil {
		if closeErr := admin.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, dropErr := admin.Exec(cleanup, "DROP DATABASE "+ident(name)+" WITH (FORCE)"); dropErr != nil {
			t.Error("drop instance probe database", dropErr)
		}
		if _, dropErr := admin.Exec(cleanup, "DROP ROLE IF EXISTS "+ident(role)); dropErr != nil {
			t.Error("drop instance probe role", dropErr)
		}
		if closeErr := admin.Close(cleanup); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if _, err = admin.Exec(t.Context(), "CREATE ROLE "+ident(role)+" LOGIN PASSWORD 'synthetic-probe-password' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"); err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := pgx.Connect(t.Context(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if closeErr := db.Close(cleanup); closeErr != nil {
			t.Error(closeErr)
		}
	})
	for _, query := range []string{"REVOKE ALL ON SCHEMA public FROM PUBLIC", "CREATE SCHEMA private AUTHORIZATION " + ident(role), "REVOKE CREATE ON DATABASE " + ident(name) + " FROM PUBLIC"} {
		if _, err = db.Exec(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	u.User = url.UserPassword(role, "synthetic-probe-password")
	return db, u.String(), name, role
}

func TestInstancePostgresDetectsDatabaseCreate(t *testing.T) {
	db, dsn, name, role := instanceProbeDatabase(t)
	dial := (&net.Dialer{}).DialContext
	before, err := probeInstancePostgres(t.Context(), dial, dsn, name, "private", role)
	if err != nil {
		t.Fatal(err)
	}
	if before.CreateSchemas {
		t.Fatal("schema CREATE was confused with database CREATE")
	}
	if _, err = db.Exec(t.Context(), "GRANT CREATE ON DATABASE "+pgx.Identifier{name}.Sanitize()+" TO "+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	after, err := probeInstancePostgres(t.Context(), dial, dsn, name, "private", role)
	if err != nil {
		t.Fatal(err)
	}
	if !after.CreateSchemas {
		t.Fatal("database CREATE escaped the live privilege inventory")
	}
}

func TestInstancePostgresDetectsIndirectObjects(t *testing.T) {
	db, dsn, name, role := instanceProbeDatabase(t)
	for _, query := range []string{"CREATE SCHEMA hidden", "CREATE TABLE hidden.records (value text)", "CREATE VIEW private.indirect AS SELECT * FROM hidden.records", "GRANT SELECT ON private.indirect TO " + pgx.Identifier{role}.Sanitize()} {
		if _, err := db.Exec(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	observed, err := probeInstancePostgres(t.Context(), (&net.Dialer{}).DialContext, dsn, name, "private", role)
	if err != nil {
		t.Fatal(err)
	}
	if !observed.IndirectObjects {
		t.Fatal("cross-schema definer view escaped the live privilege inventory")
	}
}

func TestInstancePostgresDetectsForeignServerAccess(t *testing.T) {
	db, dsn, name, role := instanceProbeDatabase(t)
	for _, query := range []string{"CREATE FOREIGN DATA WRAPPER synthetic NO HANDLER", "CREATE SERVER synthetic FOREIGN DATA WRAPPER synthetic", "GRANT USAGE ON FOREIGN SERVER synthetic TO " + pgx.Identifier{role}.Sanitize()} {
		if _, err := db.Exec(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	observed, err := probeInstancePostgres(t.Context(), (&net.Dialer{}).DialContext, dsn, name, "private", role)
	if err != nil {
		t.Fatal(err)
	}
	if !observed.ForeignServerAccess {
		t.Fatal("foreign server grant escaped the live privilege inventory")
	}
}

func TestInstancePostgresAllowsOwnPrivateView(t *testing.T) {
	db, dsn, name, role := instanceProbeDatabase(t)
	for _, query := range []string{
		"SET ROLE " + pgx.Identifier{role}.Sanitize(),
		"CREATE TABLE private.records (value text)",
		"CREATE VIEW private.visible AS SELECT * FROM private.records",
		"RESET ROLE",
	} {
		if _, err := db.Exec(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	observed, err := probeInstancePostgres(t.Context(), (&net.Dialer{}).DialContext, dsn, name, "private", role)
	if err != nil {
		t.Fatal(err)
	}
	if observed.IndirectObjects || !observed.CanRead || !observed.CanWrite {
		t.Fatal("private role-owned view was rejected")
	}
}

func TestInstancePostgresDetectsObjectGrantsWithoutSchemaUsage(t *testing.T) {
	db, dsn, name, role := instanceProbeDatabase(t)
	if _, err := db.Exec(t.Context(), "CREATE SCHEMA hidden"); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow(t.Context(), "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil {
		t.Fatal(err)
	}
	principal := pgx.Identifier{role}.Sanitize()
	for _, tc := range []struct {
		name       string
		setup      []string
		cleanup    string
		minVersion int
	}{
		{name: "table", setup: []string{"CREATE TABLE hidden.records(value text)", "GRANT SELECT ON hidden.records TO " + principal}, cleanup: "DROP TABLE hidden.records"},
		{name: "column", setup: []string{"CREATE TABLE hidden.records(value text)", "GRANT SELECT(value) ON hidden.records TO " + principal}, cleanup: "DROP TABLE hidden.records"},
		{name: "sequence", setup: []string{"CREATE SEQUENCE hidden.counter", "GRANT USAGE ON SEQUENCE hidden.counter TO " + principal}, cleanup: "DROP SEQUENCE hidden.counter"},
		{name: "function", setup: []string{"CREATE FUNCTION hidden.callable() RETURNS integer LANGUAGE sql AS 'SELECT 1'"}, cleanup: "DROP FUNCTION hidden.callable()"},
		{name: "maintenance", setup: []string{"CREATE TABLE hidden.records(value text)", "GRANT MAINTAIN ON hidden.records TO " + principal}, cleanup: "DROP TABLE hidden.records", minVersion: 170000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if version < tc.minVersion {
				t.Skip("privilege unavailable on this server version")
			}
			for _, query := range tc.setup {
				if _, err := db.Exec(t.Context(), query); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if _, cleanupErr := db.Exec(cleanup, tc.cleanup); cleanupErr != nil {
					t.Error(cleanupErr)
				}
			})
			observed, err := probeInstancePostgres(t.Context(), (&net.Dialer{}).DialContext, dsn, name, "private", role)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(observed.OtherSchemas, "hidden") {
				t.Fatal("object grant escaped namespace inventory")
			}
		})
	}
}

func TestInstancePostgresDetectsForeignTableWithoutServerGrant(t *testing.T) {
	db, dsn, name, role := instanceProbeDatabase(t)
	for _, query := range []string{
		"CREATE FOREIGN DATA WRAPPER synthetic NO HANDLER",
		"CREATE SERVER synthetic FOREIGN DATA WRAPPER synthetic",
		"CREATE FOREIGN TABLE private.remote(value text) SERVER synthetic",
		"GRANT SELECT ON private.remote TO " + pgx.Identifier{role}.Sanitize(),
	} {
		if _, err := db.Exec(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	observed, err := probeInstancePostgres(t.Context(), (&net.Dialer{}).DialContext, dsn, name, "private", role)
	if err != nil {
		t.Fatal(err)
	}
	if !observed.IndirectObjects {
		t.Fatal("foreign table escaped scope inventory")
	}
}

func TestInstancePostgresDetectsCatalogDelegation(t *testing.T) {
	for _, kind := range []string{"inheritance", "trigger", "rule", "foreign-key"} {
		t.Run(kind, func(t *testing.T) {
			db, dsn, name, role := instanceProbeDatabase(t)
			principal := pgx.Identifier{role}.Sanitize()
			statements := []string{"CREATE SCHEMA hidden", "CREATE TABLE private.records(value integer PRIMARY KEY)", "GRANT SELECT,INSERT,UPDATE,DELETE ON private.records TO " + principal}
			switch kind {
			case "inheritance":
				statements = append(statements, "CREATE TABLE hidden.child() INHERITS(private.records)")
			case "trigger":
				statements = append(statements, "CREATE TABLE hidden.audit(value integer)", `CREATE FUNCTION hidden.audit_row() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER AS $$ BEGIN INSERT INTO hidden.audit VALUES(NEW.value); RETURN NEW; END $$`, "REVOKE ALL ON FUNCTION hidden.audit_row() FROM PUBLIC", "CREATE TRIGGER audit_row AFTER INSERT ON private.records FOR EACH ROW EXECUTE FUNCTION hidden.audit_row()")
			case "rule":
				statements = append(statements, "CREATE TABLE hidden.audit(value integer)", "CREATE RULE audit_row AS ON INSERT TO private.records DO ALSO INSERT INTO hidden.audit VALUES(NEW.value)")
			case "foreign-key":
				statements = append(statements, "CREATE TABLE hidden.child(value integer REFERENCES private.records(value) ON DELETE CASCADE)")
			}
			for _, statement := range statements {
				if _, err := db.Exec(t.Context(), statement); err != nil {
					t.Fatal(err)
				}
			}
			observed, err := probeInstancePostgres(t.Context(), (&net.Dialer{}).DialContext, dsn, name, "private", role)
			if err != nil {
				t.Fatal(err)
			}
			if !observed.IndirectObjects {
				t.Fatal("catalog delegation escaped scope inventory")
			}
		})
	}
}

func TestInstancePostgresAllowsPrivateCatalogRelations(t *testing.T) {
	db, dsn, name, role := instanceProbeDatabase(t)
	for _, statement := range []string{
		"SET ROLE " + pgx.Identifier{role}.Sanitize(),
		"CREATE TABLE private.parent(value integer PRIMARY KEY)",
		"CREATE TABLE private.child() INHERITS(private.parent)",
		"CREATE TABLE private.references_parent(value integer REFERENCES private.parent(value) ON DELETE CASCADE)",
		"CREATE TABLE private.audit(value integer)",
		"CREATE RULE local_audit AS ON INSERT TO private.parent DO ALSO INSERT INTO private.audit VALUES(NEW.value)",
		"RESET ROLE",
	} {
		if _, err := db.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	observed, err := probeInstancePostgres(t.Context(), (&net.Dialer{}).DialContext, dsn, name, "private", role)
	if err != nil {
		t.Fatal(err)
	}
	if observed.IndirectObjects {
		t.Fatal("instance-owned private relations were rejected")
	}
}

func TestInstancePostgresDetectsRetainedOwnership(t *testing.T) {
	for _, kind := range []string{"database-owner", "schema-owner", "server-owner", "wrapper-grant"} {
		t.Run(kind, func(t *testing.T) {
			db, dsn, name, role := instanceProbeDatabase(t)
			principal := pgx.Identifier{role}.Sanitize()
			var statements []string
			switch kind {
			case "database-owner":
				statements = []string{"ALTER DATABASE " + pgx.Identifier{name}.Sanitize() + " OWNER TO " + principal, "REVOKE CREATE ON DATABASE " + pgx.Identifier{name}.Sanitize() + " FROM " + principal}
			case "schema-owner":
				statements = []string{"CREATE SCHEMA hidden AUTHORIZATION " + principal, "REVOKE ALL ON SCHEMA hidden FROM " + principal}
			case "server-owner":
				statements = []string{"CREATE FOREIGN DATA WRAPPER synthetic NO HANDLER", "CREATE SERVER synthetic FOREIGN DATA WRAPPER synthetic", "ALTER SERVER synthetic OWNER TO " + principal, "REVOKE ALL ON FOREIGN SERVER synthetic FROM " + principal}
			case "wrapper-grant":
				statements = []string{"CREATE FOREIGN DATA WRAPPER synthetic NO HANDLER", "GRANT USAGE ON FOREIGN DATA WRAPPER synthetic TO " + principal}
			}
			for _, statement := range statements {
				if _, err := db.Exec(t.Context(), statement); err != nil {
					t.Fatal(err)
				}
			}
			observed, err := probeInstancePostgres(t.Context(), (&net.Dialer{}).DialContext, dsn, name, "private", role)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "database-owner":
				if !observed.CreateSchemas {
					t.Fatal("database owner can restore its revoked CREATE grant")
				}
			case "schema-owner":
				if !slices.Contains(observed.OtherSchemas, "hidden") {
					t.Fatal("schema owner can restore its revoked grants")
				}
			default:
				if !observed.ForeignServerAccess {
					t.Fatal("foreign object authority can create or restore access")
				}
			}
		})
	}
}

func TestInstancePostgresDetectsForeignOwnerCascade(t *testing.T) {
	db, dsn, name, role := instanceProbeDatabase(t)
	principal := pgx.Identifier{role}.Sanitize()
	for _, statement := range []string{
		"CREATE SCHEMA hidden",
		"CREATE TABLE hidden.audit(value integer)",
		"CREATE TABLE private.parent(value integer PRIMARY KEY)",
		"ALTER TABLE private.parent OWNER TO " + principal,
		"CREATE TABLE private.child(value integer REFERENCES private.parent(value) ON DELETE CASCADE)",
		"GRANT SELECT,INSERT,UPDATE,DELETE ON private.child TO " + principal,
		`CREATE FUNCTION hidden.audit_delete() RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER AS $$ BEGIN INSERT INTO hidden.audit VALUES(OLD.value); RETURN OLD; END $$`,
		"REVOKE ALL ON FUNCTION hidden.audit_delete() FROM PUBLIC",
		"CREATE TRIGGER audit_delete BEFORE DELETE ON private.child FOR EACH ROW EXECUTE FUNCTION hidden.audit_delete()",
		"INSERT INTO private.parent VALUES(1)",
		"INSERT INTO private.child VALUES(1)",
	} {
		if _, err := db.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	actor, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if closeErr := actor.Close(ctx); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if _, err = actor.Exec(t.Context(), "DELETE FROM private.parent WHERE value=1"); err != nil {
		t.Fatal(err)
	}
	var effects int
	if err = db.QueryRow(t.Context(), "SELECT count(*) FROM hidden.audit").Scan(&effects); err != nil || effects != 1 {
		t.Fatal("cascade fixture did not exercise foreign-owner authority", effects, err)
	}
	observed, err := probeInstancePostgres(t.Context(), (&net.Dialer{}).DialContext, dsn, name, "private", role)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(observed.OtherSchemas, "hidden") || observed.PrivilegedFunctions {
		t.Fatal("cascade fixture unexpectedly granted direct hidden access")
	}
	if !observed.IndirectObjects {
		t.Fatal("foreign-owned same-schema cascade escaped scope inventory")
	}
}
