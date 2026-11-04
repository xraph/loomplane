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
	if _, err := r.classifyInstance(ctx, c, iid, labels); err != nil {
		return backupfiles.Object{}, err
	}
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
			return r.stoppedEngine(ctx, c, iid, labels, "backup", key, func(ctx context.Context, sourceID string) error {
				return downloadEngineFile(ctx, c, sourceID, "/data/analytics.db", out)
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
	if _, err := r.classifyInstance(ctx, c, iid, labels); err != nil {
		return err
	}
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
		return r.stoppedEngine(ctx, c, iid, labels, "restore", labels[LabelRecovery], func(ctx context.Context, sourceID string) error { return uploadEngineFile(ctx, c, sourceID, payload) })
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

// stoppedEngine retains the lifecycle reservation until exact-source recovery.
func (r *Runtime) stoppedEngine(ctx context.Context, c *Client, iid id.ID, labels map[string]string, action, operation string, work func(context.Context, string) error) (err error) {
	guard, err := r.acquireDuckMutation(ctx, c, iid, labels, duckMutationIntent{Action: action, Operation: operation})
	if err != nil {
		return err
	}
	if guard == nil {
		return ErrOwned
	}
	sourceID := guard.record.SourceID
	stopped := false
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
		defer cancel()
		for _, check := range []func(context.Context) error{guard.owner, guard.sameSource, guard.sameVolume, guard.noPrevious} {
			if checkErr := check(cleanup); checkErr != nil {
				err = errors.Join(err, checkErr, mutationPending(guard.name))
				return
			}
		}
		if !stopped {
			// Reissue an uncertain stop and wait for its response before restarting.
			stopErr := dockerSetRunning(cleanup, c, sourceID, false)
			current, checkErr := r.inspect(cleanup, c, sourceID, iid, labels)
			if stopErr != nil || checkErr != nil || current.State.Running {
				err = errors.Join(err, stopErr, checkErr, mutationPending(guard.name))
				return
			}
		}
		if action == "restore" && err != nil {
			err = errors.Join(err, guard.discardRestore(cleanup))
			return
		}
		if checkErr := guard.owner(cleanup); checkErr != nil {
			err = errors.Join(err, checkErr, mutationPending(guard.name))
			return
		}
		startErr := dockerSetRunning(cleanup, c, sourceID, true)
		if startErr == nil {
			startErr = guard.waitAuthenticated(cleanup, sourceID)
		}
		if startErr != nil {
			if action == "restore" {
				err = errors.Join(err, startErr, guard.discardRestore(cleanup))
				return
			}
			err = errors.Join(err, startErr, mutationPending(guard.name))
			return
		}
		// Copy failure is retryable only after the original source is healthy again.
		err = errors.Join(err, guard.finish(cleanup, nil))
	}()
	if ownerErr := guard.owner(ctx); ownerErr != nil {
		return ownerErr
	}
	if stopErr := dockerJSON(ctx, c, http.MethodPost, "/containers/"+sourceID+"/stop?t=25", nil, nil); stopErr != nil {
		return stopErr
	}
	current, err := r.inspect(ctx, c, sourceID, iid, labels)
	if err != nil {
		return err
	}
	stopped = !current.State.Running
	if current.State.Running || current.State.ExitCode != 0 {
		return errors.New("DuckDB did not shut down cleanly; no database file was copied")
	}
	if walErr := noDuckWAL(ctx, c, sourceID); walErr != nil {
		return walErr
	}
	// A 404 can mean the container vanished, not just that its WAL is absent.
	for _, check := range []func(context.Context) error{guard.owner, guard.sameSource, guard.sameVolume, guard.noPrevious} {
		if checkErr := check(ctx); checkErr != nil {
			return checkErr
		}
	}
	current, err = r.inspect(ctx, c, sourceID, iid, labels)
	if err != nil {
		return err
	}
	if current.ID != sourceID || current.State.Running || current.State.ExitCode != 0 {
		return ErrOwned
	}
	return work(ctx, sourceID)
}

func noDuckWAL(ctx context.Context, c *Client, containerID string) error {
	response, err := c.Request(ctx, http.MethodHead, "/containers/"+containerID+"/archive?path=/data/analytics.db.wal", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		return errors.New("DuckDB checkpoint is incomplete: a WAL remains or its absence could not be verified")
	}
	return nil
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
	return downloadEngineDirectoryLimit(ctx, c, container, dir, out, enginebackup.MaxPayload)
}

func downloadEngineDirectoryLimit(ctx context.Context, c *Client, container, dir string, out io.Writer, limit int64) error {
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
	remaining := limit
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
