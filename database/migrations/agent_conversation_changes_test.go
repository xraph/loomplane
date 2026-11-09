//go:build integration

package migrations

import (
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestAgentConversationChanges(t *testing.T) {
	if m := migration(t, "agent_conversation_changes"); m.Version != "20261225000300" {
		t.Fatalf("version %q", m.Version)
	}
	conn, ex := scratch(t)
	if _, err := migrate.NewOrchestrator(ex, All()...).Migrate(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}
	// Running the up again changes nothing.
	up(t, ex, "agent_conversation_changes")

	got := strs(t, conn, `SELECT column_name||' '||data_type||' '||is_nullable FROM information_schema.columns
		WHERE table_name = 'loomplane_agent_conversations' ORDER BY ordinal_position`)
	want := []string{
		"org_id text NO", "session_id text NO", "title text YES", "pinned_at timestamp with time zone YES",
		"deleted_at timestamp with time zone YES", "updated_at timestamp with time zone NO",
	}
	if len(got) != len(want) {
		t.Fatalf("columns %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("columns %v", got)
		}
	}
	got = strs(t, conn, `SELECT column_name||' '||data_type||' '||is_nullable||' '||coalesce(column_default, '-')
		FROM information_schema.columns
		WHERE table_name = 'loomplane_agent_tasks' AND column_name IN ('input_preface', 'page_context') ORDER BY column_name`)
	if len(got) != 2 || got[0] != "input_preface text NO ''::text" || got[1] != "page_context jsonb YES -" {
		t.Fatalf("task columns %v", got)
	}
	// One row per conversation of a workspace.
	exec(t, conn, `INSERT INTO loomplane_agent_conversations (org_id, session_id, updated_at) VALUES ('o', 's1', now())`)
	if _, err := conn.Exec(t.Context(), `INSERT INTO loomplane_agent_conversations (org_id, session_id, updated_at) VALUES ('o', 's1', now())`); err == nil {
		t.Fatal("two rows for one conversation")
	}

	down(t, ex, "agent_conversation_changes")
	if got := strs(t, conn, `SELECT count(*)||'' FROM information_schema.columns
		WHERE (table_name = 'loomplane_agent_tasks' AND column_name IN ('input_preface', 'page_context'))
			OR table_name = 'loomplane_agent_conversations'`); len(got) != 1 || got[0] != "0" {
		t.Fatalf("down left columns: %v", got)
	}
}
