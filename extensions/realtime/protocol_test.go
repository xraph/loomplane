package realtime

import (
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/security"
)

func TestSubscription_keepsScopeInTheAuthenticatedOrganization(t *testing.T) {
	p := security.Principal{UserID: "usr_1", OrgID: "org_1"}
	got, err := parseSubscription(p, []byte(`{"op":"subscribe","org_id":"org_1","project_ids":["proj_1","proj_1"],"deployment_ids":["dep_1"],"cursor":"org_1:12"}`))
	if err != nil || len(got.ProjectIDs) != 1 || got.Cursor != "org_1:12" {
		t.Fatalf("subscription: %+v %v", got, err)
	}
	for _, raw := range []string{
		`{"op":"subscribe","org_id":"org_2"}`,
		`{"op":"subscribe","org_id":"org_1","cursor":"org_2:1"}`,
		`{"op":"subscribe","org_id":"org_1","token":"reusable-token"}`,
		`{"op":"unknown","org_id":"org_1"}`,
	} {
		if _, err = parseSubscription(p, []byte(raw)); err == nil {
			t.Fatalf("unsafe subscription accepted: %s", raw)
		}
	}
	if _, err = parseSubscription(p, []byte(`{"op":"subscribe","org_id":"org_2"}`)); !errors.Is(err, security.ErrOrgMismatch) {
		t.Fatalf("org mismatch: %v", err)
	}
}
