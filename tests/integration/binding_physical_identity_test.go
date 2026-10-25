//go:build integration

package integration

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/planning"
)

func TestBindingPhysicalIdentitySurvivesStoreReopen(t *testing.T) {
	ctx := t.Context()
	dsn := freshDatabase(t)
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	raw, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close(ctx)
	for _, sql := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('project','org','One','one','ready','user',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('environment','org','project','production','production',now(),now())`,
	} {
		if _, execErr := raw.Exec(ctx, sql); execErr != nil {
			t.Fatal(execErr)
		}
	}
	db := pgdriver.New()
	if openErr := db.Open(ctx, dsn); openErr != nil {
		t.Fatal(openErr)
	}
	now := time.Now().UTC()
	bound := &planning.Binding{ID: "binding", OrgID: "org", ProjectID: "project", EnvironmentID: "environment", Subject: planning.ResourceSubject, Name: "database", Provider: "rds", ExternalID: "database", PhysicalID: "db-original", OwnedResources: map[string]json.RawMessage{"zone_id": json.RawMessage(`"123"`)}, CreatedAt: now, UpdatedAt: now, Replaced: []planning.Binding{{ExternalID: "older", PhysicalID: "db-older", OwnedResources: map[string]json.RawMessage{"zone_id": json.RawMessage(`"456"`)}}}}
	if putErr := planning.NewPgStore(db).PutBinding(ctx, bound); putErr != nil {
		t.Fatal(putErr)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	reopened := pgdriver.New()
	if reopenErr := reopened.Open(ctx, dsn); reopenErr != nil {
		t.Fatal(reopenErr)
	}
	defer reopened.Close()
	st := planning.NewPgStore(reopened)
	list, err := st.ListBindings(ctx, "org", "environment")
	if err != nil || len(list) != 1 || list[0].PhysicalID != "db-original" || list[0].Replaced[0].PhysicalID != "db-older" || !bindingOwnershipJSONEqual(list[0].OwnedResources, []byte(`{"zone_id":"123"}`)) || !bindingOwnershipJSONEqual(list[0].Replaced[0].OwnedResources, []byte(`{"zone_id":"456"}`)) {
		t.Fatalf("native identity was lost on reopen: %+v %v", list, err)
	}
	for _, scenario := range []string{"omitted", "alias", "replacement", "changed"} {
		t.Run(scenario, func(t *testing.T) {
			omitted := *list[0]
			omitted.OwnedResources = nil
			if scenario == "alias" {
				omitted.ConnectionID = "alias"
			}
			if scenario == "changed" {
				omitted.OwnedResources = map[string]json.RawMessage{"zone_id": json.RawMessage(`"foreign"`)}
			}
			if scenario == "replacement" {
				omitted.PhysicalID = "replacement"
			}
			if omittedErr := st.PutBinding(ctx, &omitted); omittedErr == nil {
				t.Error("ownership cleared through store", scenario)
				if restoreErr := st.PutBinding(ctx, list[0]); restoreErr != nil {
					t.Fatal(restoreErr)
				}
			}
		})
	}
	if _, invalidErr := raw.Exec(ctx, `UPDATE loomplane_bindings SET owned_resources='[]' WHERE id='binding'`); invalidErr == nil {
		t.Fatal("database accepted non-object ownership")
	}
	original := *list[0]
	original.Replaced = nil
	list[0].Replaced = append(list[0].Replaced, original)
	list[0].PhysicalID = "db-recreated"
	list[0].OwnedResources = map[string]json.RawMessage{"zone_id": json.RawMessage(`"789"`)}
	if putErr := st.PutBinding(ctx, list[0]); putErr != nil {
		t.Fatal(putErr)
	}
	list, err = st.ListBindings(ctx, "org", "environment")
	if err != nil || len(list) != 1 || list[0].PhysicalID != "db-recreated" || !bindingOwnershipJSONEqual(list[0].OwnedResources, []byte(`{"zone_id":"789"}`)) || len(list[0].Replaced) != 2 || !bindingOwnershipJSONEqual(list[0].Replaced[1].OwnedResources, []byte(`{"zone_id":"123"}`)) {
		t.Fatalf("native identity update was not saved: %+v %v", list, err)
	}
	foreign, err := st.ListBindings(ctx, "other-org", "environment")
	if err != nil || len(foreign) != 0 {
		t.Fatal("physical identities escaped organization scope")
	}
}

func bindingOwnershipJSONEqual(owned map[string]json.RawMessage, b []byte) bool {
	a, err := json.Marshal(owned)
	if err != nil {
		return false
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}
