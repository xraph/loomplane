package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// A conversation with the agent is the chat tasks of one workspace that
// share a session (internal/agentchat), so it needs no table of its own.
// Two partial indexes over the agent's tasks serve it: one reads a
// conversation's turns in order, the other a person's conversations by
// their last update. Neither adds a realtime trigger: a title is the
// person's own words, and agent_task events reach every agent:use holder.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateIndex("loomplane_agent_tasks_chat_session").IfNotExists().
				On("loomplane_agent_tasks").
				Columns("org_id", "session_id", "created_at").
				Where("kind = 'chat'").
				Exec(ctx)
			if err != nil {
				return err
			}
			return db.NewCreateIndex("loomplane_agent_tasks_chat_person").IfNotExists().
				On("loomplane_agent_tasks").
				Columns("org_id", "requested_by").ColumnDesc("updated_at").
				Where("kind = 'chat'").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			if _, err := db.Exec(ctx, `DROP INDEX IF EXISTS loomplane_agent_tasks_chat_person`); err != nil {
				return err
			}
			_, err := db.Exec(ctx, `DROP INDEX IF EXISTS loomplane_agent_tasks_chat_session`)
			return err
		},
	)
}
