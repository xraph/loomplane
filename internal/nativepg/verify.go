// Package nativepg verifies PostgreSQL recovery copies over an authenticated
// provider transport. It does not select a native instance or change a binding.
package nativepg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Spec struct {
	Database       string
	AdminUser      string
	AdminPassword  string `json:"-"`
	Reader         string
	ReaderPassword string `json:"-"`
	Intent         string
	ExpiresAt      time.Time
	MajorVersion   int
}

// Proof reports a real authenticated SQL session, not application integrity.
type Proof struct {
	Database            string
	Reader              string
	ServerVersion       int
	TransactionReadOnly bool
}

// Verifier requires a provider transport and a current scope/lease guard.
type Verifier struct {
	Dial func(context.Context, string, string) (net.Conn, error)
}

var ErrVerification = errors.New("native PostgreSQL recovery could not be verified")
var ErrUnsafeRole = errors.New("native PostgreSQL recovery login has unreviewed privileges or ownership")
var readerName = regexp.MustCompile(`^lp_recovery_[a-f0-9]{32}$`)
var intentName = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validSpec(s Spec) bool {
	now := time.Now()
	return s.MajorVersion == 17 && s.Database != "" && len(s.Database) <= 63 && !strings.ContainsRune(s.Database, 0) && s.AdminUser != "" && s.AdminUser != s.Reader && s.AdminPassword != "" && !strings.ContainsRune(s.AdminPassword, 0) && readerName.MatchString(s.Reader) && intentName.MatchString(s.Intent) && len(s.ReaderPassword) >= 40 && len(s.ReaderPassword) <= 256 && !strings.ContainsRune(s.ReaderPassword, 0) && s.ExpiresAt.After(now) && s.ExpiresAt.Sub(now) <= 24*time.Hour
}

// Verify creates only an owned, expiring recovery login. Replays inspect the
// saved role and authenticate with the kept password without resetting it.
func (v *Verifier) Verify(ctx context.Context, s Spec, check func(context.Context) error) (Proof, error) {
	return v.verify(ctx, s, check, true)
}

// Inspect authenticates and checks the kept login without creating or repairing
// it. Credential disclosure must not restore access removed by an administrator.
func (v *Verifier) Inspect(ctx context.Context, s Spec, check func(context.Context) error) (Proof, error) {
	return v.verify(ctx, s, check, false)
}

func (v *Verifier) verify(ctx context.Context, s Spec, check func(context.Context) error, create bool) (Proof, error) {
	// PostgreSQL stores timestamps in microseconds. Never extend a requested TTL.
	s.ExpiresAt = s.ExpiresAt.UTC().Truncate(time.Microsecond)
	if v == nil || v.Dial == nil || check == nil || !validSpec(s) {
		return Proof{}, ErrVerification
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	guard := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return check(ctx)
	}
	if err := guard(); err != nil {
		return Proof{}, err
	}
	admin, err := v.connect(ctx, s, s.AdminUser, s.AdminPassword, false)
	if err != nil {
		return Proof{}, sqlError("connect to the copied database", err)
	}
	defer closeSQL(admin)
	if _, err = observe(ctx, admin, s.Database, s.AdminUser, s.MajorVersion); err != nil {
		return Proof{}, err
	}
	if checkErr := guard(); checkErr != nil {
		return Proof{}, checkErr
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		return Proof{}, sqlError("begin recovery login transaction", err)
	}
	defer func() {
		finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer stop()
		if rollbackErr := tx.Rollback(finish); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			_ = admin.Close(finish)
		}
	}()
	if _, err = tx.Exec(ctx, `SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1,0))`, s.Reader); err != nil {
		return Proof{}, sqlError("lock the recovery login", err)
	}
	if checkErr := guard(); checkErr != nil {
		return Proof{}, checkErr
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname=$1)`, s.Reader).Scan(&exists); err != nil {
		return Proof{}, sqlError("read the recovery login", err)
	}
	if !exists {
		if !create {
			return Proof{}, ErrVerification
		}
		if createErr := createReader(ctx, tx, s, guard); createErr != nil {
			return Proof{}, createErr
		}
	}
	if inspectErr := inspectReader(ctx, tx, s); inspectErr != nil {
		return Proof{}, inspectErr
	}
	// An uncommitted new role is invisible to other database connections. Its
	// only parent is checked above, so inspect that parent's effective PUBLIC
	// and inherited privileges until the exact reader becomes visible at commit.
	subject := s.Reader
	if !exists {
		subject = "pg_read_all_data"
	}
	if inspectErr := v.inspectOtherDatabases(ctx, tx, s, subject, guard); inspectErr != nil {
		return Proof{}, inspectErr
	}
	if checkErr := guard(); checkErr != nil {
		return Proof{}, checkErr
	}
	if err = tx.Commit(ctx); err != nil {
		return Proof{}, sqlError("commit the recovery login", err)
	}
	if checkErr := guard(); checkErr != nil {
		return Proof{}, checkErr
	}
	reader, err := v.connect(ctx, s, s.Reader, s.ReaderPassword, true)
	if err != nil {
		return Proof{}, sqlError("authenticate the kept recovery login", err)
	}
	defer closeSQL(reader)
	proof, err := observe(ctx, reader, s.Database, s.Reader, s.MajorVersion)
	if err != nil {
		return Proof{}, err
	}
	if !proof.TransactionReadOnly {
		return Proof{}, ErrVerification
	}
	// Recheck after authentication so changed grants cannot be published as the
	// original saved role. The provider guard also rereads its native identity.
	if inspectErr := inspectReader(ctx, admin, s); inspectErr != nil {
		return Proof{}, inspectErr
	}
	if inspectErr := v.inspectOtherDatabases(ctx, admin, s, s.Reader, guard); inspectErr != nil {
		return Proof{}, inspectErr
	}
	if checkErr := guard(); checkErr != nil {
		return Proof{}, checkErr
	}
	return proof, nil
}

func (v *Verifier) connect(ctx context.Context, s Spec, user, password string, readOnly bool) (*pgx.Conn, error) {
	if os.Getenv("PGSERVICE") != "" {
		return nil, ErrVerification
	}
	cfg, err := pgx.ParseConfig("host=127.0.0.1 port=5432 user=loomplane_probe password=unused dbname=postgres sslmode=disable target_session_attrs=any connect_timeout=5")
	if err != nil {
		return nil, err
	}
	// The supplied transport owns TLS and native instance identity. There is no
	// ambient PGHOST/PGOPTIONS or direct TCP fallback around that transport.
	cfg.Database, cfg.User, cfg.Password, cfg.DialFunc = s.Database, user, password, v.Dial
	cfg.Fallbacks = nil
	mode := "off"
	if readOnly {
		mode = "on"
	}
	cfg.RuntimeParams = map[string]string{"application_name": "loomplane-native-recovery", "search_path": "pg_catalog", "statement_timeout": "15000", "lock_timeout": "5000", "idle_in_transaction_session_timeout": "15000", "default_transaction_read_only": mode}
	return pgx.ConnectConfig(ctx, cfg)
}

func closeSQL(conn *pgx.Conn) {
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	_ = conn.Close(ctx)
}

func observe(ctx context.Context, conn *pgx.Conn, database, user string, major int) (Proof, error) {
	var p Proof
	var sessionUser string
	if err := conn.QueryRow(ctx, `SELECT pg_catalog.current_database(),current_user,session_user,pg_catalog.current_setting('server_version_num')::int,pg_catalog.current_setting('transaction_read_only')::boolean`).Scan(&p.Database, &p.Reader, &sessionUser, &p.ServerVersion, &p.TransactionReadOnly); err != nil {
		return Proof{}, sqlError("read the authenticated SQL identity", err)
	}
	if p.Database != database || p.Reader != user || sessionUser != user || p.ServerVersion/10000 != major {
		return Proof{}, ErrVerification
	}
	return p, nil
}

func createReader(ctx context.Context, tx pgx.Tx, s Spec, guard func() error) error {
	if err := guard(); err != nil {
		return err
	}
	// Parameters hold secrets only for this transaction. Dynamic role DDL uses
	// PostgreSQL's identifier/literal quoting and never interpolates a password
	// into the client-visible statement or a durable checkpoint.
	if _, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('loomplane.recovery_reader',$1,true),pg_catalog.set_config('loomplane.recovery_password',$2,true),pg_catalog.set_config('loomplane.recovery_expiry',$3,true),pg_catalog.set_config('loomplane.recovery_intent',$4,true)`, s.Reader, s.ReaderPassword, s.ExpiresAt.UTC().Format(time.RFC3339Nano), "loomplane-native-restore:"+s.Intent); err != nil {
		return sqlError("prepare the recovery login", err)
	}
	if err := guard(); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DO $recovery_reader$ BEGIN
 EXECUTE pg_catalog.format('CREATE ROLE %I LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 2 PASSWORD %L VALID UNTIL %L',pg_catalog.current_setting('loomplane.recovery_reader'),pg_catalog.current_setting('loomplane.recovery_password'),pg_catalog.current_setting('loomplane.recovery_expiry'));
 EXECUTE pg_catalog.format('COMMENT ON ROLE %I IS %L',pg_catalog.current_setting('loomplane.recovery_reader'),pg_catalog.current_setting('loomplane.recovery_intent'));
 EXECUTE pg_catalog.format('ALTER ROLE %I SET default_transaction_read_only=on',pg_catalog.current_setting('loomplane.recovery_reader'));
 EXECUTE pg_catalog.format('GRANT pg_read_all_data TO %I WITH SET FALSE',pg_catalog.current_setting('loomplane.recovery_reader'));
 END $recovery_reader$`); err != nil {
		return sqlError("create the restricted recovery login", err)
	}
	return nil
}

type sqlReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (v *Verifier) inspectOtherDatabases(ctx context.Context, db sqlReader, s Spec, subject string, guard func() error) error {
	var databases []string
	if err := db.QueryRow(ctx, `SELECT COALESCE(array_agg(datname ORDER BY datname),ARRAY[]::name[]) FROM (SELECT datname FROM pg_catalog.pg_database WHERE datallowconn AND datname<>$2 AND pg_catalog.has_database_privilege($1,oid,'CONNECT') ORDER BY datname LIMIT 33) reachable`, s.Reader, s.Database).Scan(&databases); err != nil {
		return sqlError("list reachable recovery databases", err)
	}
	if len(databases) > 32 {
		return ErrVerification
	}
	for _, database := range databases {
		if err := guard(); err != nil {
			return err
		}
		otherSpec := s
		otherSpec.Database = database
		conn, err := v.connect(ctx, otherSpec, s.AdminUser, s.AdminPassword, true)
		if err != nil {
			return sqlError("connect to another reachable recovery database", err)
		}
		_, identityErr := observe(ctx, conn, database, s.AdminUser, s.MajorVersion)
		var unsafe bool
		if identityErr == nil {
			err = conn.QueryRow(ctx, "SELECT "+databaseWritePrivileges, subject).Scan(&unsafe)
		}
		closeSQL(conn)
		if identityErr != nil {
			return identityErr
		}
		if err != nil {
			return sqlError("inspect another reachable recovery database", err)
		}
		if unsafe {
			return ErrUnsafeRole
		}
	}
	return guard()
}

func inspectReader(ctx context.Context, db sqlReader, s Spec) error {
	var safe bool
	var expires *time.Time
	var marker string
	if err := db.QueryRow(ctx, `SELECT rolcanlogin AND rolinherit AND NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolreplication AND NOT rolbypassrls AND rolconnlimit=2 AND COALESCE('default_transaction_read_only=on'=ANY(rolconfig),false),rolvaliduntil,COALESCE(pg_catalog.shobj_description(oid,'pg_authid'),'') FROM pg_catalog.pg_roles WHERE rolname=$1`, s.Reader).Scan(&safe, &expires, &marker); err != nil {
		return sqlError("inspect the saved recovery login", err)
	}
	if !safe || marker != "loomplane-native-restore:"+s.Intent || expires == nil || !expires.Equal(s.ExpiresAt) {
		return ErrUnsafeRole
	}
	var unsafe bool
	if err := db.QueryRow(ctx, readerPrivileges, s.Reader).Scan(&unsafe); err != nil {
		return sqlError("inspect effective recovery privileges", err)
	}
	if unsafe {
		return ErrUnsafeRole
	}
	return nil
}

// PUBLIC grants, inherited roles, object ownership and executable definer
// routines can confer writes even when the login's role attributes are safe.
const readerPrivileges = `WITH reader AS (SELECT oid FROM pg_catalog.pg_roles WHERE rolname=$1)
 SELECT NOT pg_catalog.pg_has_role($1,'pg_read_all_data','USAGE')
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_auth_members m JOIN pg_catalog.pg_roles parent ON parent.oid=m.roleid WHERE m.member=(SELECT oid FROM reader) AND (parent.rolname<>'pg_read_all_data' OR m.admin_option OR NOT m.inherit_option OR m.set_option))
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_roles parent WHERE parent.oid<>(SELECT oid FROM reader) AND pg_catalog.pg_has_role($1,parent.oid,'MEMBER') AND (parent.rolname<>'pg_read_all_data' OR parent.rolsuper OR parent.rolcreatedb OR parent.rolcreaterole OR parent.rolreplication OR parent.rolbypassrls OR parent.rolcanlogin))
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_shdepend WHERE refclassid='pg_catalog.pg_authid'::regclass AND refobjid=(SELECT oid FROM reader) AND deptype='o')
 OR (` + databaseWritePrivileges + `)`

// Audit every permanent namespace, including catalog names. Default read-only
// sessions are user-controlled and cannot compensate for effective write ACLs.
// Large-object creators are invoker functions with PUBLIC execution by default.
// Only pg_settings UPDATE is session-local SET, not a persistent table write.
const databaseWritePrivileges = `pg_catalog.has_database_privilege($1,pg_catalog.current_database(),'CREATE')
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_namespace n WHERE n.oid<>pg_catalog.pg_my_temp_schema() AND NOT pg_catalog.pg_is_other_temp_schema(n.oid) AND pg_catalog.has_schema_privilege($1,n.oid,'CREATE'))
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_class c WHERE CASE WHEN c.relkind IN ('r','p','v','m','f') AND c.relpersistence<>'t' THEN pg_catalog.has_table_privilege($1,c.oid,'INSERT,DELETE,TRUNCATE,REFERENCES,TRIGGER,MAINTAIN') OR pg_catalog.has_any_column_privilege($1,c.oid,'INSERT,REFERENCES') OR c.oid<>'pg_catalog.pg_settings'::regclass AND (pg_catalog.has_table_privilege($1,c.oid,'UPDATE') OR pg_catalog.has_any_column_privilege($1,c.oid,'UPDATE')) ELSE false END)
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_class c WHERE CASE WHEN c.relkind='S' THEN pg_catalog.has_sequence_privilege($1,c.oid,'USAGE,UPDATE') ELSE false END)
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_proc p WHERE p.prosecdef AND pg_catalog.has_function_privilege($1,p.oid,'EXECUTE'))
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='pg_catalog' AND p.proname IN ('lo_create','lo_creat','lo_from_bytea','lo_import','lo_export') AND pg_catalog.has_function_privilege($1,p.oid,'EXECUTE'))
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_parameter_acl p CROSS JOIN LATERAL pg_catalog.aclexplode(p.paracl) acl WHERE (acl.privilege_type='ALTER SYSTEM' OR acl.privilege_type='SET' AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_settings s WHERE s.name=p.parname AND s.context IN ('user','backend'))) AND CASE WHEN acl.grantee=0 THEN true ELSE pg_catalog.pg_has_role($1,acl.grantee,'USAGE') END)
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_largeobject_metadata l CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(l.lomacl,pg_catalog.acldefault('L',l.lomowner))) acl WHERE acl.privilege_type='UPDATE' AND CASE WHEN acl.grantee=0 THEN true ELSE pg_catalog.pg_has_role($1,acl.grantee,'USAGE') END)`

func sqlError(stage string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fmt.Errorf("%w: %s (SQLSTATE %s)", ErrVerification, stage, pgErr.Code)
	}
	return fmt.Errorf("%w: %s", ErrVerification, stage)
}
