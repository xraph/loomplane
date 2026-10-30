//go:build integration

package migrations

import (
	"slices"
	"testing"
)

// The outbox's inbox_id came from an earlier migration, so rolling the push
// migration back must leave it, and running it again must not mind.
func TestPushOutboxDownKeepsTheEarlierInboxID(t *testing.T) {
	conn, db := scratch(t)
	exec(t, conn, `CREATE TABLE loomplane_notification_outbox(id TEXT PRIMARY KEY, org_id TEXT NOT NULL DEFAULT '', completed_at TIMESTAMPTZ, user_id TEXT NOT NULL DEFAULT '', inbox_id TEXT NOT NULL DEFAULT '')`)
	columns := func() []string {
		return strs(t, conn, `SELECT column_name FROM information_schema.columns WHERE table_name='loomplane_notification_outbox' ORDER BY column_name`)
	}
	up(t, db, "push_outbox")
	if got := columns(); !slices.Contains(got, "push_state") || !slices.Contains(got, "inbox_id") {
		t.Fatalf("after up: %v", got)
	}
	down(t, db, "push_outbox")
	got := columns()
	if !slices.Contains(got, "inbox_id") || slices.Contains(got, "push_state") || slices.Contains(got, "push_devices") || slices.Contains(got, "push_collapse") {
		t.Fatalf("after down: %v", got)
	}
	up(t, db, "push_outbox")
	if got = columns(); !slices.Contains(got, "push_state") || !slices.Contains(got, "inbox_id") {
		t.Fatalf("after the second up: %v", got)
	}
}
