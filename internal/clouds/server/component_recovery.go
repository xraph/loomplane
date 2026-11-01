package server

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/enginebackup"
	nativegraph "github.com/xraph/loomplane/internal/clouds/falkordb"
	"github.com/xraph/loomplane/internal/clouds/owned"
	nativesearch "github.com/xraph/loomplane/internal/clouds/search"
	"github.com/xraph/loomplane/internal/engineprobe"
)

func engineIdentity(iid id.ID, labels map[string]string) (enginebackup.Identity, error) {
	identity := enginebackup.Identity{Org: labels[owned.LabelOrg], Project: labels[owned.LabelProject], Environment: labels[owned.LabelEnvironment], Resource: labels[owned.LabelService], Instance: iid.String(), Kind: labels[enginebackup.LabelEngine], Revision: labels[enginebackup.LabelRevision], Image: labels[owned.LabelImage], Architecture: labels[owned.LabelArchitecture], Format: enginebackup.Format(labels[enginebackup.LabelEngine])}
	if source := labels[enginebackup.LabelSource]; source != "" {
		if labels[LabelRecovery] == "" {
			return identity, ErrOwned
		}
		var original enginebackup.Identity
		if json.Unmarshal([]byte(source), &original) != nil || original.Org != identity.Org || original.Project != identity.Project || original.Environment != identity.Environment || original.Kind != identity.Kind || original.Revision != identity.Revision || original.Image != identity.Image || original.Architecture != identity.Architecture || original.Format != identity.Format {
			return identity, ErrOwned
		}
		identity = original
	}
	return identity, identity.Validate()
}
func (r *Runtime) engineSession(ctx context.Context, iid id.ID, labels map[string]string) (*Client, error) {
	if _, err := engineIdentity(iid, labels); err != nil {
		return nil, err
	}
	c, err := r.Session(ctx, labels)
	if err != nil {
		return nil, err
	}
	info, err := r.inspect(ctx, c, ContainerName(iid), iid, labels)
	if err == nil {
		if !info.State.Running || info.Config.Image != labels[owned.LabelImage] {
			err = ErrOwned
		}
		for _, key := range []string{enginebackup.LabelEngine, enginebackup.LabelRevision, enginebackup.LabelSource, LabelRecovery, owned.LabelPublic, owned.LabelConnection} {
			if info.Config.Labels[key] != labels[key] {
				err = ErrOwned
			}
		}
		if labels[LabelRecovery] != "" {
			if labels[owned.LabelPublic] != "false" {
				err = ErrOwned
			}
			for _, ports := range info.NetworkSettings.Ports {
				if len(ports) > 0 {
					err = ErrOwned
				}
			}
			if len(info.NetworkSettings.Networks) != 1 {
				err = ErrOwned
			} else if _, ok := info.NetworkSettings.Networks[runtimeNetwork(labels)]; !ok {
				err = ErrOwned
			}
		}
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
func (r *Runtime) engineProbe(ctx context.Context, iid id.ID, labels map[string]string) error {
	c, err := r.engineSession(ctx, iid, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	args, err := engineprobe.Command(labels[enginebackup.LabelEngine], labels[enginebackup.LabelEngine]+"-v1")
	if err != nil {
		return err
	}
	return ExecOutput(ctx, c, ContainerName(iid), args, io.Discard)
}
func (r *Runtime) engineRun(c *Client, iid id.ID) func(context.Context, []string, io.Writer) error {
	return func(ctx context.Context, args []string, out io.Writer) error {
		return ExecOutput(ctx, c, ContainerName(iid), args, out)
	}
}
func (r *Runtime) engineUpload(c *Client, iid id.ID, engine string) func(context.Context, string, int64, func(io.Writer) error) error {
	return func(ctx context.Context, name string, size int64, write func(io.Writer) error) error {
		owner := fileOwner{}
		if engine == "elasticsearch" || engine == "opensearch" {
			owner = fileOwner{UID: 1000, GID: 1000}
		}
		return putOwnedContainerFile(ctx, c, ContainerName(iid), owner, name, size, write)
	}
}
func (r *Runtime) backupEngine(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, key string) (backupfiles.Object, error) {
	if labels[LabelRecovery] != "" {
		return backupfiles.Object{}, errors.New("recovery copies cannot be backup sources")
	}
	identity, err := engineIdentity(iid, labels)
	if err != nil {
		return backupfiles.Object{}, err
	}
	c, err := r.engineSession(ctx, iid, labels)
	if err != nil {
		return backupfiles.Object{}, err
	}
	defer c.Close()
	run := r.engineRun(c, iid)
	return enginebackup.Export(ctx, files, key, identity, func(ctx context.Context, out io.Writer) error {
		switch identity.Kind {
		case "falkordb":
			return (nativegraph.Database{Run: run}).Export(ctx, out)
		case "elasticsearch", "opensearch":
			return (nativesearch.Database{Engine: identity.Kind, Run: run, Download: func(ctx context.Context, dir string, out io.Writer) error {
				return downloadEngineDirectory(ctx, c, ContainerName(iid), dir, out)
			}}).Export(ctx, out)
		case "duckdb":
			return r.stoppedEngine(ctx, c, iid, labels, func(ctx context.Context) error {
				return downloadEngineFile(ctx, c, ContainerName(iid), "/data/analytics.db", out)
			})
		}
		return errors.New("unsupported native engine")
	})
}
func (r *Runtime) restoreEngine(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, object backupfiles.Object) error {
	if labels[LabelRecovery] == "" || labels[owned.LabelPublic] != "false" || labels[enginebackup.LabelSource] == "" {
		return ErrOwned
	}
	identity, err := engineIdentity(iid, labels)
	if err != nil {
		return err
	}
	payload, err := enginebackup.Verified(ctx, files, object, identity)
	if err != nil {
		return err
	}
	defer os.Remove(payload.Name())
	defer payload.Close()
	c, err := r.engineSession(ctx, iid, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	run := r.engineRun(c, iid)
	upload := r.engineUpload(c, iid, identity.Kind)
	switch identity.Kind {
	case "falkordb":
		return (nativegraph.Database{Run: run, Upload: upload, Restart: func(ctx context.Context) error { return r.restartEngine(ctx, c, iid, labels) }}).Restore(ctx, payload)
	case "elasticsearch", "opensearch":
		return (nativesearch.Database{Engine: identity.Kind, Run: run, Upload: upload, Extract: func(ctx context.Context, payload *os.File, dir string) error {
			return extractEngineDirectory(ctx, c, ContainerName(iid), dir, payload)
		}}).Restore(ctx, payload)
	case "duckdb":
		return r.stoppedEngine(ctx, c, iid, labels, func(ctx context.Context) error { return uploadEngineFile(ctx, c, ContainerName(iid), payload) })
	}
	return errors.New("unsupported native engine")
}
func (r *Runtime) configureEngine(ctx context.Context, iid id.ID, labels map[string]string, password string) error {
	c, err := r.engineSession(ctx, iid, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	engine := labels[enginebackup.LabelEngine]
	if engine == "elasticsearch" || engine == "opensearch" {
		return (nativesearch.Database{Engine: engine, Run: r.engineRun(c, iid), Upload: r.engineUpload(c, iid, engine)}).ConfigureReader(ctx, password)
	}
	return nil
}
func (r *Runtime) restartEngine(ctx context.Context, c *Client, iid id.ID, labels map[string]string) error {
	if _, err := r.inspect(ctx, c, ContainerName(iid), iid, labels); err != nil {
		return err
	}
	if err := dockerJSON(ctx, c, http.MethodPost, "/containers/"+ContainerName(iid)+"/restart?t=25", nil, nil); err != nil {
		return err
	}
	return r.wait(ctx, c, ContainerName(iid), iid, labels)
}

// stoppedEngine always resumes the owned service, including after cancellation.
func (r *Runtime) stoppedEngine(ctx context.Context, c *Client, iid id.ID, labels map[string]string, work func(context.Context) error) (err error) {
	name := ContainerName(iid)
	stopped := false
	if _, err = r.inspect(ctx, c, name, iid, labels); err != nil {
		return err
	}
	release, lockErr := r.checkpointLock(ctx, c, iid, labels)
	if lockErr != nil {
		return lockErr
	}
	defer func() { err = errors.Join(err, release()) }()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
		defer cancel()
		if _, checkErr := r.inspect(cleanup, c, name, iid, labels); checkErr != nil {
			err = errors.Join(err, checkErr)
			return
		}
		if !stopped {
			// A cancelled HTTP request may leave Docker's stop in flight. Reissue
			// it with the cleanup deadline before starting, so it cannot stop later.
			stopErr := dockerJSON(cleanup, c, http.MethodPost, "/containers/"+name+"/stop?t=25", nil, nil)
			current, checkErr := r.inspect(cleanup, c, name, iid, labels)
			if checkErr != nil || current.State.Running {
				err = errors.Join(err, stopErr, checkErr, errors.New("could not settle database shutdown"))
				return
			}
		}
		startErr := dockerJSON(cleanup, c, http.MethodPost, "/containers/"+name+"/start", nil, nil)
		if startErr == nil {
			startErr = r.wait(cleanup, c, name, iid, labels)
		}
		err = errors.Join(err, startErr)
	}()
	if checkErr := dockerJSON(ctx, c, http.MethodPost, "/containers/"+name+"/stop?t=25", nil, nil); checkErr != nil {
		return checkErr
	}
	info, err := r.inspect(ctx, c, name, iid, labels)
	if err != nil {
		return err
	}
	stopped = !info.State.Running
	if info.State.Running || info.State.ExitCode != 0 {
		return errors.New("DuckDB did not shut down cleanly; no backup file was copied")
	}
	return work(ctx)
}
func downloadEngineFile(ctx context.Context, c *Client, container, filename string, out io.Writer) error {
	response, err := c.Request(ctx, http.MethodGet, "/containers/"+container+"/archive?path="+url.QueryEscape(filename), nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("read stopped database returned HTTP %d", response.StatusCode)
	}
	tr := tar.NewReader(response.Body)
	h, err := tr.Next()
	if err != nil || h.Name != "analytics.db" || h.Typeflag != tar.TypeReg || h.Size <= 0 || h.Size > enginebackup.MaxPayload {
		return errors.New("invalid stopped database archive")
	}
	if _, err = io.CopyN(out, tr, h.Size); err != nil {
		return err
	}
	if _, err = tr.Next(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected stopped database file")
	}
	return nil
}
func uploadEngineFile(ctx context.Context, c *Client, container string, payload *os.File) error {
	info, err := payload.Stat()
	if err != nil {
		return err
	}
	// DuckDB's magic lives at byte 8; reject unrelated archives before upload.
	header := make([]byte, 12)
	if _, err = io.ReadFull(payload, header); err != nil || string(header[8:12]) != "DUCK" {
		return errors.New("invalid checkpointed DuckDB file")
	}
	if _, err = payload.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		tw := tar.NewWriter(writer)
		e := tw.WriteHeader(&tar.Header{Name: "analytics.db", Mode: 0o600, Typeflag: tar.TypeReg, Size: info.Size()})
		if e == nil {
			_, e = io.Copy(tw, payload)
		}
		if e == nil {
			e = tw.Close()
		}
		writer.CloseWithError(e)
		done <- e
	}()
	response, err := c.RequestHeaders(ctx, http.MethodPut, "/containers/"+container+"/archive?path=/data", reader, http.Header{"Content-Type": []string{"application/x-tar"}})
	if err == nil {
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			err = errors.New("upload stopped database failed")
		}
	}
	reader.CloseWithError(err)
	return errors.Join(err, <-done)
}

func downloadEngineDirectory(ctx context.Context, c *Client, container, dir string, out io.Writer) error {
	response, err := c.Request(ctx, http.MethodGet, "/containers/"+container+"/archive?path="+url.QueryEscape(dir), nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("read native snapshot repository failed")
	}
	reader := tar.NewReader(response.Body)
	writer := tar.NewWriter(out)
	root := path.Base(dir)
	remaining := enginebackup.MaxPayload
	for {
		h, e := reader.Next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return e
		}
		if strings.TrimSuffix(h.Name, "/") == root && h.Typeflag == tar.TypeDir {
			continue
		}
		name, ok := strings.CutPrefix(h.Name, root+"/")
		if !ok || path.Clean(strings.TrimSuffix(name, "/")) != strings.TrimSuffix(name, "/") || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || (h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir) {
			return errors.New("invalid native repository file")
		}
		if h.Size < 0 || h.Size > remaining {
			return errors.New("native repository exceeds size limit")
		}
		remaining -= h.Size
		mode := int64(0o600)
		if h.Typeflag == tar.TypeDir {
			mode = 0o700
		}
		if headerErr := writer.WriteHeader(&tar.Header{Name: name, Typeflag: h.Typeflag, Size: h.Size, Mode: mode, Uid: 1000, Gid: 1000}); headerErr != nil {
			return headerErr
		}
		if _, e = io.CopyN(writer, reader, h.Size); e != nil {
			return e
		}
	}
	return writer.Close()
}
func extractEngineDirectory(ctx context.Context, c *Client, container, dir string, payload *os.File) error {
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		tr := tar.NewReader(payload)
		tw := tar.NewWriter(writer)
		var err error
		remaining := enginebackup.MaxPayload
		for {
			h, e := tr.Next()
			if errors.Is(e, io.EOF) {
				break
			}
			if e != nil {
				err = e
				break
			}
			if h.Size < 0 || h.Size > remaining {
				err = errors.New("native repository exceeds size limit")
				break
			}
			remaining -= h.Size
			mode := int64(0o600)
			if h.Typeflag == tar.TypeDir {
				mode = 0o700
			}
			if err = tw.WriteHeader(&tar.Header{Name: h.Name, Typeflag: h.Typeflag, Size: h.Size, Mode: mode, Uid: 1000, Gid: 1000}); err != nil {
				break
			}
			if _, err = io.CopyN(tw, tr, h.Size); err != nil {
				break
			}
		}
		if err == nil {
			err = tw.Close()
		}
		writer.CloseWithError(err)
		done <- err
	}()
	response, err := c.RequestHeaders(ctx, http.MethodPut, "/containers/"+container+"/archive?path="+url.QueryEscape(dir), reader, http.Header{"Content-Type": []string{"application/x-tar"}})
	if err == nil {
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			err = errors.New("upload native snapshot repository failed")
		}
	}
	reader.CloseWithError(err)
	return errors.Join(err, <-done)
}

// A daemon-owned reservation serializes checkpoint copies across worker hosts.
// A worker crash leaves a visible reservation; it is never stolen on a timer.
func (r *Runtime) checkpointLock(ctx context.Context, c *Client, iid id.ID, labels map[string]string) (func() error, error) {
	lockLabels := map[string]string{owned.LabelInstance: iid.String(), owned.LabelOrg: labels[owned.LabelOrg], owned.LabelProject: labels[owned.LabelProject], owned.LabelEnvironment: labels[owned.LabelEnvironment], owned.LabelConnection: labels[owned.LabelConnection], "loomplane.engine.checkpoint-lock": "true"}
	var created struct {
		ID string `json:"Id"`
	}
	body := map[string]any{"Image": labels[owned.LabelImage], "Labels": lockLabels, "HostConfig": map[string]any{"NetworkMode": "none"}}
	if err := dockerJSON(ctx, c, http.MethodPost, "/containers/create?name="+ContainerName(iid)+"-checkpoint", body, &created); err != nil {
		return nil, errors.New("another checkpoint or an unfinished backup holds this service; recover that operation before retrying")
	}
	if created.ID == "" {
		return nil, errors.New("checkpoint reservation has no identity")
	}
	return func() error {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		record, err := r.inspect(cleanup, c, created.ID, iid, labels)
		if err != nil {
			return err
		}
		if record.ID != created.ID || record.Config.Labels["loomplane.engine.checkpoint-lock"] != "true" || record.State.Running {
			return ErrOwned
		}
		return dockerJSON(cleanup, c, http.MethodDelete, "/containers/"+created.ID, nil, nil)
	}, nil
}
