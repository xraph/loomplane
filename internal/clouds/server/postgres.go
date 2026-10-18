package server

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/owned"
)

const cleanupWait = 30 * time.Second

const LabelDatabase = "loomplane.postgres.database"
const LabelRecovery = "loomplane.postgres.recovery"

var databaseIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

func (r *Runtime) postgresSession(ctx context.Context, iid id.ID, labels map[string]string) (*Client, error) {
	if !databaseIdentifier.MatchString(labels[LabelDatabase]) {
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

// BackupPostgres exports one database in the native custom format. A failed
// pg_dump closes the stream with an error, so Trove cannot publish partial data.
func (r *Runtime) BackupPostgres(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, key string) (backupfiles.Object, error) {
	c, err := r.postgresSession(ctx, iid, labels)
	if err != nil {
		return backupfiles.Object{}, err
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		dumpErr := ExecOutput(ctx, c, ContainerName(iid), []string{"pg_dump", "-U", "postgres", "-d", labels[LabelDatabase], "--format=custom", "--no-owner", "--no-acl"}, writer)
		writer.CloseWithError(dumpErr)
		done <- dumpErr
	}()
	object, err := files.Put(ctx, key, reader)
	reader.CloseWithError(err)
	cancel()
	return object, errors.Join(err, <-done)
}

// RestorePostgres is available only on an isolated recovery instance. The
// native restore starts after the complete dump passes its integrity check.
// Publication and credential bindings are the recovery workflow's responsibility.
func (r *Runtime) RestorePostgres(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, object backupfiles.Object) (err error) {
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
	name := "loomplane-" + rand.Text() + ".dump"
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupWait)
		defer cancel()
		err = errors.Join(err, ExecOutput(cleanup, c, ContainerName(iid), []string{"rm", "-f", "/tmp/" + name}, io.Discard))
	}()
	if copyErr := putContainerFile(ctx, c, ContainerName(iid), name, object.Size, func(w io.Writer) error { return files.CopyVerified(ctx, object, w) }); copyErr != nil {
		return copyErr
	}
	return ExecOutput(ctx, c, ContainerName(iid), []string{"pg_restore", "-U", "postgres", "-d", labels[LabelDatabase], "--single-transaction", "--exit-on-error", "--no-owner", "--no-acl", "--role=app", "/tmp/" + name}, io.Discard)
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

// PostgresReady uses the engine's local protocol probe, not an HTTP endpoint.
func PostgresReady(ctx context.Context, c *Client, container, database string) error {
	if !databaseIdentifier.MatchString(database) {
		return errors.New("invalid PostgreSQL database identity")
	}
	return ExecOutput(ctx, c, container, []string{"pg_isready", "-h", "127.0.0.1", "-U", "postgres", "-d", database}, io.Discard)
}

// RestoreProbe verifies SQL access after the restore before it can be exposed.
func (r *Runtime) RestoreProbe(ctx context.Context, iid id.ID, labels map[string]string) error {
	c, err := r.postgresSession(ctx, iid, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	var result bytes.Buffer
	if probeErr := ExecOutput(ctx, c, ContainerName(iid), []string{"psql", "-U", "postgres", "-d", labels[LabelDatabase], "-At", "-v", "ON_ERROR_STOP=1", "-c", "SELECT 1"}, &result); probeErr != nil {
		return probeErr
	}
	if result.String() != "1\n" {
		return errors.New("restored PostgreSQL SQL probe failed")
	}
	return nil
}

const appRoleSQL = `\getenv app_password LOOMPLANE_APP_PASSWORD
\getenv db_name LOOMPLANE_DATABASE
SELECT 'CREATE ROLE app LOGIN' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='app') \gexec
SELECT format('ALTER ROLE app WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD %L', :'app_password') \gexec
SELECT format('ALTER DATABASE %I OWNER TO app', :'db_name') \gexec
ALTER SCHEMA public OWNER TO app;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
`

// ConfigurePostgres reconstructs the application role kept in Vault. Passwords
// travel as exec environment values, never command arguments or SQL output.
func (r *Runtime) ConfigurePostgres(ctx context.Context, iid id.ID, labels map[string]string, password string) (err error) {
	if len(password) < 26 {
		return errors.New("PostgreSQL application password is missing")
	}
	c, err := r.postgresSession(ctx, iid, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	name := "loomplane-" + rand.Text() + ".sql"
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupWait)
		defer cancel()
		err = errors.Join(err, ExecOutput(cleanup, c, ContainerName(iid), []string{"rm", "-f", "/tmp/" + name}, io.Discard))
	}()
	if copyErr := putContainerFile(ctx, c, ContainerName(iid), name, int64(len(appRoleSQL)), func(w io.Writer) error { _, err := io.Copy(w, strings.NewReader(appRoleSQL)); return err }); copyErr != nil {
		return copyErr
	}
	if setupErr := execOutput(ctx, c, ContainerName(iid), []string{"psql", "-U", "postgres", "-d", labels[LabelDatabase], "-v", "ON_ERROR_STOP=1", "-f", "/tmp/" + name}, []string{"LOOMPLANE_APP_PASSWORD=" + password, "LOOMPLANE_DATABASE=" + labels[LabelDatabase]}, io.Discard); setupErr != nil {
		return setupErr
	}
	var result bytes.Buffer
	if probeErr := execOutput(ctx, c, ContainerName(iid), []string{"psql", "-h", "127.0.0.1", "-U", "app", "-d", labels[LabelDatabase], "-At", "-v", "ON_ERROR_STOP=1", "-c", "SELECT current_user"}, []string{"PGPASSWORD=" + password}, &result); probeErr != nil {
		return probeErr
	}
	if result.String() != "app\n" {
		return errors.New("PostgreSQL application authentication failed")
	}
	return nil
}
