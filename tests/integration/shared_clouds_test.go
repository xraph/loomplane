//go:build integration

package integration

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
)

func TestSharedCloudGrantIsolationAndProjectRemoval(t *testing.T) {
	st, conn, _ := cloudSyncDatabase(t)
	ctx := t.Context()
	for _, sql := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('second','org','Second','second','ready','usr',now(),now()),('foreign','other','Foreign','foreign','ready','usr',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('second-env','org','second','production','production',now(),now())`,
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	grants, ok := any(st).(interface {
		SetProjectGrant(context.Context, string, string, string, []string, bool) (int64, error)
	})
	if !ok {
		t.Fatal("organization Cloud grant persistence is missing")
	}
	if _, err := st.GetProfile(ctx, "org", "second", "cloud"); !errors.Is(err, cloudprofiles.ErrNotFound) {
		t.Fatalf("ungranted Cloud: %v", err)
	}
	revision, err := grants.SetProjectGrant(ctx, "org", "cloud", "second", []string{"target"}, false)
	if err != nil || revision != 1 {
		t.Fatalf("grant: %d %v", revision, err)
	}
	if _, err = st.GetTarget(ctx, "org", "second", "cloud", "target"); err != nil {
		t.Fatal(err)
	}
	hidden, err := st.GetTarget(ctx, "org", "proj", "cloud", "target")
	if err != nil {
		t.Fatal(err)
	}
	hidden.ID, hidden.Name = "hidden-target", "hidden"
	if err = st.CreateTarget(ctx, hidden); err != nil {
		t.Fatal(err)
	}
	if _, err = st.GetTarget(ctx, "org", "second", "cloud", hidden.ID); !errors.Is(err, cloudprofiles.ErrNotFound) {
		t.Fatalf("target grant leaked: %v", err)
	}
	allowed, err := st.ListTargets(ctx, "org", "second", "cloud")
	if err != nil || len(allowed) != 1 || allowed[0].ID != "target" {
		t.Fatalf("target grant discovery: %+v %v", allowed, err)
	}

	if _, err = grants.SetProjectGrant(ctx, "org", "cloud", "foreign", nil, false); !errors.Is(err, cloudprofiles.ErrNotFound) {
		t.Fatalf("cross organization grant: %v", err)
	}
	if _, err = conn.Exec(ctx, `INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES('org','second','second-env','cloud',1)`); err != nil {
		t.Fatal(err)
	}
	assignment, err := st.Resolve(ctx, "org", "second", "second-env")
	if err != nil || assignment == nil || len(assignment.Targets) != 1 {
		t.Fatalf("shared assignment: %+v %v", assignment, err)
	}
	revision, err = grants.SetProjectGrant(ctx, "org", "cloud", "second", nil, true)
	if err != nil || revision != 2 {
		t.Fatalf("revoke: %d %v", revision, err)
	}
	if _, err = st.Resolve(ctx, "org", "second", "second-env"); !errors.Is(err, cloudprofiles.ErrNotFound) {
		t.Fatalf("revoked placement resolved: %v", err)
	}
	var count int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_cloud_assignments WHERE project_id='second'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("revocation removed assignment: %d %v", count, err)
	}
	if _, err = conn.Exec(ctx, `DELETE FROM loomplane_projects WHERE id='proj'`); err != nil {
		t.Fatal(err)
	}
	if _, err = st.GetProfile(ctx, "org", "", "cloud"); err != nil {
		t.Fatalf("project removal deleted shared Cloud: %v", err)
	}
}

func TestSharedCloudPreviewParticipantsAndRevocation(t *testing.T) {
	st, conn, db := cloudSyncDatabase(t)
	ctx := t.Context()
	for _, sql := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('second','org','Second','second','ready','usr',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('second-env','org','second','production','production',now(),now())`,
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.SetProjectGrant(ctx, "org", "cloud", "second", []string{"target"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES('org','proj','env','cloud',1),('org','second','second-env','cloud',1)`); err != nil {
		t.Fatal(err)
	}
	service := planning.NewService(planning.Deps{Store: planning.NewPgStore(db), Projects: projects.NewPgStore(db), CloudProfiles: st, Connections: &syncAccounts{store: connections.NewPgStore(db)}, Providers: providers.Registry(), Authz: &syncAuth{}})
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	preview, err := service.PreviewCloudChange(ctx, actor, "", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "target", Revision: 1, Provider: "vercel", ConnectionID: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Environments) != 2 || preview.Environments[0].ProjectID != "proj" || preview.Environments[1].ProjectID != "second" {
		t.Fatalf("participants: %+v", preview.Environments)
	}
	if _, err = st.SetProjectGrant(ctx, "org", "cloud", "second", []string{"target"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err = service.ApplyCloudChange(ctx, actor, "", preview.ID, preview.Revision, "stale-grant"); !errors.Is(err, cloudprofiles.ErrConflict) {
		t.Fatalf("stale grant accepted: %v", err)
	}
	preview, err = service.PreviewCloudChange(ctx, actor, "", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "target", Revision: 1, Provider: "vercel", ConnectionID: "new"})
	if err != nil {
		t.Fatal(err)
	}
	sync, err := service.ApplyCloudChange(ctx, actor, "", preview.ID, preview.Revision, "shared-change")
	if err != nil {
		t.Fatal(err)
	}
	if _, assignErr := conn.Exec(ctx, `INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES('org','proj','stage','cloud',1)`); assignErr == nil || !strings.Contains(assignErr.Error(), "unfinished migration") {
		t.Fatalf("new participant joined accepted sync: %v", assignErr)
	}
	_, snapshot, err := st.GetSync(ctx, "org", "", sync.ID)
	if err != nil || !snapshot.Shared || len(snapshot.Grants) != 2 {
		t.Fatalf("stored scope: %+v %v", snapshot, err)
	}
	for _, env := range []struct{ project, id string }{{"proj", "env"}, {"second", "second-env"}} {
		if _, _, err = st.SyncForParticipant(ctx, "org", env.project, env.id, sync.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err = st.SyncForParticipant(ctx, "org", "proj", "second-env", sync.ID); !errors.Is(err, cloudprofiles.ErrNotFound) {
		t.Fatalf("cross-project operation lookup: %v", err)
	}
	if _, err = st.SetProjectGrant(ctx, "org", "cloud", "second", nil, true); err != nil {
		t.Fatal(err)
	}
	if err = st.ValidateGrants(ctx, "org", snapshot.Grants); !errors.Is(err, cloudprofiles.ErrConflict) {
		t.Fatalf("revoked execution: %v", err)
	}
}

func TestSharedCloudLegacySyncScope(t *testing.T) {
	st, _, _ := cloudSyncDatabase(t)
	ctx := t.Context()
	p, snapshot, sync := persistAssignmentPreview(t, st, "legacy", "cloud", 0)
	accepted, err := st.Accept(ctx, p, snapshot, sync, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.SyncForParticipant(ctx, "org", "proj", "env", accepted.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.SyncForParticipant(ctx, "org", "second", "env", accepted.ID); !errors.Is(err, cloudprofiles.ErrNotFound) {
		t.Fatalf("legacy participant broadened: %v", err)
	}
	_, stored, err := st.GetSync(ctx, "org", "proj", accepted.ID)
	if err != nil || stored.Shared || stored.Environments[0].ProjectID != "" {
		t.Fatalf("legacy snapshot changed: %+v %v", stored, err)
	}
}

func TestSharedCloudMigrationPreservesInitialGrant(t *testing.T) {
	ctx := t.Context()
	dsn := freshDatabase(t)
	legacy := migrate.NewGroup(migrations.GroupName)
	for _, migration := range migrations.Group().Migrations() {
		if migration.Name != "shared_clouds" {
			legacy.MustRegister(migration)
		}
	}
	if err := ops.Migrate(ctx, dsn, []*migrate.Group{legacy}); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, sql := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('original','org','Original','original','ready','usr',now(),now()),('second','org','Second','second','ready','usr',now(),now())`,
		`INSERT INTO loomplane_cloud_profiles(id,org_id,project_id,name,revision,created_at,updated_at) VALUES('cloud-original','org','original','Production',1,now(),now()),('cloud-second','org','second','Production',1,now(),now())`,
		`INSERT INTO loomplane_cloud_previews(id,org_id,project_id,revision,body,snapshot,created_at) VALUES('legacy-preview','org','original','digest','{}','{"environments":[{"environment_id":"old-env"}]}',now())`,
		`INSERT INTO loomplane_cloud_syncs(id,org_id,project_id,preview_id,idempotency_key,status,body,snapshot,created_at,updated_at) VALUES('legacy-sync','org','original','legacy-preview','legacy','running','{"id":"legacy-sync","org_id":"org","project_id":"original","status":"running"}','{"environments":[{"environment_id":"old-env"}]}',now(),now())`,
	} {
		if _, err = conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	if err = ops.Migrate(ctx, dsn, []*migrate.Group{migrations.Group()}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_cloud_project_grants WHERE cloud_id='cloud-original' AND project_id='original' AND revision=1 AND NOT revoked`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("initial grant: %d %v", count, err)
	}
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_cloud_project_grants WHERE cloud_id='cloud-original'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration broadened grant: %d %v", count, err)
	}
	var cloudRevision int64
	if err = conn.QueryRow(ctx, `SELECT revision FROM loomplane_cloud_profiles WHERE id='cloud-original'`).Scan(&cloudRevision); err != nil || cloudRevision != 1 {
		t.Fatalf("migration invalidated accepted revision: %d %v", cloudRevision, err)
	}
	var name, project, snapshot string
	if err = conn.QueryRow(ctx, `SELECT name,project_id FROM loomplane_cloud_profiles WHERE id='cloud-original'`).Scan(&name, &project); err != nil || project != "original" || !strings.Contains(name, "original") {
		t.Fatalf("identity or qualified name: %s %s %v", name, project, err)
	}
	if err = conn.QueryRow(ctx, `SELECT snapshot::text FROM loomplane_cloud_syncs WHERE id='legacy-sync'`).Scan(&snapshot); err != nil || strings.Contains(snapshot, "project_id") || strings.Contains(snapshot, "shared") {
		t.Fatalf("legacy participants rewritten: %s %v", snapshot, err)
	}
}

func TestSharedCloudCleanupSurvivesGrantRevocation(t *testing.T) {
	f := newSyncFixture(t)
	f.executor.failStage = false
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	ctx := t.Context()
	preview, err := f.service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "target", Revision: 1, Provider: "vercel", ConnectionID: "new"})
	if err != nil {
		t.Fatal(err)
	}
	sync, err := f.service.ApplyCloudChange(ctx, actor, "proj", preview.ID, preview.Revision, "cleanup-revocation")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	sync, _, err = f.store.GetSync(ctx, "org", "proj", sync.ID)
	if err != nil || sync.Status != cloudprofiles.SyncRouting {
		t.Fatalf("routing checkpoint: %+v %v", sync, err)
	}
	if _, err = f.store.SetProjectGrant(ctx, "org", "cloud", "proj", nil, true); err != nil {
		t.Fatal(err)
	}
	for _, env := range sync.Environments {
		if !env.ExternalRouting {
			continue
		}
		if _, err = f.service.CompleteCloudRouting(ctx, actor, "proj", sync.ID, env.EnvironmentID, "Verified the saved destination"); err != nil {
			t.Fatal(err)
		}
		if err = f.service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, func([]byte) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	sync, _, err = f.store.GetSync(ctx, "org", "proj", sync.ID)
	if err != nil || sync.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("cleanup blocked by revoked placement: %+v %v", sync, err)
	}
	if f.executor.badSecret {
		t.Fatal("cleanup changed applied secret scope")
	}
}

func TestSharedCloudOrganizationAPI(t *testing.T) {
	_, r, c, _, project := cloudAPISetup(t)
	defer r.stop()
	var cloud cloudprofiles.Profile
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/clouds", cloudprofiles.CreateInput{Name: "Organization production"}, &cloud)
	if cloud.ProjectID != "" {
		t.Fatalf("new Cloud has project owner: %+v", cloud)
	}
	var list struct {
		Clouds []cloudprofiles.Profile `json:"clouds"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project+"/clouds", nil, &list)
	if len(list.Clouds) != 0 {
		t.Fatal("ungranted Cloud discovered")
	}
	var grant cloudprofiles.ProjectGrant
	c.must(http.StatusOK, http.MethodPut, "/api/v1/clouds/"+cloud.ID+"/projects/"+project, map[string]any{"target_ids": []string{}}, &grant)
	if grant.Revision != 1 || grant.Revoked {
		t.Fatalf("grant: %+v", grant)
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project+"/clouds", nil, &list)
	if len(list.Clouds) != 1 || list.Clouds[0].ID != cloud.ID {
		t.Fatal("granted Cloud missing")
	}
	c.must(http.StatusOK, http.MethodDelete, "/api/v1/clouds/"+cloud.ID+"/projects/"+project, nil, &grant)
	if grant.Revision != 2 || !grant.Revoked {
		t.Fatalf("revocation: %+v", grant)
	}
	c.must(http.StatusNotFound, http.MethodGet, "/api/v1/projects/"+project+"/clouds/"+cloud.ID, nil, nil)
	c.must(http.StatusOK, http.MethodGet, "/api/v1/clouds/"+cloud.ID, nil, &cloud)
}

type sharedProjectAuthorizer struct{}

func (sharedProjectAuthorizer) Require(_ context.Context, _ security.Principal, permission security.Permission, _ string) error {
	if permission == security.OrganizationCloudManage {
		return security.ErrForbidden
	}
	return nil
}
func TestSharedCloudProjectCannotManageSharedInfrastructure(t *testing.T) {
	st, _, db := cloudSyncDatabase(t)
	actor := security.Principal{OrgID: "org", UserID: "developer"}
	auth := sharedProjectAuthorizer{}
	config := cloudprofiles.NewService(cloudprofiles.Deps{Store: st, Projects: projects.NewPgStore(db), Authz: auth})
	if _, err := config.GrantProject(t.Context(), actor, "cloud", "proj", nil); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("project member granted Cloud: %v", err)
	}
	service := planning.NewService(planning.Deps{Store: planning.NewPgStore(db), Projects: projects.NewPgStore(db), CloudProfiles: st, Connections: &syncAccounts{store: connections.NewPgStore(db)}, Providers: providers.Registry(), Authz: auth})
	if _, err := service.PreviewCloudChange(t.Context(), actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", TargetID: "target", Revision: 1, Provider: "vercel", ConnectionID: "new"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("project member changed shared target: %v", err)
	}
	if _, err := service.PreviewCloudChange(t.Context(), actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", EnvironmentID: "env", Revision: 0}); err != nil {
		t.Fatalf("project release authority rejected: %v", err)
	}
}
