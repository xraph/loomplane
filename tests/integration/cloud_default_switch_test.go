//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// defaultSwitchFixture puts each environment's api on the Cloud's default
// (the old account) and leaves web on the frontend target.
func defaultSwitchFixture(t *testing.T) *syncFixture {
	t.Helper()
	f := newSyncFixture(t)
	f.executor.failStage = false
	ctx := t.Context()
	if _, err := f.conn.Exec(ctx, `UPDATE loomplane_cloud_profiles SET default_provider='vercel',default_connection_id='old',revision=2 WHERE id='cloud'`); err != nil {
		t.Fatal(err)
	}
	for _, environment := range []string{"env", "stage"} {
		id := "binding-" + environment + "-api"
		var raw []byte
		if err := f.conn.QueryRow(ctx, `SELECT config FROM loomplane_bindings WHERE id=$1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var cfg planning.ServiceConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		cfg.CloudPlacement = &planning.CloudPlacement{Default: true, CloudID: "cloud", CloudRevision: 2, AssignmentRevision: 1, Provider: "vercel", ConnectionID: "old", AccountIdentity: connections.AccountIdentity("vercel", "old", map[string]string{"team_id": "old"})}
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.conn.Exec(ctx, `UPDATE loomplane_bindings SET config=$2 WHERE id=$1`, id, raw); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func bindingConnection(t *testing.T, f *syncFixture, id string) string {
	t.Helper()
	var connection string
	if err := f.conn.QueryRow(t.Context(), `SELECT connection_id FROM loomplane_bindings WHERE id=$1`, id).Scan(&connection); err != nil {
		t.Fatal(err)
	}
	return connection
}

func cloudDefault(t *testing.T, f *syncFixture) (string, int64) {
	t.Helper()
	var connection string
	var revision int64
	if err := f.conn.QueryRow(t.Context(), `SELECT coalesce(default_connection_id,''),revision FROM loomplane_cloud_profiles WHERE id='cloud'`).Scan(&connection, &revision); err != nil {
		t.Fatal(err)
	}
	return connection, revision
}

func TestCloudDefaultSwitchMovesOnlyDefaultPlacedServices(t *testing.T) {
	f := defaultSwitchFixture(t)
	ctx := orchestration.CapacityContext(t.Context())
	service := f.service
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	heartbeat := func([]byte) error { return nil }

	// Editing the default directly would move serving services without review.
	profile, err := f.store.GetProfile(ctx, "org", "", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	profile.DefaultPlacement, profile.Revision = &cloudprofiles.DefaultPlacement{Provider: "vercel", ConnectionID: "new"}, 3
	if err = f.store.UpdateProfile(ctx, profile, 2); !errors.Is(err, cloudprofiles.ErrInUse) {
		t.Fatalf("direct default change on a used Cloud: %v", err)
	}

	for name, in := range map[string]cloudprofiles.ChangeInput{
		"stale revision":   {CloudID: "cloud", Default: true, Revision: 1, Provider: "vercel", ConnectionID: "new"},
		"unchanged":        {CloudID: "cloud", Default: true, Revision: 2, Provider: "vercel", ConnectionID: "old"},
		"also a target":    {CloudID: "cloud", Default: true, TargetID: "target", Revision: 2, Provider: "vercel", ConnectionID: "new"},
		"no connection":    {CloudID: "cloud", Default: true, Revision: 2, Provider: "vercel"},
		"connection alone": {CloudID: "cloud", Default: true, Revision: 2, ConnectionID: "new"},
	} {
		if _, err = service.PreviewCloudChange(ctx, actor, "proj", in); err == nil {
			t.Fatalf("%s preview accepted", name)
		}
	}

	in := cloudprofiles.ChangeInput{CloudID: "cloud", Default: true, Revision: 2, Provider: "vercel", ConnectionID: "new"}
	preview, err := service.PreviewCloudChange(ctx, actor, "proj", in)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Blockers) > 0 {
		t.Fatalf("preview blocked: %v", preview.Blockers)
	}
	if preview.BeforeDefault == nil || preview.BeforeDefault.ConnectionID != "old" || preview.AfterDefault == nil || preview.AfterDefault.ConnectionID != "new" || len(preview.Environments) != 2 {
		t.Fatalf("review: %+v", preview)
	}
	for _, env := range preview.Environments {
		if !slices.Contains(env.Services, "api") || env.Summary.Replace != 1 {
			t.Fatalf("%s should replace only the default-placed api: %+v", env.Environment, env)
		}
	}

	sync, err := service.ApplyCloudChange(ctx, actor, "proj", preview.ID, preview.Revision, "default-switch")
	if err != nil {
		t.Fatal(err)
	}
	if !sync.Default || sync.TargetID != "" || sync.AfterDefault == nil || sync.AfterDefault.ConnectionID != "new" {
		t.Fatalf("sync: %+v", sync)
	}
	if connection, revision := cloudDefault(t, f); connection != "new" || revision != 3 {
		t.Fatalf("acceptance left the default at %s revision %d", connection, revision)
	}
	// One switch per Cloud at a time, and no environment joins it meanwhile.
	if _, err = f.conn.Exec(ctx, `INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('late','org','proj','late','preview',now(),now())`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.conn.Exec(ctx, `INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES('org','proj','late','cloud',1)`); err == nil {
		t.Fatal("an environment joined the Cloud during its default switch")
	}
	again, err := service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", Default: true, Revision: 3, Provider: "vercel", ConnectionID: "old"})
	if err == nil && len(again.Blockers) == 0 {
		if _, err = service.ApplyCloudChange(ctx, actor, "proj", again.ID, again.Revision, "second-switch"); err == nil {
			t.Fatal("a second default switch ran beside the first")
		}
	}

	if err = service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	routing, err := service.CloudSync(ctx, actor, "proj", sync.ID)
	if err != nil || routing.Status != cloudprofiles.SyncRouting {
		t.Fatalf("routing state %+v %v", routing, err)
	}
	for _, environment := range []string{"env", "stage"} {
		if _, err = service.CompleteCloudRouting(ctx, actor, "proj", sync.ID, environment, "fixture routing verified"); err != nil {
			t.Fatal(err)
		}
		if err = service.ReconcileCloudSync(ctx, "org", "proj", sync.ID, heartbeat); err != nil {
			t.Fatal(err)
		}
	}
	done, err := service.CloudSync(ctx, actor, "proj", sync.ID)
	if err != nil || done.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("completion: %+v %v", done, err)
	}
	for _, environment := range []string{"env", "stage"} {
		if got := bindingConnection(t, f, "binding-"+environment+"-api"); got != "new" {
			t.Fatalf("%s api stayed on %s", environment, got)
		}
		if got := bindingConnection(t, f, "binding-"+environment+"-web"); got != "old" {
			t.Fatalf("%s web left its target for %s", environment, got)
		}
	}

	// Revert reviews the old default back through the same path.
	revert, err := service.PreviewCloudRevert(ctx, actor, "proj", sync.ID)
	if err != nil || len(revert.Blockers) > 0 {
		t.Fatalf("revert review: %+v %v", revert, err)
	}
	if revert.BeforeDefault == nil || revert.BeforeDefault.ConnectionID != "new" || revert.AfterDefault == nil || revert.AfterDefault.ConnectionID != "old" {
		t.Fatalf("revert direction: %+v", revert)
	}
	if _, err = service.ApplyCloudRevert(ctx, actor, "proj", revert.ID, revert.Revision, "default-revert"); err != nil {
		t.Fatal(err)
	}
	if connection, revision := cloudDefault(t, f); connection != "old" || revision != 4 {
		t.Fatalf("revert left the default at %s revision %d", connection, revision)
	}
}

func TestCloudDefaultSwitchVisibilityAndClearing(t *testing.T) {
	f := defaultSwitchFixture(t)
	ctx := t.Context()
	actor := security.Principal{OrgID: "org", UserID: "usr"}

	// Clearing a default that serving services use leaves them nowhere to go.
	clearing, err := f.service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", Default: true, Revision: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(clearing.Blockers) == 0 || clearing.AfterDefault != nil {
		t.Fatalf("clearing a used default was not blocked: %+v", clearing)
	}
	if _, err = f.service.ApplyCloudChange(ctx, actor, "proj", clearing.ID, clearing.Revision, "clear"); err == nil {
		t.Fatal("blocked clear accepted")
	}

	// Default switches are shared: only an organization Cloud manager reviews them.
	f.auth.denied = true
	if _, err = f.service.PreviewCloudChange(ctx, actor, "proj", cloudprofiles.ChangeInput{CloudID: "cloud", Default: true, Revision: 2, Provider: "vercel", ConnectionID: "new"}); err == nil {
		t.Fatal("a denied caller reviewed a default switch")
	}
}
