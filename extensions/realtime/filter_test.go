package realtime

import (
	"context"
	"encoding/json"
	"testing"

	store "github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/security"
)

type permissionFixture struct{ denied security.Permission }

func (f permissionFixture) Require(_ context.Context, _ security.Principal, perm security.Permission, _ string) error {
	if perm == f.denied {
		return security.ErrForbidden
	}
	return nil
}

func TestFilter_requiresDeploymentReadForLogsAndLimitsSelectedProjects(t *testing.T) {
	input := []store.Event{
		{ProjectID: "proj_a", Type: "build.log", Data: json.RawMessage(`{"deployment_id":"dep_a"}`)},
		{ProjectID: "proj_a", Type: "project.updated"},
		{ProjectID: "proj_b", Type: "project.updated"},
	}
	e := &Extension{authz: permissionFixture{denied: security.DeploymentRead}}
	p := security.Principal{OrgID: "org_a", UserID: "usr_a"}
	sub := Subscription{ProjectIDs: []string{"proj_a"}, DeploymentIDs: []string{"dep_a"}}
	got, err := e.filter(t.Context(), p, sub, input)
	if err != nil || len(got) != 1 || got[0].Type != "project.updated" || got[0].ProjectID != "proj_a" {
		t.Fatalf("filtered data: %+v %v", got, err)
	}
	e.authz = permissionFixture{}
	sub.DeploymentIDs = nil
	got, err = e.filter(t.Context(), p, sub, input)
	if err != nil || len(got) != 1 {
		t.Fatalf("global subscription included unselected build output: %+v %v", got, err)
	}
}

func TestFilterCloudEventsRequireTheirResourcePermissions(t *testing.T) {
	input := []store.Event{{ProjectID: "proj_a", Type: "cloud.updated"}, {ProjectID: "proj_a", Type: "cloud_target.updated"}, {ProjectID: "proj_a", Type: "environment_cloud.updated"}, {ProjectID: "proj_a", Type: "cloud_sync.updated"}, {ProjectID: "proj_b", Type: "cloud_sync.updated"}}
	p := security.Principal{OrgID: "org_a", UserID: "usr_a"}
	sub := Subscription{ProjectIDs: []string{"proj_a"}}
	for denied, want := range map[security.Permission]int{security.CloudAccountRead: 1, security.DeploymentRead: 3} {
		e := &Extension{authz: permissionFixture{denied: denied}}
		got, err := e.filter(t.Context(), p, sub, input)
		if err != nil || len(got) != want {
			t.Fatalf("denied %s: %+v %v", denied, got, err)
		}
	}
}

func TestFilterCloudSyncTargetDetailsStayPrivateForProjectSubscribers(t *testing.T) {
	raw := json.RawMessage(`{"id":"sync","target_id":"private-target","before":{"connection_id":"private-old"},"after":{"connection_id":"private-new"},"status":"running","environments":[{"project_id":"proj_a","environment_id":"env"}]}`)
	original := string(raw)
	input := []store.Event{{ProjectID: "proj_a", Type: "cloud_sync.updated", Data: raw}}
	e := &Extension{authz: permissionFixture{denied: security.OrganizationCloudManage}}
	actor := security.Principal{OrgID: "org", UserID: "member"}
	got, err := e.filter(t.Context(), actor, Subscription{ProjectIDs: []string{"proj_a"}}, input)
	if err != nil || len(got) != 1 {
		t.Fatalf("project event: %+v %v", got, err)
	}
	var projected map[string]json.RawMessage
	if err = json.Unmarshal(got[0].Data, &projected); err != nil {
		t.Fatal(err)
	}
	if string(projected["target_id"]) != `""` {
		t.Error("project status event exposed target_id")
	}
	for _, field := range []string{"before", "after"} {
		if _, ok := projected[field]; ok {
			t.Errorf("project status event exposed %s", field)
		}
	}
	if string(projected["status"]) != `"running"` || len(projected["environments"]) == 0 {
		t.Fatal("status notification lost participant state")
	}
	if string(input[0].Data) != original {
		t.Fatal("projection mutated stored event payload")
	}
	e.authz = permissionFixture{}
	got, err = e.filter(t.Context(), actor, Subscription{ProjectIDs: []string{"proj_a"}}, input)
	if err != nil || len(got) != 1 || string(got[0].Data) != original {
		t.Fatalf("manager event details changed: %+v %v", got, err)
	}
}
