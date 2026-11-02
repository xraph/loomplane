//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

func instanceResourceFixture(t *testing.T, ownership string) (fleetFixture, fleet.TemplateRelease) {
	t.Helper()
	f := newFleetFixture(t, 3)
	raw := strings.Replace(string(fleetReleaseDocument()), "  services:", "  resources:\n    database: {kind: postgres}\n  services:", 1)
	raw += "resources:\n  database:\n    ownership: " + ownership + "\n"
	if ownership != "owned" {
		raw += "    attachment_id: attachment\n    version: version-1\n"
	}
	release, err := f.service.PublishRelease(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, f.scope.FleetID, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_catalog_endpoints(id,org_id,name,engine,host,port,database,username,tls,created_by,created_at) VALUES('endpoint','org','Instance database','postgres','database.example.test',5432,'instance_db','instance_role','verify-full','usr',now())`); err != nil {
		t.Fatal(err)
	}
	return f, *release
}

func instanceResourceBinding(t *testing.T, f fleetFixture, release fleet.TemplateRelease, ref string) (fleets.InstanceSnapshot, fleets.ResourceBinding) {
	t.Helper()
	input := f.input(ref)
	input.ReleaseID = release.ID
	result, err := f.store.ReserveInstance(t.Context(), f.scope, input)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fleets.RenderInstance(release, result.Instance, input)
	if err != nil {
		t.Fatal(err)
	}
	policy := release.Template.Resources["database"]
	return *snapshot, fleets.ResourceBinding{ID: uuid.NewString(), Scope: f.scope, InstanceID: result.Instance.ID, EnvironmentID: result.Instance.EnvironmentID, Name: "database", Ownership: policy.Ownership, Generation: 1, OwnershipEpoch: 1, ResourceID: "data-resource", Incarnation: "physical-1", Provider: "vercel", ConnectionID: "old", ConnectionGeneration: 1, AttachmentID: policy.AttachmentID, AttachmentVersion: policy.Version, Credential: fleets.ResourceCredentialRef{EndpointID: "endpoint", Version: 1}, PrincipalID: "instance_role", AccessScope: "instance_db/instance_schema"}
}

func TestInstanceResourcePersistence(t *testing.T) {
	f, release := instanceResourceFixture(t, "owned")
	snapshot, binding := instanceResourceBinding(t, f, release, "a")
	if err := f.store.SaveInstanceResourceBinding(t.Context(), f.scope, snapshot, binding); err != nil {
		t.Fatal(err)
	}
	if err := fleets.NewPgStore(f.db).SaveInstanceResourceBinding(t.Context(), f.scope, snapshot, binding); err != nil {
		t.Fatal("lost-response replay", err)
	}
	changed := binding
	changed.Incarnation = "physical-2"
	if err := f.store.SaveInstanceResourceBinding(t.Context(), f.scope, snapshot, changed); err == nil {
		t.Fatal("recreated physical resource replaced receipt")
	}
	rows, err := f.store.ListInstanceResourceBindings(t.Context(), f.scope, binding.InstanceID)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	actual, _ := json.Marshal(rows[0])
	expected, _ := json.Marshal(binding)
	if string(actual) != string(expected) {
		t.Fatal("receipt changed")
	}
	for _, query := range []string{`UPDATE loomplane_fleet_resource_bindings SET body='{}'`, `DELETE FROM loomplane_fleet_resource_bindings`, `DELETE FROM loomplane_catalog_endpoints WHERE id='endpoint'`} {
		if _, err = f.sql.Exec(t.Context(), query); err == nil {
			t.Fatal("ownership history changed", query)
		}
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_instances SET ownership_epoch=ownership_epoch+1 WHERE id=$1`, binding.InstanceID); err != nil {
		t.Fatal(err)
	}
	if err = f.store.SaveInstanceResourceBinding(t.Context(), f.scope, snapshot, binding); err == nil {
		t.Fatal("stale epoch replay accepted")
	}
	rows, err = f.store.ListInstanceResourceBindings(t.Context(), f.scope, binding.InstanceID)
	if err != nil || len(rows) != 0 {
		t.Fatal("stale binding exposed", rows, err)
	}
}

func TestInstanceResourcePrincipalCannotCrossInstances(t *testing.T) {
	f, release := instanceResourceFixture(t, "shared")
	snapshot, binding := instanceResourceBinding(t, f, release, "a")
	if err := f.store.SaveInstanceResourceBinding(t.Context(), f.scope, snapshot, binding); err != nil {
		t.Fatal(err)
	}
	other, otherBinding := instanceResourceBinding(t, f, release, "b")
	if err := f.store.SaveInstanceResourceBinding(t.Context(), f.scope, other, otherBinding); err == nil {
		t.Fatal("shared credential reused across instances")
	}
	otherBinding.PrincipalID = "other_role"
	if err := f.store.SaveInstanceResourceBinding(t.Context(), f.scope, other, otherBinding); err != nil {
		t.Fatal(err)
	}
	if otherBinding.PhysicalDeletionAllowed(other.InstanceID, 1) {
		t.Fatal("shared resource deletion allowed")
	}
}

func TestInstanceResourceOwnershipCannotCrossInstances(t *testing.T) {
	f, release := instanceResourceFixture(t, "owned")
	snapshot, binding := instanceResourceBinding(t, f, release, "a")
	if err := f.store.SaveInstanceResourceBinding(t.Context(), f.scope, snapshot, binding); err != nil {
		t.Fatal(err)
	}
	other, otherBinding := instanceResourceBinding(t, f, release, "b")
	otherBinding.PrincipalID = "other_role"
	if err := f.store.SaveInstanceResourceBinding(t.Context(), f.scope, other, otherBinding); err == nil {
		t.Fatal("two instances own one physical resource")
	}
}
