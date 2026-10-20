package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/owned"
	native "github.com/xraph/loomplane/internal/clouds/redis"
)

func (r *Runtime) cacheSession(ctx context.Context, iid id.ID, labels map[string]string) (*Client, error) {
	if !native.ValidEngine(labels[native.LabelEngine]) {
		return nil, errors.New("invalid Redis/Valkey database identity")
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
	if !info.State.Running || labels[owned.LabelImage] == "" || info.Config.Image != labels[owned.LabelImage] || info.Config.Labels[native.LabelEngine] != labels[native.LabelEngine] || info.Config.Labels[LabelRecovery] != labels[LabelRecovery] {
		c.Close()
		return nil, ErrOwned
	}
	return c, nil
}

func nativeCache(c *Client, container string, labels map[string]string) native.Database {
	return native.Database{Engine: labels[native.LabelEngine], Recovery: labels[LabelRecovery] != "", Run: func(ctx context.Context, args []string, out io.Writer) error {
		return ExecOutput(ctx, c, container, args, out)
	}, Upload: func(ctx context.Context, name string, size int64, write func(io.Writer) error) error {
		return putContainerFile(ctx, c, container, name, size, write)
	}}
}

func (r *Runtime) configureCache(ctx context.Context, iid id.ID, labels map[string]string, password string) error {
	c, err := r.cacheSession(ctx, iid, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	return nativeCache(c, ContainerName(iid), labels).Configure(ctx, password)
}
func (r *Runtime) backupCache(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, key string) (backupfiles.Object, error) {
	c, err := r.cacheSession(ctx, iid, labels)
	if err != nil {
		return backupfiles.Object{}, err
	}
	defer c.Close()
	return nativeCache(c, ContainerName(iid), labels).Export(ctx, files, key)
}
func (r *Runtime) restoreCache(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, object backupfiles.Object) error {
	if labels[LabelRecovery] == "" || labels[owned.LabelPublic] != "false" {
		return errors.New("restore requires a private recovery cache")
	}
	c, err := r.cacheSession(ctx, iid, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	info, err := r.inspect(ctx, c, ContainerName(iid), iid, labels)
	if err != nil {
		return err
	}
	if info.Config.Labels[owned.LabelPublic] != "false" {
		return ErrOwned
	}
	for _, ports := range info.NetworkSettings.Ports {
		if len(ports) != 0 {
			return errors.New("recovery cache has a published port")
		}
	}
	db := nativeCache(c, ContainerName(iid), labels)
	db.Restart = func(ctx context.Context) error {
		if _, checkErr := r.inspect(ctx, c, ContainerName(iid), iid, labels); checkErr != nil {
			return checkErr
		}
		if checkErr := dockerJSON(ctx, c, http.MethodPost, "/containers/"+url.PathEscape(ContainerName(iid))+"/restart?t=30", nil, nil); checkErr != nil {
			return checkErr
		}
		return r.wait(ctx, c, ContainerName(iid), iid, labels)
	}
	return db.Restore(ctx, files, object)
}
func (r *Runtime) cacheProbe(ctx context.Context, iid id.ID, labels map[string]string) error {
	c, err := r.cacheSession(ctx, iid, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	return nativeCache(c, ContainerName(iid), labels).Probe(ctx)
}
