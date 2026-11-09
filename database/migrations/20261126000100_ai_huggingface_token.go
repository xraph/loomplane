package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// The workspace Hugging Face token, as far as Postgres knows it: a hint
// ("hf_" and the last four characters), the account it belongs to, when it
// was set and a revision every put and delete bumps. The value itself lives
// in Vault, never here. A null huggingface_set_at means no token is set.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_ai_settings").
				AddColumn("huggingface_hint", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				AddColumn("huggingface_account", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				AddColumn("huggingface_set_at", migrate.Timestamp).IfNotExists().
				AddColumn("huggingface_revision", migrate.BigInt, migrate.NotNull(), migrate.Default(0),
					migrate.Check("huggingface_revision>=0")).IfNotExists().
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_ai_settings").
				DropColumn("huggingface_revision").IfExists().
				DropColumn("huggingface_set_at").IfExists().
				DropColumn("huggingface_account").IfExists().
				DropColumn("huggingface_hint").IfExists().
				Exec(ctx)
		},
	)
}
