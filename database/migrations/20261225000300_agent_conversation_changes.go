package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// What a person changes about a conversation with the agent, which is
// otherwise only its chat tasks (20261220000500): the title they gave it
// (NULL until they do, and nothing automatic ever writes it), when they
// pinned it, and when they deleted it. A conversation has a row only once
// they change something. A deleted one keeps its tasks, their events and
// requests; only the conversation reads stop seeing it.
//
// Each chat task also keeps where in the dashboard the person sent its
// message from (page_context, NULL without one) and the preface the model
// read before the message (input_preface). input stays what the person
// typed. The agent_task realtime trigger sends an allowlist of columns,
// so neither reaches it.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_agent_conversations").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("session_id", migrate.Text, migrate.NotNull()).
				Column("title", migrate.Text).
				Column("pinned_at", migrate.Timestamp).
				Column("deleted_at", migrate.Timestamp).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				PrimaryKey("org_id", "session_id").
				Exec(ctx)
			if err != nil {
				return err
			}
			err = db.NewAlterTable("loomplane_agent_tasks").
				AddColumn("input_preface", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}
			return db.NewAlterTable("loomplane_agent_tasks").
				AddColumn("page_context", migrate.JSON).IfNotExists().
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_agent_tasks").
				DropColumn("page_context").IfExists().
				Exec(ctx)
			if err != nil {
				return err
			}
			err = db.NewAlterTable("loomplane_agent_tasks").
				DropColumn("input_preface").IfExists().
				Exec(ctx)
			if err != nil {
				return err
			}
			return db.NewDropTable("loomplane_agent_conversations").IfExists().Exec(ctx)
		},
	)
}
