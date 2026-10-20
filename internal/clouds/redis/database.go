// Package redis shares native Redis and Valkey snapshot and recovery operations.
package redis

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"strings"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/dbexec"
)

const LabelEngine = "loomplane.cache.engine"

func ValidEngine(engine string) bool { return engine == "redis" || engine == "valkey" }

type Database struct {
	Engine   string
	Recovery bool
	Run      dbexec.Run
	Upload   dbexec.Upload
	Restart  func(context.Context) error
}

func (d Database) validate() error {
	if !ValidEngine(d.Engine) || d.Run == nil {
		return errors.New("invalid Redis/Valkey database identity")
	}
	return nil
}

func (d Database) command(app bool, passwordFile string, args ...string) []string {
	script := `export REDISCLI_AUTH="$LOOMPLANE_CACHE_ADMIN_PASSWORD"; exec "$@"`
	user := "default"
	if app {
		script, user = `export REDISCLI_AUTH="$LOOMPLANE_APP_PASSWORD"; exec "$@"`, "app"
	}
	if passwordFile != "" {
		script = `export REDISCLI_AUTH="$(cat "$1")"; shift; exec "$@"`
	}
	command := []string{"sh", "-c", script, "loomplane-cache"}
	if passwordFile != "" {
		command = append(command, "/tmp/"+passwordFile)
	}
	command = append(command, d.Engine+"-cli", "--no-auth-warning", "-e", "--raw", "--user", user)
	return append(command, args...)
}

func (d Database) query(ctx context.Context, app bool, file string, args ...string) (string, error) {
	var result bytes.Buffer
	err := d.Run(ctx, d.command(app, file, args...), &result)
	return strings.TrimSpace(result.String()), err
}

func (d Database) Ready(ctx context.Context) error {
	if err := d.validate(); err != nil {
		return err
	}
	result, err := d.query(ctx, false, "", "PING")
	if err == nil && result != "PONG" {
		return errors.New("native cache readiness probe failed")
	}
	return err
}
func (d Database) Probe(ctx context.Context) error {
	if err := d.validate(); err != nil {
		return err
	}
	result, err := d.query(ctx, true, "", "PING")
	if err == nil && result != "PONG" {
		return errors.New("native cache application probe failed")
	}
	return err
}

func (d Database) Configure(ctx context.Context, password string) (err error) {
	if checkErr := d.validate(); checkErr != nil {
		return checkErr
	}
	if checkErr := dbexec.ValidatePassword(password); checkErr != nil {
		return checkErr
	}
	name := "loomplane-cache-password-" + rand.Text()
	defer func() { err = errors.Join(err, dbexec.Cleanup(ctx, d.Run, name)) }()
	if checkErr := dbexec.Text(ctx, d.Upload, name, password); checkErr != nil {
		return checkErr
	}
	result, err := d.query(ctx, true, name, "PING")
	if err == nil && result != "PONG" {
		return errors.New("native cache application authentication failed")
	}
	return err
}

func (d Database) Export(ctx context.Context, files *backupfiles.Store, key string) (backupfiles.Object, error) {
	if err := d.validate(); err != nil {
		return backupfiles.Object{}, err
	}
	return dbexec.Export(ctx, files, key, func(ctx context.Context, out io.Writer) error {
		return d.Run(ctx, d.command(false, "", "--rdb", "-"), out)
	})
}

func (d Database) Restore(ctx context.Context, files *backupfiles.Store, object backupfiles.Object) (err error) {
	if !d.Recovery || d.Restart == nil || d.Upload == nil {
		return errors.New("restore requires an isolated recovery cache")
	}
	if checkErr := d.validate(); checkErr != nil {
		return checkErr
	}
	name := "loomplane-" + rand.Text() + ".rdb"
	defer func() { err = errors.Join(err, dbexec.Cleanup(ctx, d.Run, name)) }()
	if checkErr := d.Upload(ctx, name, object.Size, func(out io.Writer) error { return files.CopyVerified(ctx, object, out) }); checkErr != nil {
		return checkErr
	}
	if checkErr := d.Run(ctx, []string{d.Engine + "-check-rdb", "/tmp/" + name}, io.Discard); checkErr != nil {
		return checkErr
	}
	count, err := d.query(ctx, false, "", "DBSIZE")
	if err != nil || count != "0" {
		return errors.New("recovery cache must be empty before importing an RDB")
	}
	// Copies have no AOF or automatic save rules, so shutdown cannot replace
	// this checked snapshot with the empty in-memory database.
	if checkErr := d.Run(ctx, []string{"cp", "/tmp/" + name, "/data/" + name}, io.Discard); checkErr != nil {
		return checkErr
	}
	if checkErr := d.Run(ctx, []string{"mv", "/data/" + name, "/data/dump.rdb"}, io.Discard); checkErr != nil {
		return checkErr
	}
	return d.Restart(ctx)
}
