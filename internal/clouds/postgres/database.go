// Package postgres keeps native database operations independent of runtime transport.
package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/dbexec"
)

const LabelDatabase = "loomplane.postgres.database"
const LabelRecovery = "loomplane.postgres.recovery"

// LabelExtensions lists the extensions a recipe's image ships and the
// restricted app role cannot create. The value is comma separated, so it is
// not a valid Kubernetes label value: keep it out of object metadata and out
// of the runtimes' label copy lists. It only travels in Loomplane's own label map.
const LabelExtensions = "loomplane.postgres.extensions"

var databaseIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
var extensionIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// tocEntry matches a `pg_restore -l` line: dump id, catalog oid, oid, then the
// description, schema and tag. Comment lines start with ";" and never match.
var tocEntry = regexp.MustCompile(`^\d+; \d+ \d+ (.*)$`)

// ParseExtensions reads the LabelExtensions value.
func ParseExtensions(label string) []string {
	var names []string
	for name := range strings.SplitSeq(label, ",") {
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func ValidDatabase(name string) bool { return databaseIdentifier.MatchString(name) }

func ValidatePassword(password string) error {
	if len(password) < 26 || len(password) > 4096 || !utf8.ValidString(password) || strings.ContainsAny(password, "\x00\r\n") {
		return errors.New("PostgreSQL application password is invalid")
	}
	return nil
}

// Backend is the deployment workflow's database contract for any owned target.
type Backend interface {
	BackupPostgres(context.Context, id.ID, map[string]string, *backupfiles.Store, string) (backupfiles.Object, error)
	RestorePostgres(context.Context, id.ID, map[string]string, *backupfiles.Store, backupfiles.Object) error
	ConfigurePostgres(context.Context, id.ID, map[string]string, string) error
	RestoreProbe(context.Context, id.ID, map[string]string) error
	PurgeRecovery(context.Context, id.ID, map[string]string) error
}

// Database receives a transport already bound to a checked, owned runtime.
// Upload must create a regular file in /tmp with mode 0600, never a persistent mount.
type Database struct {
	Name     string
	Recovery bool
	// Extensions are created as postgres before a restore, because the app
	// role cannot create them. timescaledb is the only one needing hooks.
	Extensions []string
	Run        func(context.Context, []string, io.Writer) error
	Upload     func(context.Context, string, int64, func(io.Writer) error) error
}

func (d Database) validate() error {
	if !ValidDatabase(d.Name) {
		return errors.New("invalid PostgreSQL database identity")
	}
	for _, name := range d.Extensions {
		if !extensionIdentifier.MatchString(name) {
			return errors.New("invalid PostgreSQL extension name")
		}
	}
	return nil
}
func (d Database) cleanup(ctx context.Context, names ...string) error {
	return dbexec.Cleanup(ctx, d.Run, names...)
}
func (d Database) text(ctx context.Context, name, value string) error {
	return dbexec.Text(ctx, d.Upload, name, value)
}

func (d Database) Export(ctx context.Context, files *backupfiles.Store, key string) (backupfiles.Object, error) {
	if err := d.validate(); err != nil {
		return backupfiles.Object{}, err
	}
	return dbexec.Export(ctx, files, key, func(ctx context.Context, out io.Writer) error {
		return d.Run(ctx, []string{"pg_dump", "-U", "postgres", "-d", d.Name, "--format=custom", "--no-owner", "--no-acl"}, out)
	})
}

func (d Database) Restore(ctx context.Context, files *backupfiles.Store, object backupfiles.Object) (err error) {
	if !d.Recovery {
		return errors.New("restore requires an isolated recovery instance")
	}
	if checkErr := d.validate(); checkErr != nil {
		return checkErr
	}
	name, list := "loomplane-"+rand.Text()+".dump", ""
	if len(d.Extensions) > 0 {
		list = "loomplane-" + rand.Text() + ".list"
	}
	defer func() {
		// One rm for every file this restore may have left behind.
		err = errors.Join(err, d.cleanup(ctx, slices.DeleteFunc([]string{name, list}, func(n string) bool { return n == "" })...))
	}()
	if copyErr := d.Upload(ctx, name, object.Size, func(w io.Writer) error { return files.CopyVerified(ctx, object, w) }); copyErr != nil {
		return copyErr
	}
	if len(d.Extensions) == 0 {
		return d.Run(ctx, []string{"pg_restore", "-U", "postgres", "-d", d.Name, "--single-transaction", "--exit-on-error", "--no-owner", "--no-acl", "--role=app", "/tmp/" + name}, io.Discard)
	}
	return d.restoreWithExtensions(ctx, name, list)
}

// psql runs one statement as postgres against the database.
func (d Database) psql(ctx context.Context, statement string) error {
	return d.Run(ctx, []string{"psql", "-U", "postgres", "-d", d.Name, "-v", "ON_ERROR_STOP=1", "-c", statement}, io.Discard)
}

// restoreWithExtensions restores a dump whose database uses extensions the
// app role cannot create. The extensions' own tables and sequences (the
// TimescaleDB catalog, PostGIS spatial_ref_sys) belong to postgres, so app
// cannot restore their data. The restore therefore runs as postgres and
// does not drop ownership: pg_restore creates each object as postgres and
// then runs ALTER ... OWNER TO the owner the archive records (pg_dump's
// --no-owner is ignored for archives). Setting the session to each owner
// instead would make a chunk's creation run as app, which has no CREATE on
// _timescaledb_internal. An owner the database does not have is created
// first as a role that cannot log in.
func (d Database) restoreWithExtensions(ctx context.Context, dump, list string) (err error) {
	var toc bytes.Buffer
	if listErr := d.Run(ctx, []string{"pg_restore", "-l", "/tmp/" + dump}, &toc); listErr != nil {
		return listErr
	}
	kept := withoutExtensionEntries(toc.String(), d.Extensions)
	owners, ownersErr := tocOwners(kept)
	if ownersErr != nil {
		return ownersErr
	}
	// timescaledb goes last so its hooks run against a database that already
	// has the others.
	timescale := slices.Contains(d.Extensions, "timescaledb")
	for _, extension := range d.Extensions {
		if extension == "timescaledb" {
			continue
		}
		if createErr := d.psql(ctx, "CREATE EXTENSION IF NOT EXISTS "+extension); createErr != nil {
			return createErr
		}
	}
	if timescale {
		if createErr := d.psql(ctx, "CREATE EXTENSION IF NOT EXISTS timescaledb"); createErr != nil {
			return createErr
		}
	}
	if roleErr := d.createOwners(ctx, owners); roleErr != nil {
		return roleErr
	}
	if copyErr := d.text(ctx, list, kept); copyErr != nil {
		return copyErr
	}
	if timescale {
		// post_restore runs even when the restore fails: pre_restore leaves
		// the database in restoring mode until it does.
		defer func() {
			post, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
			defer cancel()
			err = errors.Join(err, d.psql(post, "SELECT timescaledb_post_restore()"))
		}()
		if preErr := d.psql(ctx, "SELECT timescaledb_pre_restore()"); preErr != nil {
			return preErr
		}
	}
	return d.Run(ctx, []string{"pg_restore", "-U", "postgres", "-d", d.Name, "--single-transaction", "--exit-on-error", "--no-acl", "-L", "/tmp/" + list, "/tmp/" + dump}, io.Discard)
}

// createOwners creates each owner the database lacks, as a role that cannot
// log in or do anything but own what the restore creates.
func (d Database) createOwners(ctx context.Context, owners []string) error {
	if len(owners) == 0 {
		return nil
	}
	var existing bytes.Buffer
	if err := d.Run(ctx, []string{"psql", "-U", "postgres", "-d", d.Name, "-At", "-v", "ON_ERROR_STOP=1", "-c", "SELECT rolname FROM pg_roles"}, &existing); err != nil {
		return err
	}
	have := strings.Fields(existing.String())
	for _, owner := range owners {
		if slices.Contains(have, owner) {
			continue
		}
		if err := d.psql(ctx, `CREATE ROLE "`+owner+`" NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION`); err != nil {
			return err
		}
	}
	return nil
}

// tagFields is how many fields the tag of each description with a fixed shape
// takes in a `pg_restore -l` line. Any other description (a COMMENT's tag is
// an object type and its name, a FUNCTION's has its arguments) has a tag of no
// fixed length.
var tagFields = map[string]int{
	"TABLE": 1, "TABLE DATA": 1, "SEQUENCE": 1, "SEQUENCE SET": 1, "SEQUENCE OWNED BY": 1,
	"VIEW": 1, "MATERIALIZED VIEW": 1, "MATERIALIZED VIEW DATA": 1, "FOREIGN TABLE": 1,
	"INDEX": 1, "SCHEMA": 1, "TYPE": 1, "DOMAIN": 1, "EXTENSION": 1, "COLLATION": 1,
	"DEFAULT": 2, "CONSTRAINT": 2, "FK CONSTRAINT": 2, "TRIGGER": 2,
}

// tocOwners returns the distinct owners of a `pg_restore -l` listing, sorted.
// The owner is the last column. An entry with none, such as an extension,
// ends in a space after its tag. The listing prints a name with a space as
// two fields, so for a description whose tag has a fixed length anything
// beyond schema, tag and one owner is part of the owner, and the restore is
// refused. Where the tag has no fixed length the last field is taken, and a
// split name there fails loudly in pg_restore instead. A name that is not a
// plain lower case identifier is refused, so none is ever quoted into SQL
// from a dump.
func tocOwners(toc string) ([]string, error) {
	seen := map[string]bool{}
	for line := range strings.SplitSeq(toc, "\n") {
		line = strings.TrimRight(line, "\r")
		m := tocEntry.FindStringSubmatch(line)
		if len(m) < 2 || strings.HasSuffix(m[1], " ") {
			continue
		}
		fields := strings.Fields(m[1])
		if len(fields) == 0 {
			continue
		}
		owner := fields[len(fields)-1]
		for words := 3; words >= 1; words-- {
			if len(fields) < words {
				continue
			}
			n, fixed := tagFields[strings.Join(fields[:words], " ")]
			if !fixed {
				continue
			}
			// description, schema, tag, owner
			if len(fields) < words+1+n+1 {
				return nil, fmt.Errorf("the backup has an entry with no readable owner: %q", m[1])
			}
			owner = strings.Join(fields[words+1+n:], " ")
			break
		}
		if !extensionIdentifier.MatchString(owner) {
			return nil, fmt.Errorf("the backup has an object owned by %q, which is not a plain lower case role name", owner)
		}
		seen[owner] = true
	}
	return slices.Sorted(maps.Keys(seen)), nil
}

// withoutExtensionEntries drops the EXTENSION entries, and their COMMENT
// entries, of the extensions Restore pre-creates. Every other line is kept,
// including entries for extensions that were not pre-created, so pg_restore
// still tries them.
func withoutExtensionEntries(toc string, created []string) string {
	var kept strings.Builder
	for line := range strings.SplitAfterSeq(toc, "\n") {
		if m := tocEntry.FindStringSubmatch(strings.TrimRight(line, "\r\n")); m != nil {
			fields := strings.Fields(m[1])
			switch {
			// EXTENSION <schema> <name>
			case len(fields) >= 3 && fields[0] == "EXTENSION" && slices.Contains(created, fields[2]):
				continue
			// COMMENT <schema> EXTENSION <name>
			case len(fields) >= 4 && fields[0] == "COMMENT" && fields[2] == "EXTENSION" && slices.Contains(created, fields[3]):
				continue
			}
		}
		kept.WriteString(line)
	}
	return kept.String()
}
func (d Database) Ready(ctx context.Context) error {
	if err := d.validate(); err != nil {
		return err
	}
	return d.Run(ctx, []string{"pg_isready", "-h", "127.0.0.1", "-U", "postgres", "-d", d.Name}, io.Discard)
}
func (d Database) Probe(ctx context.Context) error {
	if err := d.validate(); err != nil {
		return err
	}
	var out bytes.Buffer
	if err := d.Run(ctx, []string{"psql", "-U", "postgres", "-d", d.Name, "-At", "-v", "ON_ERROR_STOP=1", "-c", "SELECT 1"}, &out); err != nil {
		return err
	}
	if out.String() != "1\n" {
		return errors.New("restored PostgreSQL SQL probe failed")
	}
	return nil
}

const appRoleSQL = `SELECT 'CREATE ROLE app LOGIN' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='app') \gexec
SELECT format('ALTER ROLE app WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD %L', :'app_password') \gexec
SELECT format('ALTER DATABASE %I OWNER TO app', :'db_name') \gexec
ALTER SCHEMA public OWNER TO app;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
`

// Configure uses SQL literals encoded as hex and a temporary pgpass file. The
// password never enters command arguments, diagnostic output, or backup bytes.
func (d Database) Configure(ctx context.Context, password string) (err error) {
	if checkErr := d.validate(); checkErr != nil {
		return checkErr
	}
	if checkErr := ValidatePassword(password); checkErr != nil {
		return checkErr
	}
	script, pass := "loomplane-"+rand.Text()+".sql", "loomplane-"+rand.Text()+".pgpass"
	defer func() { err = errors.Join(err, d.cleanup(ctx, script, pass)) }()
	sql := "SELECT convert_from(decode('" + hex.EncodeToString([]byte(password)) + "','hex'),'UTF8') AS app_password, convert_from(decode('" + hex.EncodeToString([]byte(d.Name)) + "','hex'),'UTF8') AS db_name \\gset\n" + appRoleSQL
	if copyErr := d.text(ctx, script, sql); copyErr != nil {
		return copyErr
	}
	if setupErr := d.Run(ctx, []string{"psql", "-U", "postgres", "-d", d.Name, "-v", "ON_ERROR_STOP=1", "-f", "/tmp/" + script}, io.Discard); setupErr != nil {
		return setupErr
	}
	escaped := strings.NewReplacer(`\`, `\\`, `:`, `\:`).Replace(password)
	if copyErr := d.text(ctx, pass, "127.0.0.1:5432:"+d.Name+":app:"+escaped+"\n"); copyErr != nil {
		return copyErr
	}
	var out bytes.Buffer
	if authErr := d.Run(ctx, []string{"env", "PGPASSFILE=/tmp/" + pass, "psql", "-h", "127.0.0.1", "-U", "app", "-d", d.Name, "-At", "-v", "ON_ERROR_STOP=1", "-c", "SELECT current_user"}, &out); authErr != nil {
		return authErr
	}
	if out.String() != "app\n" {
		return errors.New("PostgreSQL application authentication failed")
	}
	return nil
}
