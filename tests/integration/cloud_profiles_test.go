//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/ops"
)

func TestCloudProfiles_scopesRevisionsAndDeletion(t *testing.T) {
	ctx := t.Context()
	dsn := freshDatabase(t)
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	db := pgdriver.New()
	if err := db.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, sql := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('proj_1','org_1','One','one','ready','usr',now(),now()),('proj_2','org_1','Two','two','ready','usr',now(),now()),('proj_3','org_2','Three','three','ready','usr',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('env_1','org_1','proj_1','production','production',now(),now()),('env_2','org_1','proj_2','production','production',now(),now())`,
	} {
		if _, err = conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	cs := connections.NewPgStore(db)
	now := time.Now().UTC()
	for _, c := range []*connections.Connection{{ID: "conn_1", OrgID: "org_1", Cloud: connections.GCP, Name: "one", Status: connections.StatusReady, CreatedAt: now, UpdatedAt: now}, {ID: "conn_2", OrgID: "org_2", Cloud: connections.GCP, Name: "two", Status: connections.StatusReady, CreatedAt: now, UpdatedAt: now}} {
		if err = cs.CreateConnection(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	st := cloudprofiles.NewPgStore(db)
	p := &cloudprofiles.Profile{ID: "cloud_1", OrgID: "org_1", ProjectID: "proj_1", Name: "Production", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err = st.CreateProfile(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, scope := range [][2]string{{"org_1", "proj_2"}, {"org_2", "proj_1"}} {
		if _, err = st.GetProfile(ctx, scope[0], scope[1], p.ID); !errors.Is(err, cloudprofiles.ErrNotFound) {
			t.Fatalf("scope leak: %v", err)
		}
	}
	bad := *p
	bad.ID = "bad"
	bad.OrgID = "org_2"
	if err = st.CreateProfile(ctx, &bad); !errors.Is(err, cloudprofiles.ErrNotFound) {
		t.Fatalf("cross-org project accepted: %v", err)
	}
	target := &cloudprofiles.Target{ID: "target_1", OrgID: p.OrgID, ProjectID: p.ProjectID, CloudID: p.ID, Name: "primary", DisplayName: "Primary", Provider: "cloud-run", ConnectionID: "conn_1", Region: "us-central1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err = st.CreateTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	badTarget := *target
	badTarget.ID = "target_bad"
	badTarget.ConnectionID = "conn_2"
	badTarget.Name = "other"
	if err = st.CreateTarget(ctx, &badTarget); !errors.Is(err, cloudprofiles.ErrNotFound) {
		t.Fatalf("cross-org connection accepted: %v", err)
	}
	badTarget = *target
	badTarget.ID = "target_other"
	badTarget.ProjectID = "proj_2"
	if err = st.CreateTarget(ctx, &badTarget); !errors.Is(err, cloudprofiles.ErrNotFound) {
		t.Fatalf("cross-project cloud accepted: %v", err)
	}

	for _, slug := range []string{"z-last", "a-first"} {
		other := *target
		other.ID = "target_" + slug
		other.Name = slug
		if err = st.CreateTarget(ctx, &other); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := st.ListTargets(ctx, "org_1", "proj_1", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 3 || listed[0].Name != "a-first" || listed[1].Name != "primary" || listed[2].Name != "z-last" {
		t.Fatalf("target order: %+v", listed)
	}
	for _, other := range listed {
		if other.ID != target.ID {
			if err = st.DeleteTarget(ctx, "org_1", "proj_1", p.ID, other.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	duplicate := *target
	duplicate.ID = "target_duplicate"
	if err = st.CreateTarget(ctx, &duplicate); !errors.Is(err, cloudprofiles.ErrExists) {
		t.Fatalf("duplicate target slug accepted: %v", err)
	}
	for _, scope := range [][2]string{{"org_1", "proj_2"}, {"org_2", "proj_1"}} {
		if _, err = st.GetTarget(ctx, scope[0], scope[1], p.ID, target.ID); !errors.Is(err, cloudprofiles.ErrNotFound) {
			t.Fatalf("target scope leak: %v", err)
		}
	}

	moved := *target
	moved.Revision = 2
	moved.Region = "us-east1"
	if err = st.UpdateTarget(ctx, &moved, 1); !errors.Is(err, cloudprofiles.ErrConflict) {
		t.Fatalf("metadata update changed region: %v", err)
	}
	target.Revision = 2
	target.DisplayName = "Renamed"
	if err = st.UpdateTarget(ctx, target, 1); err != nil {
		t.Fatal(err)
	}
	if err = st.UpdateTarget(ctx, target, 1); !errors.Is(err, cloudprofiles.ErrConflict) {
		t.Fatalf("stale target update: %v", err)
	}
	p.Revision = 2
	p.Name = "Renamed"
	if err = st.UpdateProfile(ctx, p, 1); err != nil {
		t.Fatal(err)
	}
	if err = st.UpdateProfile(ctx, p, 1); !errors.Is(err, cloudprofiles.ErrConflict) {
		t.Fatalf("stale profile update: %v", err)
	}
	if err = cs.DeleteConnection(ctx, "org_1", "conn_1"); !errors.Is(err, connections.ErrInUse) {
		t.Fatalf("referenced connection deleted: %v", err)
	}
	// Creating a Cloud from a project grants it to that project without
	// making it the project's default.
	assignment, err := st.ResolveCloud(ctx, "org_1", "proj_1", "")
	if err != nil || assignment != nil {
		t.Fatalf("no default Cloud: %+v %v", assignment, err)
	}
	if _, err = st.ResolveCloud(ctx, "org_1", "proj_2", p.ID); !errors.Is(err, cloudprofiles.ErrNotFound) {
		t.Fatalf("ungranted Cloud resolved: %v", err)
	}

	// Making the Cloud a project's default must serialize with target
	// deletion, including a default change that has not yet committed.
	// Acceptance locks the Cloud row before it moves the default, as here.
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SELECT 1 FROM loomplane_cloud_profiles WHERE org_id='org_1' AND id='cloud_1' FOR UPDATE`); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_cloud_project_grants SET is_default=true,revision=revision+1 WHERE org_id='org_1' AND cloud_id='cloud_1' AND project_id='proj_1'`); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	blocked, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	err = st.DeleteTarget(blocked, "org_1", "proj_1", p.ID, target.ID)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		_ = tx.Rollback(ctx)
		t.Fatalf("delete did not wait for the default change: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// A revoked grant can never be a project's default.
	if _, err = conn.Exec(ctx, `INSERT INTO loomplane_cloud_project_grants(org_id,cloud_id,project_id,target_ids,revoked,is_default) VALUES('org_1','cloud_1','proj_2','{}',true,true)`); err == nil {
		t.Fatal("revoked default grant accepted")
	}
	assignment, err = st.ResolveCloud(ctx, "org_1", "proj_1", "")
	if err != nil || assignment == nil || assignment.CloudID != p.ID || !assignment.Default || assignment.CloudRevision != p.Revision || len(assignment.Targets) != 1 || assignment.Targets[0].ID != target.ID {
		t.Fatalf("default Cloud: %+v %v", assignment, err)
	}
	if _, err = st.ResolveCloud(ctx, "org_2", "proj_1", ""); !errors.Is(err, cloudprofiles.ErrNotFound) {
		t.Fatalf("cross-org resolve: %v", err)
	}
	// A manifest names a Cloud by name; the same view comes back.
	named, err := st.ResolveNamedCloud(ctx, "org_1", "proj_1", assignment.Name)
	if err != nil || named.CloudID != p.ID || named.GrantRevision != assignment.GrantRevision || len(named.Targets) != 1 {
		t.Fatalf("named Cloud: %+v %v", named, err)
	}
	if _, err = st.ResolveNamedCloud(ctx, "org_1", "proj_1", "No such Cloud"); !errors.Is(err, cloudprofiles.ErrNotFound) {
		t.Fatalf("unknown Cloud name: %v", err)
	}
	if _, err = st.ResolveNamedCloud(ctx, "org_2", "proj_1", assignment.Name); !errors.Is(err, cloudprofiles.ErrNotFound) {
		t.Fatalf("cross-org named Cloud: %v", err)
	}
	if err = st.DeleteTarget(ctx, "org_1", "proj_1", p.ID, target.ID); !errors.Is(err, cloudprofiles.ErrInUse) {
		t.Fatalf("target of a default Cloud deleted: %v", err)
	}
	if err = st.DeleteProfile(ctx, "org_1", "proj_1", p.ID); !errors.Is(err, cloudprofiles.ErrInUse) {
		t.Fatalf("default Cloud deleted: %v", err)
	}
	if _, err = conn.Exec(ctx, `UPDATE loomplane_cloud_project_grants SET is_default=false,revision=revision+1 WHERE org_id='org_1' AND project_id='proj_1' AND is_default`); err != nil {
		t.Fatal(err)
	}

	if err = st.DeleteProfile(ctx, "org_1", "proj_1", p.ID); err != nil {
		t.Fatal(err)
	}
	if err = cs.DeleteConnection(ctx, "org_1", "conn_1"); err != nil {
		t.Fatal(err)
	}
}
