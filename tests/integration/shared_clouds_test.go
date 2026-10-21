//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/page"

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

func TestSharedCloudRevokedPreviewKeepsTargetsPrivate(t *testing.T) {
	st, conn, db := cloudSyncDatabase(t)
	ctx := t.Context()
	hidden, err := st.GetTarget(ctx, "org", "proj", "cloud", "target")
	if err != nil {
		t.Fatal(err)
	}
	hidden.ID, hidden.Name, hidden.Region = "never-granted-target", "private-target", "private-region"
	if err = st.CreateTarget(ctx, hidden); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetProjectGrant(ctx, "org", "cloud", "proj", []string{"target"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES('org','proj','env','cloud',1)`); err != nil {
		t.Fatal(err)
	}
	service := planning.NewService(planning.Deps{Store: planning.NewPgStore(db), Projects: projects.NewPgStore(db), CloudProfiles: st, Connections: &syncAccounts{store: connections.NewPgStore(db)}, Providers: providers.Registry(), Authz: sharedProjectAuthorizer{}})
	actor := security.Principal{OrgID: "org", UserID: "developer"}
	authorizedPreview, err := service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{EnvironmentID: "env", Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if authorizedPreview.BeforeAssignment == nil || len(authorizedPreview.BeforeAssignment.Targets) != 1 || authorizedPreview.BeforeAssignment.Targets[0].ID != "target" {
		t.Fatalf("allowed preview target missing: %+v", authorizedPreview.BeforeAssignment)
	}
	if _, err = st.SetProjectGrant(ctx, "org", "cloud", "proj", nil, true); err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{EnvironmentID: "env", Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	check := func(preview *cloudprofiles.Preview) {
		t.Helper()
		if preview.BeforeAssignment == nil || preview.BeforeAssignment.CloudID != "cloud" {
			t.Fatalf("retained assignment reference missing: %+v", preview.BeforeAssignment)
		}
		if len(preview.BeforeAssignment.Targets) != 0 {
			t.Errorf("revoked preview exposed target configurations: %+v", preview.BeforeAssignment.Targets)
		}
	}
	check(preview)
	stored, err := service.CloudPreview(ctx, actor, "proj", preview.ID)
	if err != nil {
		t.Fatal(err)
	}
	check(stored)
	stored, err = service.CloudPreview(ctx, actor, "proj", authorizedPreview.ID)
	if err != nil {
		t.Fatal(err)
	}
	check(stored)
	retained, err := st.RetainedAssignment(ctx, "org", "proj", "env")
	if err != nil || len(retained.Targets) != 2 {
		t.Fatalf("private retained comparison state lost: %+v %v", retained, err)
	}
}

func insertSharedCloudHistory(t *testing.T, conn *pgx.Conn, org, id, project string, participants []cloudprofiles.EnvironmentSnapshot, at time.Time) {
	t.Helper()
	sync := cloudprofiles.Sync{ID: id, OrgID: org, ProjectID: project, PreviewID: id + "-preview", TargetID: "target", CloudID: "cloud", Status: cloudprofiles.SyncSucceeded, CreatedAt: at, UpdatedAt: at, Error: "another project's error"}
	for _, participant := range participants {
		sync.Environments = append(sync.Environments, cloudprofiles.EnvironmentChange{ProjectID: participant.ProjectID, EnvironmentID: participant.EnvironmentID, Services: []string{participant.EnvironmentID + "-service"}})
	}
	body, err := json.Marshal(sync)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := json.Marshal(cloudprofiles.Snapshot{Environments: participants})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(t.Context(), `INSERT INTO loomplane_cloud_previews(id,org_id,project_id,revision,body,snapshot,created_at) VALUES($1,$2,$3,'digest','{}',$4,$5)`, sync.PreviewID, org, project, snapshot, at); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(t.Context(), `INSERT INTO loomplane_cloud_syncs(id,org_id,project_id,preview_id,idempotency_key,status,body,snapshot,created_at,updated_at) VALUES($1,$2,$3,$4,$1,'succeeded',$5,$6,$7,$7)`, id, org, project, sync.PreviewID, body, snapshot, at); err != nil {
		t.Fatal(err)
	}
}

type sharedHistoryAuthorizer struct{ project string }

func (a sharedHistoryAuthorizer) Require(_ context.Context, _ security.Principal, permission security.Permission, resource string) error {
	if permission == security.OrganizationCloudManage || resource != a.project {
		return security.ErrForbidden
	}
	return nil
}

func TestSharedCloudParticipantHistoryAndPagination(t *testing.T) {
	st, conn, db := cloudSyncDatabase(t)
	ctx := t.Context()
	if _, err := conn.Exec(ctx, `INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('second','org','Second','second','ready','usr',now(),now()),('third','org','Third','third','ready','usr',now(),now())`); err != nil {
		t.Fatal(err)
	}
	shared := []cloudprofiles.EnvironmentSnapshot{{ProjectID: "proj", EnvironmentID: "env"}, {ProjectID: "second", EnvironmentID: "second-env"}}
	now := time.Now().UTC().Truncate(time.Microsecond)
	insertSharedCloudHistory(t, conn, "org", "old-legacy", "second", []cloudprofiles.EnvironmentSnapshot{{EnvironmentID: "second-env"}}, now.Add(-4*time.Second))
	insertSharedCloudHistory(t, conn, "org", "compat-shared", "proj", shared, now.Add(-3*time.Second))
	insertSharedCloudHistory(t, conn, "org", "hidden-middle", "third", []cloudprofiles.EnvironmentSnapshot{{ProjectID: "third", EnvironmentID: "private-env"}}, now.Add(-2*time.Second))
	insertSharedCloudHistory(t, conn, "org", "organization-shared", "", shared, now.Add(-time.Second))
	insertSharedCloudHistory(t, conn, "org", "hidden-newest", "third", nil, now)
	service := planning.NewService(planning.Deps{Projects: projects.NewPgStore(db), CloudProfiles: st, Authz: sharedHistoryAuthorizer{project: "second"}})
	actor := security.Principal{OrgID: "org", UserID: "developer"}
	for _, id := range []string{"organization-shared", "compat-shared", "old-legacy"} {
		sync, err := service.CloudSync(ctx, actor, "second", id)
		if err != nil {
			t.Errorf("participant lookup %s: %v", id, err)
			continue
		}
		if len(sync.Environments) != 1 || sync.Environments[0].EnvironmentID != "second-env" || sync.Error != "" {
			t.Errorf("participant view: %+v", sync)
		}
	}
	for _, id := range []string{"hidden-middle", "hidden-newest"} {
		if _, err := service.CloudSync(ctx, actor, "second", id); !errors.Is(err, cloudprofiles.ErrNotFound) {
			t.Errorf("unrelated operation discovered: %s %v", id, err)
		}
	}
	if _, err := service.CloudSync(ctx, security.Principal{OrgID: "other"}, "second", "organization-shared"); !errors.Is(err, cloudprofiles.ErrNotFound) {
		t.Errorf("cross-org operation discovered: %v", err)
	}
	if _, _, err := service.CloudSyncPage(ctx, actor, "third", page.Cursor{}, 1); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("unauthorized project history: %v", err)
	}
	var cursor page.Cursor
	for i, id := range []string{"organization-shared", "compat-shared", "old-legacy"} {
		rows, next, err := service.CloudSyncPage(ctx, actor, "second", cursor, 1)
		if err != nil || len(rows) != 1 || rows[0].ID != id {
			t.Errorf("eligible page %d: %+v %v", i, rows, err)
			break
		}
		if len(rows[0].Environments) != 1 || rows[0].Environments[0].EnvironmentID != "second-env" {
			t.Errorf("page leaked participants: %+v", rows[0])
		}
		if (i == 2) != next.IsZero() {
			t.Errorf("page count exposed ineligible operations: next=%+v", next)
		}
		cursor = next
	}
	rows, err := service.CloudSyncs(ctx, actor, "second")
	if err != nil || len(rows) != 3 {
		t.Errorf("participant history: %+v %v", rows, err)
	}
	if _, _, err := st.GetSync(ctx, "org", "second", "organization-shared"); !errors.Is(err, cloudprofiles.ErrNotFound) {
		t.Errorf("read view broadened internal mutation lookup: %v", err)
	}
}

func TestSharedCloudOrganizationSyncListAPI(t *testing.T) {
	cfg, r, c, org, project := cloudAPISetup(t)
	defer r.stop()
	conn, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	insertSharedCloudHistory(t, conn, org, "existing-project-operation", project, nil, time.Now().UTC().Truncate(time.Microsecond))
	var list struct {
		Syncs []cloudprofiles.Sync `json:"syncs"`
		Next  string               `json:"next"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/clouds/syncs", nil, &list)
	if len(list.Syncs) != 1 || list.Syncs[0].ID != "existing-project-operation" || list.Next != "" {
		t.Fatalf("organization history: %+v", list)
	}
	var sync cloudprofiles.Sync
	c.must(http.StatusOK, http.MethodGet, "/api/v1/clouds/syncs/existing-project-operation", nil, &sync)
	if sync.ID != "existing-project-operation" {
		t.Fatalf("organization read: %+v", sync)
	}
}
