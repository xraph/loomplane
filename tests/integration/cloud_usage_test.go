//go:build integration

package integration

import (
	"slices"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/page"
)

func TestCloudUsageCountsPlacedServicesByTarget(t *testing.T) {
	st, conn, _ := cloudSyncDatabase(t)
	ctx := t.Context()
	now := time.Now().UTC()
	if err := st.CreateProfile(ctx, &cloudprofiles.Profile{ID: "other", OrgID: "org", ProjectID: "proj", Name: "Other", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES('org','proj','env','cloud',1)`,
		`INSERT INTO loomplane_bindings(id,org_id,project_id,environment_id,subject,name,provider,engine,config,created_at,updated_at) VALUES
		 ('b1','org','proj','env','service','web','vercel','vercel','{"cloud_placement":{"cloud_id":"cloud","target_id":"target"}}',now(),now()),
		 ('b2','org','proj','env','service','api','vercel','vercel','{"cloud_placement":{"cloud_id":"cloud","target_id":"","default":true}}',now(),now()),
		 ('b3','org','proj','env','service','admin','vercel','vercel','{"cloud_placement":{"cloud_id":"cloud","target_id":"target"}}',now(),now()),
		 ('b4','org','proj','env','resource','db','vercel','vercel','{"cloud_placement":{"cloud_id":"cloud","target_id":"target"}}',now(),now()),
		 ('b5','org','proj','env','service','direct','vercel','vercel','{}',now(),now()),
		 ('b6','org','proj','stage','service','job','vercel','vercel','{"cloud_placement":{"cloud_id":"cloud","target_id":"target"}}',now(),now()),
		 ('b7','org','proj','stage','service','elsewhere','vercel','vercel','{"cloud_placement":{"cloud_id":"other","target_id":"x"}}',now(),now())`,
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	usage, err := st.CloudUsage(ctx, "org", "", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if len(usage.Environments) != 2 {
		t.Fatalf("environments: %+v", usage.Environments)
	}
	prod, stage := usage.Environments[0], usage.Environments[1]
	if prod.EnvironmentName != "production" || !prod.Assigned || prod.ProjectName != "One" {
		t.Fatalf("assigned environment: %+v", prod)
	}
	// A service this Cloud placed keeps the environment listed after it
	// chose another Cloud, so nothing running here goes unseen.
	if stage.EnvironmentName != "staging" || stage.Assigned {
		t.Fatalf("leftover environment: %+v", stage)
	}
	want := []cloudprofiles.PlacementUsage{{Default: true, Services: []string{"api"}}, {TargetID: "target", Services: []string{"admin", "web"}}}
	if len(prod.Placements) != 2 || !slices.Equal(prod.Placements[0].Services, want[0].Services) || !prod.Placements[0].Default ||
		prod.Placements[1].TargetID != "target" || !slices.Equal(prod.Placements[1].Services, want[1].Services) {
		t.Fatalf("placements: %+v", prod.Placements)
	}
	if len(stage.Placements) != 1 || !slices.Equal(stage.Placements[0].Services, []string{"job"}) {
		t.Fatalf("leftover placements: %+v", stage.Placements)
	}
	scoped, err := st.CloudUsage(ctx, "org", "nobody", "cloud")
	if err != nil || len(scoped.Environments) != 0 {
		t.Fatalf("another project's usage leaked: %+v %v", scoped, err)
	}
	foreign, err := st.CloudUsage(ctx, "elsewhere", "", "cloud")
	if err != nil || len(foreign.Environments) != 0 {
		t.Fatalf("cross-organization usage leaked: %+v %v", foreign, err)
	}
}

func TestCloudSyncPageFiltersByCloud(t *testing.T) {
	st, conn, _ := cloudSyncDatabase(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	insert := func(id, cloud, previewBody string, at time.Time) {
		t.Helper()
		if _, err := conn.Exec(ctx, `INSERT INTO loomplane_cloud_previews(id,org_id,project_id,revision,body,snapshot,created_at) VALUES($1,'org','proj','digest',$2,'{}',$3)`, id+"-preview", previewBody, at); err != nil {
			t.Fatal(err)
		}
		body := `{"id":"` + id + `","org_id":"org","project_id":"proj","cloud_id":"` + cloud + `","status":"succeeded","created_at":"` + at.Format(time.RFC3339Nano) + `"}`
		if _, err := conn.Exec(ctx, `INSERT INTO loomplane_cloud_syncs(id,org_id,project_id,preview_id,idempotency_key,status,cloud_id,body,snapshot,created_at,updated_at) VALUES($1,'org','proj',$2,$1,'succeeded',$3,$4,'{}',$5,$5)`, id, id+"-preview", cloud, body, at); err != nil {
			t.Fatal(err)
		}
	}
	insert("target-move", "cloud", `{}`, now.Add(-3*time.Second))
	insert("other-cloud", "other", `{}`, now.Add(-2*time.Second))
	// Leaving a Cloud records no Cloud on the sync, only on the review.
	insert("unassign", "", `{"before_assignment":{"cloud_id":"cloud"}}`, now.Add(-time.Second))

	ids := func(cloud string) []string {
		t.Helper()
		rows, _, err := st.ListSyncPage(ctx, "org", "proj", cloud, page.Cursor{}, 10)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	if got := ids("cloud"); !slices.Equal(got, []string{"unassign", "target-move"}) {
		t.Fatalf("cloud history: %v", got)
	}
	if got := ids("other"); !slices.Equal(got, []string{"other-cloud"}) {
		t.Fatalf("other cloud history: %v", got)
	}
	if got := ids(""); len(got) != 3 {
		t.Fatalf("unfiltered history: %v", got)
	}
}
