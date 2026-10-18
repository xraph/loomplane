package server

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/owned"
	nativepg "github.com/xraph/loomplane/internal/clouds/postgres"
)

const LabelDatabase = nativepg.LabelDatabase
const LabelRecovery = nativepg.LabelRecovery

var _ nativepg.Backend = (*Runtime)(nil)

func (r *Runtime) postgresSession(ctx context.Context, iid id.ID, labels map[string]string) (*Client, error) {
	if !nativepg.ValidDatabase(labels[LabelDatabase]) {
		return nil, errors.New("invalid PostgreSQL database identity")
	}
	c, err := r.Session(ctx, labels)
	if err != nil {
		return nil, err
	}
	info, err := r.inspect(ctx, c, ContainerName(iid), iid, labels)
	if err != nil {
		c.Close()
		return nil, err
	}
	if !info.State.Running || info.Config.Labels[LabelDatabase] != labels[LabelDatabase] {
		c.Close()
		return nil, errors.New("the running instance is not the recorded PostgreSQL database")
	}
	return c, nil
}

func nativePostgres(c *Client, iid id.ID, labels map[string]string) nativepg.Database {
	return nativepg.Database{Name: labels[LabelDatabase], Recovery: labels[LabelRecovery] != "", Run: func(ctx context.Context, args []string, out io.Writer) error {
		return ExecOutput(ctx, c, ContainerName(iid), args, out)
	}, Upload: func(ctx context.Context, name string, size int64, write func(io.Writer) error) error {
		return putContainerFile(ctx, c, ContainerName(iid), name, size, write)
	}}
}

func (r *Runtime) BackupPostgres(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, key string) (backupfiles.Object, error) {
	c, err := r.postgresSession(ctx, iid, labels)
	if err != nil {
		return backupfiles.Object{}, err
	}
	defer c.Close()
	return nativePostgres(c, iid, labels).Export(ctx, files, key)
}
func (r *Runtime) RestorePostgres(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, object backupfiles.Object) error {
	if labels[LabelRecovery] == "" || labels[owned.LabelPublic] == "true" {
		return errors.New("restore requires a private recovery instance")
	}
	c, err := r.postgresSession(ctx, iid, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	info, err := r.inspect(ctx, c, ContainerName(iid), iid, labels)
	if err != nil {
		return err
	}
	if info.Config.Labels[LabelRecovery] != labels[LabelRecovery] {
		return ErrOwned
	}
	return nativePostgres(c, iid, labels).Restore(ctx, files, object)
}
func PostgresReady(ctx context.Context, c *Client, container, database string) error {
	db := nativepg.Database{Name: database, Run: func(ctx context.Context, args []string, out io.Writer) error {
		return ExecOutput(ctx, c, container, args, out)
	}}
	return db.Ready(ctx)
}
func (r *Runtime) RestoreProbe(ctx context.Context, iid id.ID, labels map[string]string) error {
	c, err := r.postgresSession(ctx, iid, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	return nativePostgres(c, iid, labels).Probe(ctx)
}
func (r *Runtime) ConfigurePostgres(ctx context.Context, iid id.ID, labels map[string]string, password string) error {
	if err := nativepg.ValidatePassword(password); err != nil {
		return err
	}
	c, err := r.postgresSession(ctx, iid, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	return nativePostgres(c, iid, labels).Configure(ctx, password)
}

func putContainerFile(ctx context.Context, c *Client, container, name string, size int64, write func(io.Writer) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		archive := tar.NewWriter(writer)
		err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, Typeflag: tar.TypeReg})
		if err == nil {
			err = write(archive)
		}
		if err == nil {
			err = archive.Close()
		}
		writer.CloseWithError(err)
		done <- err
	}()
	response, err := c.RequestHeaders(ctx, http.MethodPut, "/containers/"+url.PathEscape(container)+"/archive?path=/tmp", reader, http.Header{"Content-Type": []string{"application/x-tar"}})
	if err == nil {
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			err = fmt.Errorf("upload database file returned HTTP %d", response.StatusCode)
		}
	}
	reader.CloseWithError(err)
	cancel()
	return errors.Join(err, <-done)
}
