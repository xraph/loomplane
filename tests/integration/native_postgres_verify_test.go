//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/xraph/loomplane/internal/nativepg"
	"github.com/xraph/loomplane/internal/security"
)

type nativePostgresVerifier interface {
	Verify(context.Context, nativepg.Spec, func(context.Context) error) (nativepg.Proof, error)
}

type nativeSQLFixture struct {
	admin    *pgx.Conn
	config   *pgx.ConnConfig
	spec     nativepg.Spec
	verifier *nativepg.Verifier
}

func nativeSQLFixtureFor(t *testing.T) nativeSQLFixture {
	t.Helper()
	if os.Getenv("LOOMPLANE_TEST_NATIVE_POSTGRES_URL") == "" {
		t.Skip("run tests/acceptance/native-postgres/run.sh for an isolated SQL instance")
	}
	t.Setenv("LOOMPLANE_TEST_DATABASE_URL", os.Getenv("LOOMPLANE_TEST_NATIVE_POSTGRES_URL"))
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	reader := "lp_recovery_" + hex.EncodeToString(random[:])
	// Roles are cluster-wide. Drop only this test's role after its database.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, os.Getenv("LOOMPLANE_TEST_DATABASE_URL"))
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err = conn.Exec(ctx, "DROP ROLE IF EXISTS "+pgx.Identifier{reader}.Sanitize()); err != nil {
			t.Error("drop owned recovery test role", err)
		}
	})
	dsn := freshDatabase(t)
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.ConnectConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	if _, err = admin.Exec(t.Context(), `CREATE TABLE public.recovered_rows(id int PRIMARY KEY,value text NOT NULL); INSERT INTO public.recovered_rows VALUES(7,'retained-after-restore')`); err != nil {
		t.Fatal(err)
	}
	address := net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
	v := &nativepg.Verifier{Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
	}}
	return nativeSQLFixture{admin: admin, config: cfg, verifier: v, spec: nativepg.Spec{Database: cfg.Database, AdminUser: cfg.User, AdminPassword: cfg.Password, Reader: reader, ReaderPassword: rand.Text() + rand.Text(), Intent: hex.EncodeToString(random[:]) + hex.EncodeToString(random[:]), ExpiresAt: time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond), MajorVersion: 17}}
}

func nativeSQLChecker(t *testing.T, v *nativepg.Verifier) nativePostgresVerifier {
	t.Helper()
	checker, ok := any(v).(nativePostgresVerifier)
	if !ok {
		t.Fatal("native PostgreSQL has no actual SQL verification")
	}
	return checker
}

func (f nativeSQLFixture) reader(t *testing.T, password string) *pgx.Conn {
	t.Helper()
	cfg := f.config.Copy()
	cfg.User, cfg.Password = f.spec.Reader, password
	conn, err := pgx.ConnectConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal("connect with kept recovery password", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func TestNativePostgresVerificationCreatesAndReopensRestrictedLogin(t *testing.T) {
	f := nativeSQLFixtureFor(t)
	checker := nativeSQLChecker(t, f.verifier)
	guard := func(context.Context) error { return nil }
	proof, err := checker.Verify(t.Context(), f.spec, guard)
	if err != nil || proof.Database != f.spec.Database || proof.Reader != f.spec.Reader || proof.ServerVersion < 170000 || proof.ServerVersion >= 180000 || !proof.TransactionReadOnly {
		t.Fatal("SQL identity was not verified", proof, err)
	}
	conn := f.reader(t, f.spec.ReaderPassword)
	var value string
	if err = conn.QueryRow(t.Context(), `SELECT value FROM public.recovered_rows WHERE id=7`).Scan(&value); err != nil || value != "retained-after-restore" {
		t.Fatal("recovery login could not read retained data", value, err)
	}
	if _, err = conn.Exec(t.Context(), `SET default_transaction_read_only=off`); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(t.Context(), `UPDATE public.recovered_rows SET value='unexpected-write' WHERE id=7`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatal("session policy concealed an unrestricted login", err)
	}
	for _, statement := range []string{`SELECT pg_catalog.lo_create(0)`, `SELECT pg_catalog.lo_creat(-1)`, `SELECT pg_catalog.lo_from_bytea(0,'\x01'::bytea)`} {
		_, err = conn.Exec(t.Context(), statement)
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatal("verified reader could create a persistent large object", err)
		}
	}
	var before, after string
	if err = f.admin.QueryRow(t.Context(), `SELECT rolpassword FROM pg_authid WHERE rolname=$1`, f.spec.Reader).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = checker.Verify(t.Context(), f.spec, guard); err != nil {
		t.Fatal("kept login could not replay", err)
	}
	if err = f.admin.QueryRow(t.Context(), `SELECT rolpassword FROM pg_authid WHERE rolname=$1`, f.spec.Reader).Scan(&after); err != nil || before != after {
		t.Fatal("verification replay reset the saved password", err)
	}
	wrong := f.spec
	wrong.ReaderPassword = rand.Text() + rand.Text()
	if _, err = checker.Verify(t.Context(), wrong, guard); err == nil {
		t.Fatal("wrong kept password replaced an existing recovery login")
	}
	_ = f.reader(t, f.spec.ReaderPassword)
}

func TestNativePostgresVerificationRejectsInheritedWriteCapabilities(t *testing.T) {
	for _, scenario := range []string{"table-write", "column-write", "schema-create", "pg-prefixed-schema", "system-schema-create", "system-table-write", "sequence-write", "large-object-write", "large-object-create", "large-object-creat", "large-object-from-bytea", "other-database", "other-database-reader", "parameter-write", "parameter-set", "security-definer", "unowned-role", "wrong-version", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			f := nativeSQLFixtureFor(t)
			checker := nativeSQLChecker(t, f.verifier)
			var query string
			switch scenario {
			case "table-write":
				query = `GRANT INSERT ON public.recovered_rows TO PUBLIC`
			case "column-write":
				query = `GRANT UPDATE(value) ON public.recovered_rows TO PUBLIC`
			case "schema-create":
				query = `GRANT CREATE ON SCHEMA public TO PUBLIC`
			case "pg-prefixed-schema":
				query = `CREATE SCHEMA pgbusiness; GRANT CREATE ON SCHEMA pgbusiness TO PUBLIC`
			case "system-schema-create":
				query = `GRANT CREATE ON SCHEMA information_schema TO PUBLIC`
			case "system-table-write":
				query = `CREATE TABLE information_schema.writable_rows(id int); GRANT INSERT ON information_schema.writable_rows TO PUBLIC`
			case "large-object-create", "large-object-creat", "large-object-from-bytea":
				function := map[string]string{"large-object-create": "lo_create(oid)", "large-object-creat": "lo_creat(integer)", "large-object-from-bytea": "lo_from_bytea(oid,bytea)"}[scenario]
				query = "GRANT EXECUTE ON FUNCTION pg_catalog." + function + " TO PUBLIC"
			case "other-database", "other-database-reader":
				if scenario == "other-database-reader" {
					if _, err := checker.Verify(t.Context(), f.spec, func(context.Context) error { return nil }); err != nil {
						t.Fatal(err)
					}
				}
				otherDSN := freshDatabase(t)
				other, err := pgx.Connect(t.Context(), otherDSN)
				if err != nil {
					t.Fatal(err)
				}
				grantee := "PUBLIC"
				if scenario == "other-database-reader" {
					grantee = pgx.Identifier{f.spec.Reader}.Sanitize()
				}
				if _, err = other.Exec(t.Context(), "GRANT CREATE ON SCHEMA public TO "+grantee); err != nil {
					t.Fatal(err)
				}
				_ = other.Close(t.Context())
			case "sequence-write":
				query = `CREATE SEQUENCE public.writable_sequence; GRANT USAGE ON SEQUENCE public.writable_sequence TO PUBLIC`
			case "large-object-write":
				var oid uint32
				if err := f.admin.QueryRow(t.Context(), `SELECT lo_create(0)`).Scan(&oid); err != nil {
					t.Fatal(err)
				}
				query = fmt.Sprintf("GRANT UPDATE ON LARGE OBJECT %d TO PUBLIC", oid)
			case "parameter-write", "parameter-set":
				if _, err := checker.Verify(t.Context(), f.spec, func(context.Context) error { return nil }); err != nil {
					t.Fatal(err)
				}
				privilege, parameter := "ALTER SYSTEM", "statement_timeout"
				if scenario == "parameter-set" {
					privilege, parameter = "SET", "lo_compat_privileges"
				}
				query = "GRANT " + privilege + " ON PARAMETER " + parameter + " TO " + pgx.Identifier{f.spec.Reader}.Sanitize()
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if _, cleanupErr := f.admin.Exec(ctx, "REVOKE "+privilege+" ON PARAMETER "+parameter+" FROM "+pgx.Identifier{f.spec.Reader}.Sanitize()); cleanupErr != nil {
						t.Error(cleanupErr)
					}
				})
			case "security-definer":
				query = `CREATE FUNCTION public.inherited_authority() RETURNS int LANGUAGE sql SECURITY DEFINER AS 'SELECT 1'`
			case "unowned-role":
				query = "CREATE ROLE " + pgx.Identifier{f.spec.Reader}.Sanitize() + " NOLOGIN CREATEROLE"
			case "wrong-version":
				f.spec.MajorVersion = 18
			case "expired":
				f.spec.ExpiresAt = time.Now().Add(-time.Minute)
			}
			if query != "" {
				if _, err := f.admin.Exec(t.Context(), query); err != nil {
					t.Fatal(err)
				}
			}
			proof, verifyErr := checker.Verify(t.Context(), f.spec, func(context.Context) error { return nil })
			wantErr := nativepg.ErrUnsafeRole
			if scenario == "wrong-version" || scenario == "expired" {
				wantErr = nativepg.ErrVerification
			}
			if !errors.Is(verifyErr, wantErr) || proof.Reader != "" {
				t.Fatal("unsafe or mismatched SQL recovery was not refused for its actual cause", proof, verifyErr)
			}
			var exists bool
			if err := f.admin.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, f.spec.Reader).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if scenario != "unowned-role" && scenario != "parameter-write" && scenario != "parameter-set" && scenario != "other-database-reader" && exists {
				t.Fatal("failed verification left an admitted recovery login")
			}
			if scenario == "unowned-role" {
				var canCreate, canLogin bool
				if err := f.admin.QueryRow(t.Context(), `SELECT rolcreaterole,rolcanlogin FROM pg_roles WHERE rolname=$1`, f.spec.Reader).Scan(&canCreate, &canLogin); err != nil || !canCreate || canLogin {
					t.Fatal("verification altered an unrelated role", err)
				}
			}
		})
	}
}

func TestNativePostgresVerificationCanonicalizesExpiry(t *testing.T) {
	f := nativeSQLFixtureFor(t)
	f.spec.ExpiresAt = f.spec.ExpiresAt.Add(789 * time.Nanosecond)
	if _, err := f.verifier.Verify(t.Context(), f.spec, func(context.Context) error { return nil }); err != nil {
		t.Fatal("ordinary nanosecond expiry failed verification", err)
	}
	var saved time.Time
	if err := f.admin.QueryRow(t.Context(), `SELECT rolvaliduntil FROM pg_roles WHERE rolname=$1`, f.spec.Reader).Scan(&saved); err != nil || !saved.Equal(f.spec.ExpiresAt.Truncate(time.Microsecond)) {
		t.Fatal("expiry was not canonicalized without extending it", saved, err)
	}
}

func TestNativePostgresVerificationPreservesRowSecurityAndRejectsChangedRole(t *testing.T) {
	f := nativeSQLFixtureFor(t)
	checker := nativeSQLChecker(t, f.verifier)
	if _, err := f.admin.Exec(t.Context(), `ALTER TABLE public.recovered_rows ENABLE ROW LEVEL SECURITY; ALTER TABLE public.recovered_rows FORCE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	guard := func(context.Context) error { return nil }
	if _, err := checker.Verify(t.Context(), f.spec, guard); err != nil {
		t.Fatal(err)
	}
	conn := f.reader(t, f.spec.ReaderPassword)
	var rows int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.recovered_rows`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("verification bypassed row security", rows, err)
	}
	if _, err := f.admin.Exec(t.Context(), "GRANT pg_write_all_data TO "+pgx.Identifier{f.spec.Reader}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := checker.Verify(t.Context(), f.spec, guard); err == nil {
		t.Fatal("changed recovery role was accepted on replay")
	}
}

func TestNativePostgresVerificationChecksPermissionBeforeConnecting(t *testing.T) {
	f := nativeSQLFixtureFor(t)
	checker := nativeSQLChecker(t, f.verifier)
	proof, err := checker.Verify(t.Context(), f.spec, func(context.Context) error { return security.ErrForbidden })
	if !errors.Is(err, security.ErrForbidden) || proof.Reader != "" {
		t.Fatal("denied verifier admitted SQL access", proof, err)
	}
	var exists bool
	if err = f.admin.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, f.spec.Reader).Scan(&exists); err != nil || exists {
		t.Fatal("denied verifier changed SQL users", exists, err)
	}
}

func TestNativePostgresVerificationIgnoresAmbientSessionOverrides(t *testing.T) {
	f := nativeSQLFixtureFor(t)
	t.Setenv("PGHOST", "unrelated.invalid")
	t.Setenv("PGOPTIONS", "-c default_transaction_read_only=off -c statement_timeout=0")
	t.Setenv("PGTARGETSESSIONATTRS", "read-only")
	proof, err := nativeSQLChecker(t, f.verifier).Verify(t.Context(), f.spec, func(context.Context) error { return nil })
	if err != nil || !proof.TransactionReadOnly || proof.Database != f.spec.Database {
		t.Fatal("ambient settings changed the native verification session", proof, err)
	}
}

func TestNativePostgresVerificationRechecksPermissionAfterWaitingForRoleLock(t *testing.T) {
	f := nativeSQLFixtureFor(t)
	checker := nativeSQLChecker(t, f.verifier)
	lock, err := f.admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if rollbackErr := lock.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			t.Error(rollbackErr)
		}
	}()
	if _, err = lock.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, f.spec.Reader); err != nil {
		t.Fatal(err)
	}
	monitor, err := pgx.ConnectConfig(t.Context(), f.config.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = monitor.Close(context.Background()) }()
	var revoked atomic.Bool
	done := make(chan error, 1)
	go func() {
		_, verifyErr := checker.Verify(t.Context(), f.spec, func(context.Context) error {
			if revoked.Load() {
				return security.ErrForbidden
			}
			return nil
		})
		done <- verifyErr
	}()
	deadline := time.Now().Add(4 * time.Second)
	for {
		var waiting bool
		if err = monitor.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("verification did not wait for the held role lock")
		}
		select {
		case early := <-done:
			t.Fatal("verification stopped before the held role lock", early)
		case <-time.After(20 * time.Millisecond):
		}
	}
	revoked.Store(true)
	if err = lock.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, security.ErrForbidden) {
		t.Fatal("revoked permission was not checked after the wait", err)
	}
	var exists bool
	if err = monitor.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, f.spec.Reader).Scan(&exists); err != nil || exists {
		t.Fatal("revoked verifier committed a login", exists, err)
	}
}

func TestNativePostgresVerificationWorksWithoutSuperuserAccess(t *testing.T) {
	adminName := "lp_verify_admin_" + rand.Text()
	// This role is dropped after the verifier role and its database.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if os.Getenv("LOOMPLANE_TEST_NATIVE_POSTGRES_URL") == "" {
			return
		}
		conn, err := pgx.Connect(ctx, os.Getenv("LOOMPLANE_TEST_NATIVE_POSTGRES_URL"))
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err = conn.Exec(ctx, "DROP ROLE IF EXISTS "+pgx.Identifier{adminName}.Sanitize()); err != nil {
			t.Error("drop owned verification admin", err)
		}
	})
	f := nativeSQLFixtureFor(t)
	password := rand.Text() + rand.Text()
	var statement string
	if err := f.admin.QueryRow(t.Context(), `SELECT format('CREATE ROLE %I LOGIN CREATEROLE NOSUPERUSER PASSWORD %L',$1::text,$2::text)`, adminName, password).Scan(&statement); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(t.Context(), statement); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(t.Context(), "GRANT pg_read_all_data TO "+pgx.Identifier{adminName}.Sanitize()+" WITH ADMIN OPTION"); err != nil {
		t.Fatal(err)
	}
	f.spec.AdminUser, f.spec.AdminPassword = adminName, password
	proof, err := nativeSQLChecker(t, f.verifier).Verify(t.Context(), f.spec, func(context.Context) error { return nil })
	if err != nil || proof.Reader != f.spec.Reader {
		t.Fatal("verifier required superuser-only SQL catalogs", proof, err)
	}
}
