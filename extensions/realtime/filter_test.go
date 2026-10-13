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
