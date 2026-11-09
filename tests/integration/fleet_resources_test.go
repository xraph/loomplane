//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
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
	if err := f.store.SaveInstanceResourceBinding(t.Context(), f.scope, other, otherBinding); err == nil {
		t.Fatal("two instance roles share one private access scope")
	}
	otherBinding.AccessScope = "instance_db/other_schema"
	if err := f.store.SaveInstanceResourceBinding(t.Context(), f.scope, other, otherBinding); err != nil {
		t.Fatal(err)
	}
	if otherBinding.PhysicalDeletionAllowed(other.InstanceID, 1) {
		t.Fatal("shared resource deletion allowed")
	}
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_instances SET status='retired' WHERE id=$1`, binding.InstanceID); err != nil {
		t.Fatal(err)
	}
	next, nextBinding := instanceResourceBinding(t, f, release, "c")
	nextBinding.PrincipalID = "next_role"
	if err := f.store.SaveInstanceResourceBinding(t.Context(), f.scope, next, nextBinding); !errors.Is(err, fleets.ErrConflict) {
		t.Fatal("retired instance private scope was reassigned", err)
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

func TestInstanceResourceScopeConcurrentClaim(t *testing.T) {
	f, release := instanceResourceFixture(t, "shared")
	first, firstBinding := instanceResourceBinding(t, f, release, "a")
	second, secondBinding := instanceResourceBinding(t, f, release, "b")
	secondBinding.PrincipalID = "other_role"
	results := make(chan error, 2)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for _, candidate := range []struct {
		snapshot fleets.InstanceSnapshot
		binding  fleets.ResourceBinding
	}{{first, firstBinding}, {second, secondBinding}} {
		workers.Go(func() {
			<-start
			results <- f.store.SaveInstanceResourceBinding(t.Context(), f.scope, candidate.snapshot, candidate.binding)
		})
	}
	close(start)
	workers.Wait()
	close(results)
	var saved, refused int
	for err := range results {
		if err == nil {
			saved++
		} else if errors.Is(err, fleets.ErrConflict) {
			refused++
		} else {
			t.Fatal(err)
		}
	}
	if saved != 1 || refused != 1 {
		t.Fatalf("scope claim results: saved=%d refused=%d", saved, refused)
	}
}

func TestInstanceResourceRejectsHistoricalScopeConflict(t *testing.T) {
	f, release := instanceResourceFixture(t, "shared")
	first, firstBinding := instanceResourceBinding(t, f, release, "a")
	if err := f.store.SaveInstanceResourceBinding(t.Context(), f.scope, first, firstBinding); err != nil {
		t.Fatal(err)
	}
	second, secondBinding := instanceResourceBinding(t, f, release, "b")
	secondBinding.PrincipalID = "other_role"
	insertHistoricalResourceBinding(t, f, secondBinding)
	for _, candidate := range []struct {
		snapshot fleets.InstanceSnapshot
		binding  fleets.ResourceBinding
	}{{first, firstBinding}, {second, secondBinding}} {
		if _, err := f.store.ListInstanceResourceBindings(t.Context(), f.scope, candidate.binding.InstanceID); !errors.Is(err, fleets.ErrConflict) {
			t.Fatal("ambiguous private scope remained readable", err)
		}
		if err := f.store.SaveInstanceResourceBinding(t.Context(), f.scope, candidate.snapshot, candidate.binding); !errors.Is(err, fleets.ErrConflict) {
			t.Fatal("ambiguous private scope replay succeeded", err)
		}
	}
}

func insertHistoricalResourceBinding(t *testing.T, f fleetFixture, binding fleets.ResourceBinding) {
	t.Helper()
	raw, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce a binding that the earlier admission rule allowed.
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_fleet_resource_bindings(id,org_id,project_id,fleet_id,instance_id,environment_id,name,generation,ownership_epoch,ownership,resource_id,incarnation,principal_id,endpoint_id,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, binding.ID, f.scope.OrgID, f.scope.ProjectID, f.scope.FleetID, binding.InstanceID, binding.EnvironmentID, binding.Name, binding.Generation, binding.OwnershipEpoch, binding.Ownership, binding.ResourceID, binding.Incarnation, binding.PrincipalID, binding.Credential.EndpointID, string(raw)); err != nil {
		t.Fatal(err)
	}
}

func otherOrganizationResourceFixture(t *testing.T, original fleetFixture, release fleet.TemplateRelease) (fleetFixture, fleet.TemplateRelease) {
	t.Helper()
	now := time.Now().UTC()
	for _, statement := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('other-project','other-org','Other','other','ready','usr',now(),now())`,
		`INSERT INTO loomplane_catalog_endpoints(id,org_id,name,engine,host,port,database,username,tls,created_by,created_at) VALUES('other-endpoint','other-org','Instance database','postgres','database.example.test',5432,'instance_db','other_role','verify-full','usr',now())`,
	} {
		if _, err := original.sql.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	connection := &connections.Connection{ID: "other-connection", OrgID: "other-org", Cloud: "vercel", Name: "Other", Status: connections.StatusReady, Account: "old", Details: map[string]string{"team_id": "old"}, CreatedAt: now, UpdatedAt: now}
	if err := connections.NewPgStore(original.db).CreateConnection(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	cloud := &cloudprofiles.Profile{ID: "other-cloud", OrgID: "other-org", ProjectID: "other-project", Name: "Cloud", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := cloudprofiles.NewPgStore(original.db).CreateProfile(t.Context(), cloud); err != nil {
		t.Fatal(err)
	}
	group := &fleet.Fleet{ID: uuid.NewString(), OrgID: "other-org", ProjectID: "other-project", Revision: 1, Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := original.store.CreateFleet(t.Context(), group); err != nil {
		t.Fatal(err)
	}
	scope := fleets.FleetScope{OrgID: group.OrgID, ProjectID: group.ProjectID, FleetID: group.ID}
	stage := &fleet.Stage{ID: uuid.NewString(), FleetID: group.ID, Name: "production", EnvironmentKind: "production", AllowedCloudIDs: []string{cloud.ID}, MaxInstances: 3, PolicyRevision: 1, CreatedAt: now}
	if err := original.store.CreateStage(t.Context(), scope, stage); err != nil {
		t.Fatal(err)
	}
	release.ID, release.FleetID = uuid.NewString(), group.ID
	saved, err := original.store.PublishRelease(t.Context(), scope, &release)
	if err != nil {
		t.Fatal(err)
	}
	return fleetFixture{db: original.db, store: original.store, sql: original.sql, scope: scope, stage: stage}, *saved
}

func TestInstanceResourceScopeAcrossOrganizations(t *testing.T) {
	for _, mode := range []string{"sequential", "concurrent", "historical", "principal", "owned"} {
		t.Run(mode, func(t *testing.T) {
			ownership := "shared"
			if mode == "owned" {
				ownership = "owned"
			}
			first, release := instanceResourceFixture(t, ownership)
			second, otherRelease := otherOrganizationResourceFixture(t, first, release)
			a, bindingA := instanceResourceBinding(t, first, release, "a")
			b, bindingB := instanceResourceBinding(t, second, otherRelease, "b")
			bindingB.ConnectionID = "other-connection"
			bindingB.Credential.EndpointID = "other-endpoint"
			bindingB.PrincipalID = "other_role"
			if mode == "principal" || mode == "owned" {
				bindingB.AccessScope = "instance_db/other_schema"
			}
			if mode == "principal" {
				bindingB.PrincipalID = bindingA.PrincipalID
			}
			candidates := []struct {
				fixture  fleetFixture
				snapshot fleets.InstanceSnapshot
				binding  fleets.ResourceBinding
			}{{first, a, bindingA}, {second, b, bindingB}}
			if mode == "concurrent" {
				start := make(chan struct{})
				results := make(chan error, 2)
				var workers sync.WaitGroup
				for _, candidate := range candidates {
					workers.Go(func() {
						<-start
						results <- candidate.fixture.store.SaveInstanceResourceBinding(t.Context(), candidate.fixture.scope, candidate.snapshot, candidate.binding)
					})
				}
				close(start)
				workers.Wait()
				close(results)
				var saved, refused int
				for err := range results {
					if err == nil {
						saved++
					} else if errors.Is(err, fleets.ErrConflict) {
						refused++
					} else {
						t.Fatal(err)
					}
				}
				if saved != 1 || refused != 1 {
					t.Fatalf("cross-organization scope claim results: saved=%d refused=%d", saved, refused)
				}
				return
			}
			if err := first.store.SaveInstanceResourceBinding(t.Context(), first.scope, a, bindingA); err != nil {
				t.Fatal(err)
			}
			if mode == "historical" {
				insertHistoricalResourceBinding(t, second, bindingB)
				for _, candidate := range candidates {
					if _, err := candidate.fixture.store.ListInstanceResourceBindings(t.Context(), candidate.fixture.scope, candidate.binding.InstanceID); !errors.Is(err, fleets.ErrConflict) {
						t.Fatal("cross-organization historical scope remained readable", err)
					}
					if err := candidate.fixture.store.SaveInstanceResourceBinding(t.Context(), candidate.fixture.scope, candidate.snapshot, candidate.binding); !errors.Is(err, fleets.ErrConflict) {
						t.Fatal("cross-organization historical scope replay succeeded", err)
					}
				}
				return
			}
			if err := second.store.SaveInstanceResourceBinding(t.Context(), second.scope, b, bindingB); !errors.Is(err, fleets.ErrConflict) {
				t.Fatal("cross-organization private scope was claimed", err)
			}
			if mode == "principal" || mode == "owned" {
				return
			}
			bindingB.AccessScope = "instance_db/other_schema"
			if err := second.store.SaveInstanceResourceBinding(t.Context(), second.scope, b, bindingB); err != nil {
				t.Fatal("distinct private scope refused", err)
			}
			c, bindingC := instanceResourceBinding(t, second, otherRelease, "c")
			bindingC.ConnectionID = "other-connection"
			bindingC.Credential.EndpointID = "other-endpoint"
			bindingC.ResourceID = "distinct-resource"
			bindingC.PrincipalID = "other_role"
			if err := second.store.SaveInstanceResourceBinding(t.Context(), second.scope, c, bindingC); err != nil {
				t.Fatal("distinct physical resource refused", err)
			}
		})
	}
}
