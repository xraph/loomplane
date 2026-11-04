//go:build integration

package migrations

import (
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestAgentConversationIndexes(t *testing.T) {
	// 3b's reserved version, after 3a's 0310.
	if m := migration(t, "agent_conversations"); m.Version != "20261220000500" {
		t.Fatalf("version %q", m.Version)
	}
	conn, ex := scratch(t)
	if _, err := migrate.NewOrchestrator(ex, All()...).Migrate(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}
	// Running the up again changes nothing.
	up(t, ex, "agent_conversations")
	got := strs(t, conn, `SELECT indexdef FROM pg_indexes WHERE indexname LIKE 'loomplane_agent_tasks_chat_%' ORDER BY indexname`)
	want := []string{
		"CREATE INDEX loomplane_agent_tasks_chat_person ON public.loomplane_agent_tasks USING btree (org_id, requested_by, updated_at DESC) WHERE (kind = 'chat'::text)",
		"CREATE INDEX loomplane_agent_tasks_chat_session ON public.loomplane_agent_tasks USING btree (org_id, session_id, created_at) WHERE (kind = 'chat'::text)",
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("indexes %v", got)
	}
}
