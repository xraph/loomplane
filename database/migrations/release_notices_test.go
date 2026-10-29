//go:build integration

package migrations

import (
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestReleaseNoticesMigration(t *testing.T) {
	conn, ex := scratch(t)
	ctx := t.Context()
	others := migrate.NewGroup(GroupName)
	for _, step := range Group().Migrations() {
		if step.Name != "release_notices" {
			others.MustRegister(step)
		}
	}
	if _, err := migrate.NewOrchestrator(ex, others).Migrate(ctx); err != nil {
		t.Fatalf("up others: %v", err)
	}
	capture := func() []string {
		return strs(t, conn, `SELECT pg_get_functiondef('loomplane_notification_capture'::regproc)`)
	}
	before := capture()
	if len(before) != 1 {
		t.Fatalf("capture before: %v", before)
	}
	exists := func() []string {
		return strs(t, conn, `SELECT 'function' FROM pg_proc WHERE proname='loomplane_release_notice' UNION ALL SELECT 'trigger' FROM pg_trigger WHERE tgname='loomplane_release_notice' AND NOT tgisinternal`)
	}
	up(t, ex, "release_notices")
	// Running the up again changes nothing.
	up(t, ex, "release_notices")
	if got := exists(); len(got) != 2 {
		t.Fatalf("after up: %v", got)
	}
	if after := capture(); after[0] == before[0] {
		t.Fatal("up left the capture function as it was")
	}
	down(t, ex, "release_notices")
	// Running the down again changes nothing.
	down(t, ex, "release_notices")
	if got := exists(); len(got) != 0 {
		t.Fatalf("down left %v", got)
	}
	if after := capture(); len(after) != 1 || after[0] != before[0] {
		t.Fatalf("down did not restore the capture function:\n%s\nwant\n%s", after, before)
	}
}
