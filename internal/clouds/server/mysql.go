package server

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/backupfiles"
	native "github.com/xraph/loomplane/internal/clouds/mysql"
	nativenats "github.com/xraph/loomplane/internal/clouds/nats"
	"github.com/xraph/loomplane/internal/clouds/owned"
	nativerabbit "github.com/xraph/loomplane/internal/clouds/rabbitmq"
	nativecache "github.com/xraph/loomplane/internal/clouds/redis"
)

func (r *Runtime) mysqlSession(ctx context.Context, iid id.ID, labels map[string]string) (*Client, error) {
	if !native.ValidDatabase(labels[native.LabelDatabase]) || !native.ValidEngine(labels[native.LabelEngine]) {
		return nil, errors.New("invalid MySQL/MariaDB database identity")
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
	if !info.State.Running || info.Config.Labels[native.LabelDatabase] != labels[native.LabelDatabase] || info.Config.Labels[native.LabelEngine] != labels[native.LabelEngine] {
		c.Close()
		return nil, ErrOwned
	}
	return c, nil
}

func nativeMySQL(c *Client, container string, labels map[string]string) native.Database {
	return native.Database{Engine: labels[native.LabelEngine], Name: labels[native.LabelDatabase], Recovery: labels[LabelRecovery] != "", Run: func(ctx context.Context, args []string, out io.Writer) error {
		return ExecOutput(ctx, c, container, args, out)
	}, Upload: func(ctx context.Context, name string, size int64, write func(io.Writer) error) error {
		return putContainerFile(ctx, c, container, name, size, write)
	}}
}

func (r *Runtime) ConfigureMySQL(ctx context.Context, iid id.ID, labels map[string]string, password string) error {
	if err := native.ValidatePassword(password); err != nil {
		return err
	}
	c, err := r.mysqlSession(ctx, iid, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	return nativeMySQL(c, ContainerName(iid), labels).Configure(ctx, password)
}

func (r *Runtime) ConfigureDatabase(ctx context.Context, iid id.ID, labels map[string]string, password string) error {
	if labels[nativerabbit.LabelEngine] != "" {
		return r.withRabbitMQ(ctx, iid, labels, password, func(db nativerabbit.Database) error { return db.Configure(ctx, password) })
	}
	if labels[nativenats.LabelEngine] != "" {
		if err := native.ValidatePassword(password); err != nil {
			return err
		}
		c, err := r.Session(ctx, labels)
		if err != nil {
			return err
		}
		defer c.Close()
		return r.natsDatabase(c, iid, labels, password).Probe(ctx)
	}
	if labels[nativecache.LabelEngine] != "" {
		return r.configureCache(ctx, iid, labels, password)
	}
	if labels[native.LabelDatabase] != "" {
		return r.ConfigureMySQL(ctx, iid, labels, password)
	}
	return r.ConfigurePostgres(ctx, iid, labels, password)
}
func (r *Runtime) BackupDatabase(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, key string) (backupfiles.Object, error) {
	if labels[nativerabbit.LabelEngine] != "" {
		var obj backupfiles.Object
		err := r.withRabbitMQ(ctx, iid, labels, "", func(db nativerabbit.Database) error { var e error; obj, e = db.Export(ctx, files, key); return e })
		return obj, err
	}
	if labels[nativenats.LabelEngine] != "" {
		return r.backupNATS(ctx, iid, labels, files, key)
	}
	if labels[nativecache.LabelEngine] != "" {
		return r.backupCache(ctx, iid, labels, files, key)
	}
	if labels[native.LabelDatabase] == "" {
		return r.BackupPostgres(ctx, iid, labels, files, key)
	}
	c, err := r.mysqlSession(ctx, iid, labels)
	if err != nil {
		return backupfiles.Object{}, err
	}
	defer c.Close()
	return nativeMySQL(c, ContainerName(iid), labels).Export(ctx, files, key)
}
func (r *Runtime) RestoreDatabase(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, object backupfiles.Object) error {
	if labels[nativerabbit.LabelEngine] != "" {
		if labels[LabelRecovery] == "" || labels[owned.LabelPublic] != "false" {
			return errors.New("restore requires a private RabbitMQ copy")
		}
		return r.withRabbitMQ(ctx, iid, labels, "", func(db nativerabbit.Database) error { return db.Restore(ctx, files, object) })
	}
	if labels[nativenats.LabelEngine] != "" {
		if labels[LabelRecovery] == "" || labels[owned.LabelPublic] != "false" {
			return errors.New("restore requires a private JetStream recovery instance")
		}
		return r.withNATS(ctx, iid, labels, func(db nativenats.Database) error { return db.Restore(ctx, files, object) })
	}
	if labels[nativecache.LabelEngine] != "" {
		return r.restoreCache(ctx, iid, labels, files, object)
	}
	if labels[native.LabelDatabase] == "" {
		return r.RestorePostgres(ctx, iid, labels, files, object)
	}
	if labels[LabelRecovery] == "" || labels[owned.LabelPublic] != "false" {
		return errors.New("restore requires a private recovery instance")
	}
	c, err := r.mysqlSession(ctx, iid, labels)
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
	return nativeMySQL(c, ContainerName(iid), labels).Restore(ctx, files, object)
}
func (r *Runtime) DatabaseProbe(ctx context.Context, iid id.ID, labels map[string]string) error {
	if labels[nativerabbit.LabelEngine] != "" {
		return r.withRabbitMQ(ctx, iid, labels, "", func(db nativerabbit.Database) error { return db.Probe(ctx) })
	}
	if labels[nativenats.LabelEngine] != "" {
		return r.withNATS(ctx, iid, labels, func(db nativenats.Database) error { return db.Probe(ctx) })
	}
	if labels[nativecache.LabelEngine] != "" {
		return r.cacheProbe(ctx, iid, labels)
	}
	if labels[native.LabelDatabase] == "" {
		return r.RestoreProbe(ctx, iid, labels)
	}
	c, err := r.mysqlSession(ctx, iid, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	return nativeMySQL(c, ContainerName(iid), labels).Probe(ctx)
}

func databaseReady(ctx context.Context, c *Client, container string, labels map[string]string) error {
	if labels[nativerabbit.LabelEngine] != "" {
		return nativeRabbitMQ(c, container, labels).Ready(ctx)
	}
	if labels[nativenats.LabelEngine] != "" {
		iid, err := id.Parse(strings.Replace(strings.TrimPrefix(container, "lp-"), "inst-", "inst_", 1))
		if err != nil {
			return err
		}
		return (&Runtime{}).natsDatabase(c, iid, labels, "").Probe(ctx)
	}
	if labels[nativecache.LabelEngine] != "" {
		return nativeCache(c, container, labels).Ready(ctx)
	}
	if labels[native.LabelDatabase] != "" {
		return nativeMySQL(c, container, labels).Ready(ctx)
	}
	return PostgresReady(ctx, c, container, labels[LabelDatabase])
}
func isDatabase(labels map[string]string) bool {
	return labels[nativerabbit.LabelEngine] != "" || labels[nativenats.LabelEngine] != "" || labels[LabelDatabase] != "" || labels[native.LabelDatabase] != "" || labels[nativecache.LabelEngine] != ""
}
