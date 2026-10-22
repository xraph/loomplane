//go:build integration

package integration

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/cloudprofiles"
)

func TestCloudTagsAndDefaultPlacementPersistence(t *testing.T) {
	st, conn, _ := cloudSyncDatabase(t)
	ctx := t.Context()
	now := time.Now().UTC()

	target, err := st.GetTarget(ctx, "org", "", "cloud", "target")
	if err != nil || target.Tags == nil || len(target.Tags) != 0 {
		t.Fatalf("untagged target should read an empty list: %+v %v", target, err)
	}
	target.Tags, target.Revision = []string{"eu", "web"}, 2
	if err = st.UpdateTarget(ctx, target, 1); err != nil {
		t.Fatal(err)
	}
	tagged := &cloudprofiles.Target{ID: "gpu", OrgID: "org", ProjectID: "proj", CloudID: "cloud", Name: "gpu", DisplayName: "GPU", Provider: "vercel", ConnectionID: "new", Tags: []string{"gpu"}, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err = st.CreateTarget(ctx, tagged); err != nil {
		t.Fatal(err)
	}
	targets, err := st.ListTargets(ctx, "org", "proj", "cloud")
	if err != nil || len(targets) != 2 || !slices.Equal(targets[0].Tags, []string{"eu", "web"}) || !slices.Equal(targets[1].Tags, []string{"gpu"}) {
		t.Fatalf("tags did not round trip: %+v %v", targets, err)
	}

	// The first default may be set while the Cloud is in use: the services
	// it places had no placement before.
	if _, err = conn.Exec(ctx, `INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES('org','proj','env','cloud',1)`); err != nil {
		t.Fatal(err)
	}
	profile, err := st.GetProfile(ctx, "org", "", "cloud")
	if err != nil || profile.DefaultPlacement != nil {
		t.Fatalf("Cloud gained a default: %+v %v", profile, err)
	}
	profile.DefaultPlacement = &cloudprofiles.DefaultPlacement{Provider: "vercel", ConnectionID: "old", Region: "iad1"}
	profile.Revision = 2
	if err = st.UpdateProfile(ctx, profile, 1); err != nil {
		t.Fatal(err)
	}
	a, err := st.Resolve(ctx, "org", "proj", "env")
	if err != nil || a.DefaultPlacement == nil || *a.DefaultPlacement != *profile.DefaultPlacement || a.CloudRevision != 2 {
		t.Fatalf("assignment default: %+v %v", a, err)
	}
	for _, at := range a.Targets {
		if at.Tags == nil {
			t.Fatalf("assigned target lost tags: %+v", at)
		}
	}

	// Renaming keeps the default; replacing or clearing it would move serving services.
	profile.Name, profile.Revision = "Renamed", 3
	if err = st.UpdateProfile(ctx, profile, 2); err != nil {
		t.Fatalf("rename of an assigned Cloud with a default: %v", err)
	}
	for _, next := range []*cloudprofiles.DefaultPlacement{nil, {Provider: "vercel", ConnectionID: "new"}, {Provider: "vercel", ConnectionID: "old", Region: "sfo1"}} {
		changed := *profile
		changed.DefaultPlacement, changed.Revision = next, 4
		if err = st.UpdateProfile(ctx, &changed, 3); !errors.Is(err, cloudprofiles.ErrInUse) {
			t.Fatalf("default change on an assigned Cloud: %v", err)
		}
	}

	// A grant limited to chosen targets reaches those targets, not the default.
	grants := any(st).(interface {
		SetProjectGrant(context.Context, string, string, string, []string, bool) (int64, error)
	})
	if _, err = grants.SetProjectGrant(ctx, "org", "cloud", "proj", []string{"gpu"}, false); err != nil {
		t.Fatal(err)
	}
	a, err = st.Resolve(ctx, "org", "proj", "env")
	if err != nil || a.DefaultPlacement != nil || len(a.Targets) != 1 || a.Targets[0].ID != "gpu" {
		t.Fatalf("limited grant reached the default: %+v %v", a, err)
	}
	retained, err := st.RetainedAssignment(ctx, "org", "proj", "env")
	if err != nil || retained.DefaultPlacement == nil {
		t.Fatalf("cleanup lost the default it placed with: %+v %v", retained, err)
	}

	// Once nothing uses the Cloud, its default can change, and the account stays retained.
	if _, err = conn.Exec(ctx, `DELETE FROM loomplane_cloud_assignments WHERE environment_id='env'`); err != nil {
		t.Fatal(err)
	}
	profile.DefaultPlacement, profile.Revision = &cloudprofiles.DefaultPlacement{Provider: "vercel", ConnectionID: "new"}, 4
	if err = st.UpdateProfile(ctx, profile, 3); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `DELETE FROM loomplane_cloud_targets WHERE id='gpu'`); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `DELETE FROM loomplane_cloud_connections WHERE id='new'`); err == nil {
		t.Fatal("deleted the connection a Cloud default uses")
	}
}

func TestCloudTagsAndDefaultPlacementAPI(t *testing.T) {
	cfg, r, c, org, project := cloudAPISetup(t)
	defer r.stop()
	conn, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	for _, id := range []string{"conn_vercel_a", "conn_vercel_b"} {
		if _, err = conn.Exec(t.Context(), `INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES($1,$2,'vercel',$1,$1,'ready','usr',now(),now())`, id, org); err != nil {
			t.Fatal(err)
		}
	}

	var cloud cloudprofiles.Profile
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/clouds", map[string]any{"name": "Single provider", "default_placement": map[string]string{"provider": "vercel", "connection_id": "conn_vercel_a", "region": "iad1"}}, &cloud)
	if d := cloud.DefaultPlacement; d == nil || d.Provider != "vercel" || d.ConnectionID != "conn_vercel_a" || d.Region != "iad1" {
		t.Fatalf("create default: %+v", cloud)
	}
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/clouds", map[string]any{"name": "Mismatch", "default_placement": map[string]string{"provider": "cloud-run", "connection_id": "conn_vercel_a"}}, nil)

	var raw map[string]any
	c.must(http.StatusOK, http.MethodPut, "/api/v1/clouds/"+cloud.ID+"/default-placement", map[string]any{"default_placement": nil, "revision": 1}, &raw)
	if v, ok := raw["default_placement"]; !ok || v != nil || raw["revision"] != float64(2) {
		t.Fatalf("cleared default should read null: %v", raw)
	}
	c.must(http.StatusConflict, http.MethodPut, "/api/v1/clouds/"+cloud.ID+"/default-placement", map[string]any{"default_placement": nil, "revision": 1}, nil)
	c.must(http.StatusOK, http.MethodPut, "/api/v1/clouds/"+cloud.ID+"/default-placement", map[string]any{"default_placement": map[string]string{"provider": "vercel", "connection_id": "conn_vercel_b"}, "revision": 2}, &cloud)
	if cloud.DefaultPlacement == nil || cloud.DefaultPlacement.ConnectionID != "conn_vercel_b" || cloud.Revision != 3 {
		t.Fatalf("set default: %+v", cloud)
	}

	var target cloudprofiles.Target
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/clouds/"+cloud.ID+"/targets", map[string]any{"name": "edge", "provider": "vercel", "connection_id": "conn_vercel_a", "tags": []string{"web", "eu"}}, &target)
	if !slices.Equal(target.Tags, []string{"eu", "web"}) {
		t.Fatalf("target tags: %+v", target)
	}
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/clouds/"+cloud.ID+"/targets", map[string]any{"name": "bad", "provider": "vercel", "connection_id": "conn_vercel_a", "tags": []string{"Not A Tag"}}, nil)
	c.must(http.StatusOK, http.MethodPut, "/api/v1/clouds/"+cloud.ID+"/targets/"+target.ID, map[string]any{"display_name": "Edge", "revision": 1}, &target)
	if !slices.Equal(target.Tags, []string{"eu", "web"}) {
		t.Fatalf("rename dropped tags: %+v", target)
	}
	c.must(http.StatusOK, http.MethodPut, "/api/v1/clouds/"+cloud.ID+"/targets/"+target.ID, map[string]any{"display_name": "Edge", "tags": []string{}, "revision": 2}, &raw)
	if tags, ok := raw["tags"].([]any); !ok || len(tags) != 0 {
		t.Fatalf("cleared tags should read an empty list: %v", raw)
	}

	// A project granted the whole Cloud reads the default through its own routes.
	c.must(http.StatusOK, http.MethodPut, "/api/v1/clouds/"+cloud.ID+"/projects/"+project, map[string]any{"target_ids": []string{}}, nil)
	var granted cloudprofiles.Profile
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project+"/clouds/"+cloud.ID, nil, &granted)
	if granted.DefaultPlacement == nil || granted.DefaultPlacement.ConnectionID != "conn_vercel_b" {
		t.Fatalf("project view default: %+v", granted)
	}
}
