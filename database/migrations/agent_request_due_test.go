//go:build integration

package migrations

import (
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestAgentRequestDueIndex(t *testing.T) {
	// Inside 3a's range, after project autonomy and before 3b's 0400.
	if m := migration(t, "agent_request_due"); m.Version != "20261220000310" {
		t.Fatalf("version %q", m.Version)
	}
	conn, ex := scratch(t)
	if _, err := migrate.NewOrchestrator(ex, All()...).Migrate(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}
	// Running the up again changes nothing.
	up(t, ex, "agent_request_due")
	got := strs(t, conn, `SELECT indexdef FROM pg_indexes WHERE indexname='loomplane_agent_requests_due'`)
	if len(got) != 1 || got[0] != "CREATE INDEX loomplane_agent_requests_due ON public.loomplane_agent_requests USING btree (created_at, id) WHERE (status = 'pending'::text)" {
		t.Fatalf("index %v", got)
	}
}
