//go:build integration

package integration

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	pgmigrate "github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	"github.com/xraph/loomplane/internal/cloudinfra"
)

const cloudCapacityEventsMigration = "20261120000100"

type capacityEvent struct {
	project, entity string
	data            map[string]json.RawMessage
}

// capacityEventsAfter returns the cloud_capacity.updated events the
// fixture org appended after sequence mark, oldest first.
func capacityEventsAfter(t *testing.T, sql *pgx.Conn, mark int64) []capacityEvent {
	t.Helper()
	rows, err := sql.Query(t.Context(), `SELECT project_id,entity_id,data FROM loomplane_events WHERE org_id='org' AND type='cloud_capacity.updated' AND sequence>$1 ORDER BY sequence`, mark)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []capacityEvent{}
	for rows.Next() {
		var e capacityEvent
		var raw []byte
		if err = rows.Scan(&e.project, &e.entity, &raw); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &e.data); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func eventMark(t *testing.T, sql *pgx.Conn) int64 {
	t.Helper()
	var mark int64
	if err := sql.QueryRow(t.Context(), `SELECT coalesce(max(sequence),0) FROM loomplane_events WHERE org_id='org'`).Scan(&mark); err != nil {
		t.Fatal(err)
	}
	return mark
}

// onlyChangeFields fails unless the payload holds exactly the CapacityChange
// fields: never the policy body, the approver or a saved authority.
func onlyChangeFields(t *testing.T, e capacityEvent, want ...string) {
	t.Helper()
	keys := make([]string, 0, len(e.data))
	for k := range e.data {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	slices.Sort(want)
	if !slices.Equal(keys, want) {
		t.Fatalf("payload keys %v, want %v", keys, want)
	}
	if e.project != "" || e.entity != "cloud" {
		t.Fatalf("event scope project=%q entity=%q", e.project, e.entity)
	}
}

func TestCapacityEventsOnPolicyAndExpansion(t *testing.T) {
	f, _, d := expansionFixture(t, true, 0)
	ctx := t.Context()

	mark := eventMark(t, f.sql)
	policy, err := f.svc.GetCapacityPolicy(ctx, f.p, "cloud")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := f.svc.SaveCapacityPolicy(ctx, f.p, "cloud", cloudinfra.CapacityPolicyInput{ExpectedRevision: policy.Revision, Policy: *policy})
	if err != nil {
		t.Fatal(err)
	}
	events := capacityEventsAfter(t, f.sql, mark)
	if len(events) != 1 {
		t.Fatalf("saving a policy appended %d events", len(events))
	}
	onlyChangeFields(t, events[0], "org_id", "cloud_id", "kind", "entity_id", "revision")
	var change cloudinfra.CapacityChange
	raw, err := json.Marshal(events[0].data)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &change); err != nil {
		t.Fatal(err)
	}
	if change != (cloudinfra.CapacityChange{OrgID: "org", CloudID: "cloud", Kind: "policy", EntityID: "cloud", Revision: saved.Revision}) {
		t.Fatalf("policy change %+v", change)
	}

	e := ensureExpansion(t, f, d)
	reconcileExpansion(t, f)
	mark = eventMark(t, f.sql)
	approved, err := f.svc.ApproveExpansion(ctx, f.p, "cloud", e.ID, e.Revision)
	if err != nil {
		t.Fatal(err)
	}
	// Approval records the approver, then starts provisioning: two
	// revisions, one event each. Recording the approval's principal changes
	// no field a subscriber sees, so it appends nothing.
	events = capacityEventsAfter(t, f.sql, mark)
	if len(events) != 2 {
		t.Fatalf("approving an expansion appended %d events, want 2: %+v", len(events), events)
	}
	previous := e.Revision
	for _, event := range events {
		onlyChangeFields(t, event, "org_id", "cloud_id", "kind", "entity_id", "revision", "status")
		raw, err = json.Marshal(event.data)
		if err != nil {
			t.Fatal(err)
		}
		change = cloudinfra.CapacityChange{}
		if err = json.Unmarshal(raw, &change); err != nil {
			t.Fatal(err)
		}
		if change.Kind != "expansion" || change.EntityID != e.ID || change.Revision <= previous {
			t.Fatalf("approval events repeat or reorder a revision: %+v", events)
		}
		previous = change.Revision
	}
	if change != (cloudinfra.CapacityChange{OrgID: "org", CloudID: "cloud", Kind: "expansion", EntityID: e.ID, Revision: approved.Revision, Status: approved.Status}) {
		t.Fatalf("last expansion change %+v, approved %+v", change, approved)
	}
}

func TestCloudCapacityEventsMigrationRoundTrip(t *testing.T) {
	_, sql, db := cloudSyncDatabase(t)
	ctx := t.Context()
	m := federationMigration(t, cloudCapacityEventsMigration)

	if err := m.Down(ctx, pgmigrate.New(db)); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := sql.QueryRow(ctx, `SELECT (SELECT count(*) FROM pg_trigger WHERE tgname='loomplane_cloud_capacity_event')+(SELECT count(*) FROM pg_proc WHERE proname='loomplane_cloud_capacity_event_trigger')`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("down left %d triggers and functions: %v", left, err)
	}
	mark := eventMark(t, sql)
	if _, err := sql.Exec(ctx, `INSERT INTO loomplane_cloud_capacity_policies(org_id,cloud_id,revision,body) VALUES('org','cloud',1,'{}')`); err != nil {
		t.Fatal(err)
	}
	if got := capacityEventsAfter(t, sql, mark); len(got) != 0 {
		t.Fatalf("downgraded schema appended %d events", len(got))
	}

	if err := m.Up(ctx, pgmigrate.New(db)); err != nil {
		t.Fatal(err)
	}
	if err := m.Up(ctx, pgmigrate.New(db)); err != nil {
		t.Fatal("upgrade cannot replay", err)
	}
	mark = eventMark(t, sql)
	if _, err := sql.Exec(ctx, `UPDATE loomplane_cloud_capacity_policies SET revision=2,body='{"enabled":false}' WHERE org_id='org' AND cloud_id='cloud'`); err != nil {
		t.Fatal(err)
	}
	// Rewriting the same row, or a column the payload doesn't carry, is
	// not a change a subscriber can see.
	if _, err := sql.Exec(ctx, `UPDATE loomplane_cloud_capacity_policies SET revision=2 WHERE org_id='org' AND cloud_id='cloud'`); err != nil {
		t.Fatal(err)
	}
	if _, err := sql.Exec(ctx, `UPDATE loomplane_cloud_capacity_policies SET authority='{"user_id":"other"}' WHERE org_id='org' AND cloud_id='cloud'`); err != nil {
		t.Fatal(err)
	}
	got := capacityEventsAfter(t, sql, mark)
	if len(got) != 1 || string(got[0].data["revision"]) != "2" || string(got[0].data["kind"]) != `"policy"` {
		t.Fatalf("upgraded schema events %+v", got)
	}
	onlyChangeFields(t, got[0], "org_id", "cloud_id", "kind", "entity_id", "revision")
}
