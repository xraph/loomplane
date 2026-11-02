package publicplan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func TestPlacementGroupProjectionOmitsPrivatePins(t *testing.T) {
	plan := &planning.Plan{PlacementGroups: []domain.QualifiedGroupPlan{{GroupID: "group", Name: "web", Members: []domain.GroupMemberPlan{{Identity: domain.MemberIdentity{MemberID: "west"}, DestinationID: "private-destination", LocationIdentity: "private-location", PhysicalIdentity: domain.PhysicalIdentity{Account: "private-account"}, Prerequisite: domain.PrerequisitePin{RenewalAuthorityID: "private-authority", TLSSecretName: "private-secret"}}}}}}
	raw, err := json.Marshal(Project(plan))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-") || !strings.Contains(string(raw), `"member_id":"west"`) {
		t.Fatalf("unsafe or missing group projection: %s", raw)
	}
	plan.PlacementGroups = nil
	raw, err = json.Marshal(Project(plan))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "placement_groups") {
		t.Fatalf("legacy projection changed: %s", raw)
	}
}
