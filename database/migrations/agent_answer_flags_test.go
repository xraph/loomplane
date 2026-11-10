//go:build integration

package migrations

import (
	"slices"
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestAgentAnswerFlags(t *testing.T) {
	if m := migration(t, "agent_answer_flags"); m.Version != "20261226000100" {
		t.Fatalf("version %q", m.Version)
	}
	conn, ex := scratch(t)
	if _, err := migrate.NewOrchestrator(ex, All()...).Migrate(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}
	// Running the up again changes nothing.
	up(t, ex, "agent_answer_flags")

	got := strs(t, conn, `SELECT column_name||' '||data_type||' '||is_nullable FROM information_schema.columns
		WHERE table_name = 'loomplane_agent_answer_flags' ORDER BY ordinal_position`)
	want := []string{
		"id text NO", "org_id text NO", "project_id text NO", "session_id text NO", "task_id text NO",
		"reported_by text NO", "reason text NO", "note text NO", "question text NO", "answer text NO",
		"created_at timestamp with time zone NO", "updated_at timestamp with time zone NO",
		"dismissed_by text NO", "dismissed_at timestamp with time zone YES",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("columns %v", got)
	}
	got = strs(t, conn, `SELECT indexdef FROM pg_indexes
		WHERE tablename = 'loomplane_agent_answer_flags' AND indexname <> 'loomplane_agent_answer_flags_pkey'
		ORDER BY indexname`)
	if len(got) != 2 ||
		got[0] != "CREATE UNIQUE INDEX loomplane_agent_answer_flags_reporter ON public.loomplane_agent_answer_flags USING btree (task_id, reported_by)" ||
		got[1] != "CREATE INDEX loomplane_agent_answer_flags_status ON public.loomplane_agent_answer_flags USING btree (dismissed_at, created_at)" {
		t.Fatalf("indexes %v", got)
	}

	insert := `INSERT INTO loomplane_agent_answer_flags (id, org_id, session_id, task_id, reported_by, reason, question, answer,
		created_at, updated_at) VALUES `
	exec(t, conn, insert+`('aflag_1', 'o', 's1', 'atask_1', 'usr_ada', 'harmful', 'q', 'a', now(), now())`)
	// One flag per turn per person; someone else may flag it too.
	if _, err := conn.Exec(t.Context(), insert+`('aflag_2', 'o', 's1', 'atask_1', 'usr_ada', 'other', 'q', 'a', now(), now())`); err == nil {
		t.Fatal("two flags of one turn by one person")
	}
	exec(t, conn, insert+`('aflag_3', 'o', 's1', 'atask_1', 'usr_al', 'other', 'q', 'a', now(), now())`)
	if _, err := conn.Exec(t.Context(), insert+`('aflag_4', 'o', 's1', 'atask_2', 'usr_ada', 'spam', 'q', 'a', now(), now())`); err == nil {
		t.Fatal("a reason that isn't one")
	}
	if _, err := conn.Exec(t.Context(), insert+`('aflag_5', 'o', 's1', 'atask_2', '', 'other', 'q', 'a', now(), now())`); err == nil {
		t.Fatal("a flag without its reporter")
	}

	down(t, ex, "agent_answer_flags")
	if got := strs(t, conn, `SELECT count(*)||'' FROM information_schema.tables
		WHERE table_name = 'loomplane_agent_answer_flags'`); len(got) != 1 || got[0] != "0" {
		t.Fatalf("down left the table: %v", got)
	}
}
