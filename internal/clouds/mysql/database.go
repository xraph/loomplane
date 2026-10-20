// Package mysql implements native MySQL and MariaDB logical recovery.
package mysql

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/dbexec"
)

const LabelDatabase = "loomplane.mysql.database"
const LabelEngine = "loomplane.mysql.engine"

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

func ValidDatabase(name string) bool { return identifier.MatchString(name) }
func ValidEngine(engine string) bool { return engine == "mysql" || engine == "mariadb" }
func ValidatePassword(password string) error {
	return dbexec.ValidatePassword(password)
}

// Run and Upload must already be bound to an owned runtime. Upload creates
// regular mode-0600 files in /tmp, outside the persistent database mount.
type Database struct {
	Engine   string
	Name     string
	Recovery bool
	Run      dbexec.Run
	Upload   dbexec.Upload
}

func (d Database) validate() error {
	if !ValidDatabase(d.Name) || !ValidEngine(d.Engine) {
		return errors.New("invalid MySQL/MariaDB database identity")
	}
	return nil
}
func (d Database) client() string {
	if d.Engine == "mariadb" {
		return "mariadb"
	}
	return "mysql"
}

func (d Database) command(app bool, input string, args ...string) []string {
	script := `export MYSQL_PWD="${MYSQL_ROOT_PASSWORD:-${MARIADB_ROOT_PASSWORD:-}}"; exec "$@"`
	if app {
		script = `export MYSQL_PWD="$LOOMPLANE_APP_PASSWORD"; exec "$@"`
	}
	if input != "" {
		script = strings.Replace(script, `exec "$@"`, `input="$1"; shift; exec "$@" < "$input"`, 1)
	}
	command := []string{"sh", "-c", script, "loomplane-db"}
	if input != "" {
		command = append(command, "/tmp/"+input)
	}
	return append(command, args...)
}

func (d Database) query(ctx context.Context, query string) (string, error) {
	var out bytes.Buffer
	err := d.Run(ctx, d.command(false, "", d.client(), "--protocol=tcp", "--host=127.0.0.1", "--user=root", "--batch", "--skip-column-names", "--execute", query), &out)
	return strings.TrimSpace(out.String()), err
}
func (d Database) Ready(ctx context.Context) error {
	if err := d.validate(); err != nil {
		return err
	}
	out, err := d.query(ctx, "SELECT 1")
	if err == nil && out != "1" {
		return errors.New("database SQL readiness probe failed")
	}
	return err
}
func (d Database) Probe(ctx context.Context) error { return d.Ready(ctx) }

func (d Database) Export(ctx context.Context, files *backupfiles.Store, key string) (backupfiles.Object, error) {
	if err := d.validate(); err != nil {
		return backupfiles.Object{}, err
	}
	out, err := d.query(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='"+d.Name+"' AND table_type='BASE TABLE' AND engine <> 'InnoDB'")
	if err != nil {
		return backupfiles.Object{}, err
	}
	if out != "0" {
		return backupfiles.Object{}, errors.New("logical snapshots require InnoDB tables and no concurrent DDL")
	}
	tool := "mysqldump"
	if d.Engine == "mariadb" {
		tool = "mariadb-dump"
	}
	args := []string{tool, "--user=root", "--single-transaction", "--quick", "--skip-lock-tables", "--no-tablespaces", "--routines", "--triggers", "--events", "--hex-blob", "--default-character-set=utf8mb4"}
	if d.Engine == "mysql" {
		args = append(args, "--set-gtid-purged=OFF")
	}
	args = append(args, d.Name)
	return dbexec.Export(ctx, files, key, func(ctx context.Context, out io.Writer) error { return d.Run(ctx, d.command(false, "", args...), out) })
}

func (d Database) Restore(ctx context.Context, files *backupfiles.Store, object backupfiles.Object) (err error) {
	if !d.Recovery {
		return errors.New("restore requires an isolated recovery instance")
	}
	if checkErr := d.validate(); checkErr != nil {
		return checkErr
	}
	name := "loomplane-" + rand.Text() + ".sql"
	defer func() { err = errors.Join(err, dbexec.Cleanup(ctx, d.Run, name)) }()
	if copyErr := d.Upload(ctx, name, object.Size, func(w io.Writer) error { return files.CopyVerified(ctx, object, w) }); copyErr != nil {
		return copyErr
	}
	return d.Run(ctx, d.command(true, name, d.client(), "--protocol=tcp", "--host=127.0.0.1", "--user=app", "--binary-mode", "--local-infile=0", "--default-character-set=utf8mb4", "--database", d.Name), io.Discard)
}

func (d Database) Configure(ctx context.Context, password string) (err error) {
	if checkErr := d.validate(); checkErr != nil {
		return checkErr
	}
	if checkErr := ValidatePassword(password); checkErr != nil {
		return checkErr
	}
	script, pass := "loomplane-"+rand.Text()+".sql", "loomplane-"+rand.Text()+".cnf"
	defer func() { err = errors.Join(err, dbexec.Cleanup(ctx, d.Run, script, pass)) }()
	literal := "'" + strings.ReplaceAll(password, "'", "''") + "'"
	grantName := strings.ReplaceAll(d.Name, "_", `\_`)
	sql := "SET SESSION sql_mode='NO_BACKSLASH_ESCAPES';\nCREATE USER IF NOT EXISTS 'app'@'%';\nALTER USER 'app'@'%' IDENTIFIED BY " + literal + ";\nGRANT ALL PRIVILEGES ON `" + grantName + "`.* TO 'app'@'%';\n"
	if copyErr := dbexec.Text(ctx, d.Upload, script, sql); copyErr != nil {
		return copyErr
	}
	if setupErr := d.Run(ctx, d.command(false, script, d.client(), "--user=root", "--binary-mode", "--local-infile=0"), io.Discard); setupErr != nil {
		return setupErr
	}
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(password)
	if copyErr := dbexec.Text(ctx, d.Upload, pass, "[client]\npassword=\""+escaped+"\"\n"); copyErr != nil {
		return copyErr
	}
	var out bytes.Buffer
	if authErr := d.Run(ctx, []string{d.client(), "--defaults-extra-file=/tmp/" + pass, "--protocol=tcp", "--host=127.0.0.1", "--user=app", "--database", d.Name, "--batch", "--skip-column-names", "--execute", "SELECT CURRENT_USER()"}, &out); authErr != nil {
		return authErr
	}
	if strings.TrimSpace(out.String()) != "app@%" {
		return errors.New("database application authentication failed")
	}
	return nil
}
