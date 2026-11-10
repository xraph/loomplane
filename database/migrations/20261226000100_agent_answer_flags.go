package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// People's reports of the agent's answers, for whoever runs the server.
// A flag keeps the question and the answer as they were when it was made,
// with the workspace, project, conversation (session_id) and turn
// (task_id) they came from, and nothing else of the conversation. Each
// person flags a turn once; flagging it again changes that row's reason
// and note. Only the server's owner reads them, open ones newest first.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_agent_answer_flags").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull(), migrate.Check("org_id <> ''")).
				Column("project_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("session_id", migrate.Text, migrate.NotNull(), migrate.Check("session_id <> ''")).
				Column("task_id", migrate.Text, migrate.NotNull(), migrate.Check("task_id <> ''")).
				Column("reported_by", migrate.Text, migrate.NotNull(), migrate.Check("reported_by <> ''")).
				Column("reason", migrate.Text, migrate.NotNull(),
					migrate.Check("reason IN ('offensive','harmful','inaccurate','other')")).
				Column("note", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("question", migrate.Text, migrate.NotNull()).
				Column("answer", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Column("dismissed_by", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("dismissed_at", migrate.Timestamp).
				Exec(ctx)
			if err != nil {
				return err
			}
			// One flag per turn per person.
			err = db.NewCreateIndex("loomplane_agent_answer_flags_reporter").IfNotExists().Unique().
				On("loomplane_agent_answer_flags").
				Columns("task_id", "reported_by").
				Exec(ctx)
			if err != nil {
				return err
			}
			// The owner's list: open (or dismissed) flags, newest first.
			return db.NewCreateIndex("loomplane_agent_answer_flags_status").IfNotExists().
				On("loomplane_agent_answer_flags").
				Columns("dismissed_at", "created_at").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropTable("loomplane_agent_answer_flags").IfExists().Exec(ctx)
		},
	)
}
