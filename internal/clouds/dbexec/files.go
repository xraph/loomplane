// Package dbexec shares bounded native database transfers and temporary files.
package dbexec

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/backupfiles"
)

type Run func(context.Context, []string, io.Writer) error
type Upload func(context.Context, string, int64, func(io.Writer) error) error

func Cleanup(ctx context.Context, run Run, names ...string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	args := make([]string, 2, 2+len(names))
	args[0], args[1] = "rm", "-f"
	for _, name := range names {
		args = append(args, "/tmp/"+name)
	}
	return run(ctx, args, io.Discard)
}

func Text(ctx context.Context, upload Upload, name, value string) error {
	return upload(ctx, name, int64(len(value)), func(w io.Writer) error { _, err := io.Copy(w, strings.NewReader(value)); return err })
}

func Export(ctx context.Context, files *backupfiles.Store, key string, export func(context.Context, io.Writer) error) (backupfiles.Object, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := export(ctx, writer)
		writer.CloseWithError(err)
		done <- err
	}()
	object, err := files.Put(ctx, key, reader)
	reader.CloseWithError(err)
	cancel()
	return object, errors.Join(err, <-done)
}
