// Package falkordb transfers the engine's native module-aware RDB snapshots.
package falkordb

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/dbexec"
)

type Database struct {
	Run     dbexec.Run
	Upload  dbexec.Upload
	Restart func(context.Context) error
}

func command(args ...string) []string {
	return append([]string{"sh", "-c", `export REDISCLI_AUTH="$(cat /etc/loomplane/password)"; exec redis-cli --no-auth-warning -e --raw "$@"`, "loomplane-graph"}, args...)
}
func (d Database) Export(ctx context.Context, dst io.Writer) error {
	return d.Run(ctx, command("--rdb", "-"), dst)
}
func (d Database) Restore(ctx context.Context, payload *os.File) (err error) {
	var count bytes.Buffer
	if checkErr := d.Run(ctx, command("DBSIZE"), &count); checkErr != nil {
		return checkErr
	}
	if strings.TrimSpace(count.String()) != "0" {
		return errors.New("recovery graph must be empty")
	}
	header := make([]byte, 5)
	if _, err = io.ReadFull(payload, header); err != nil || string(header) != "REDIS" {
		return errors.New("invalid native graph RDB")
	}
	if _, err = payload.Seek(0, io.SeekStart); err != nil {
		return err
	}
	info, err := payload.Stat()
	if err != nil {
		return err
	}
	name := "loomplane-graph-" + rand.Text() + ".rdb"
	defer func() { err = errors.Join(err, dbexec.Cleanup(ctx, d.Run, name)) }()
	if err := d.Upload(ctx, name, info.Size(), func(dst io.Writer) error { _, copyErr := io.Copy(dst, payload); return copyErr }); err != nil {
		return err
	}
	if err := d.Run(ctx, []string{"sh", "-c", `cp "$1" /data/loomplane-restore.rdb && mv /data/loomplane-restore.rdb /data/dump.rdb`, "loomplane-restore", "/tmp/" + name}, io.Discard); err != nil {
		return err
	}
	return d.Restart(ctx)
}
