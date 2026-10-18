// Package postgres keeps native database operations independent of runtime transport.
package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/backupfiles"
)

const LabelDatabase = "loomplane.postgres.database"
const LabelRecovery = "loomplane.postgres.recovery"

var databaseIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

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
	Run      func(context.Context, []string, io.Writer) error
	Upload   func(context.Context, string, int64, func(io.Writer) error) error
}

func (d Database) validate() error {
	if !ValidDatabase(d.Name) {
		return errors.New("invalid PostgreSQL database identity")
	}
	return nil
}
func (d Database) cleanup(ctx context.Context, names ...string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	args := make([]string, 2, 2+len(names))
	args[0], args[1] = "rm", "-f"
	for _, name := range names {
		args = append(args, "/tmp/"+name)
	}
	return d.Run(ctx, args, io.Discard)
}
func (d Database) text(ctx context.Context, name, value string) error {
	return d.Upload(ctx, name, int64(len(value)), func(w io.Writer) error { _, err := io.Copy(w, strings.NewReader(value)); return err })
}
func (d Database) Export(ctx context.Context, files *backupfiles.Store, key string) (backupfiles.Object, error) {
	if err := d.validate(); err != nil {
		return backupfiles.Object{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := d.Run(ctx, []string{"pg_dump", "-U", "postgres", "-d", d.Name, "--format=custom", "--no-owner", "--no-acl"}, writer)
		writer.CloseWithError(err)
		done <- err
	}()
	object, err := files.Put(ctx, key, reader)
	reader.CloseWithError(err)
	cancel()
	return object, errors.Join(err, <-done)
}
func (d Database) Restore(ctx context.Context, files *backupfiles.Store, object backupfiles.Object) (err error) {
	if !d.Recovery {
		return errors.New("restore requires an isolated recovery instance")
	}
	if checkErr := d.validate(); checkErr != nil {
		return checkErr
	}
	name := "loomplane-" + rand.Text() + ".dump"
	defer func() { err = errors.Join(err, d.cleanup(ctx, name)) }()
	if copyErr := d.Upload(ctx, name, object.Size, func(w io.Writer) error { return files.CopyVerified(ctx, object, w) }); copyErr != nil {
		return copyErr
	}
	return d.Run(ctx, []string{"pg_restore", "-U", "postgres", "-d", d.Name, "--single-transaction", "--exit-on-error", "--no-owner", "--no-acl", "--role=app", "/tmp/" + name}, io.Discard)
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
