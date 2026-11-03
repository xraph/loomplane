package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// The GitLab hosts organizations have registered, the sign-ins in flight and
// the GitLab accounts organizations have connected. Nothing secret is stored
// here: a host's client secret, and every access and refresh token, live in
// the vault. A sign-in's verifier is the PKCE code verifier of a flow that
// lasts minutes and is deleted when it is taken. Hosts the operator
// configures are not in these tables, and neither is gitlab.com.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_gitlab_hosts").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("web_url", migrate.Text, migrate.NotNull()).
				Column("host", migrate.Text, migrate.NotNull()).
				Column("client_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Unique("org_id", "host").Named("loomplane_gitlab_hosts_org_host").
				Unique("org_id", "id").Named("loomplane_gitlab_hosts_org_id").
				Exec(ctx)
			if err != nil {
				return err
			}
			err = db.NewCreateIndex("loomplane_gitlab_hosts_host").IfNotExists().
				On("loomplane_gitlab_hosts").
				Columns("host").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_gitlab_sign_ins").IfNotExists().
				Column("state", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("user_id", migrate.Text, migrate.NotNull()).
				Column("host", migrate.Text, migrate.NotNull()).
				Column("verifier", migrate.Text, migrate.NotNull()).
				Column("expires_at", migrate.Timestamp, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Exec(ctx)
			if err != nil {
				return err
			}
			err = db.NewCreateIndex("loomplane_gitlab_sign_ins_expires").IfNotExists().
				On("loomplane_gitlab_sign_ins").
				Columns("expires_at").
				Exec(ctx)
			if err != nil {
				return err
			}

			// host_id is NULL for gitlab.com and the operator's hosts, and
			// names a registered host otherwise. The foreign key is over
			// (org_id, host_id), so a connection can only use its own
			// organization's host, and an organization's host can't be
			// held by another's connection. A host with connections can't
			// be deleted: the foreign key restricts it. A NULL host_id
			// leaves the key unchecked.
			err = db.NewCreateTable("loomplane_gitlab_connections").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("host", migrate.Text, migrate.NotNull()).
				Column("host_id", migrate.Text).
				Column("kind", migrate.Text, migrate.NotNull(), migrate.Default("oauth")).
				Column("gitlab_user_id", migrate.BigInt, migrate.NotNull()).
				Column("username", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("avatar_url", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("version", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("lapsed_at", migrate.Timestamp).
				Column("webhook_set", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Unique("org_id", "host", "gitlab_user_id").Named("loomplane_gitlab_connections_org_host_user").
				ForeignKey("org_id", "host_id").References("loomplane_gitlab_hosts", "org_id", "id").
				OnDelete(migrate.Restrict).Named("loomplane_gitlab_connections_org_host_fk").
				Exec(ctx)
			if err != nil {
				return err
			}
			return db.NewCreateIndex("loomplane_gitlab_connections_host_id").IfNotExists().
				On("loomplane_gitlab_connections").
				Columns("host_id").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			for _, table := range []string{"loomplane_gitlab_connections", "loomplane_gitlab_sign_ins", "loomplane_gitlab_hosts"} {
				if err := db.NewDropTable(table).IfExists().Exec(ctx); err != nil {
					return err
				}
			}
			return nil
		},
	)
}
