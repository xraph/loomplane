package federation

import (
	"testing"

	"go.jetify.com/typeid/v2"
)

func TestPlacementGroupMemberIdentityIsolation(t *testing.T) {
	a, err := NewMemberIdentity("group-a", 1, "east")
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []struct {
		group      string
		generation int64
		member     string
	}{{"group-b", 1, "east"}, {"group-a", 2, "east"}, {"group-a", 1, "west"}} {
		b, e := NewMemberIdentity(in.group, in.generation, in.member)
		if e != nil {
			t.Fatal(e)
		}
		if a.BindingID == b.BindingID || a.InstanceID == b.InstanceID || a.RuntimeRef == b.RuntimeRef || a.ReleaseID == b.ReleaseID {
			t.Fatal("member identities alias")
		}
	}
}
func TestPlacementGroupDrainBounds(t *testing.T) {
	n := int64(30)
	for _, d := range []DrainPolicy{{Mode: "bounded", TerminationGraceSeconds: 30}, {Mode: "bounded", MaxConnectionLifetimeSeconds: &n, TerminationGraceSeconds: 29}, {Mode: "unknown"}} {
		if d.Validate() == nil {
			t.Fatal("unsafe policy accepted")
		}
	}
	for _, d := range []DrainPolicy{{Mode: "bounded", MaxConnectionLifetimeSeconds: &n, TerminationGraceSeconds: 30}, {Mode: "attested"}} {
		if err := d.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPlacementGroupIdentityUsesTypedIDs(t *testing.T) {
	identity, err := NewMemberIdentity("group-a", 1, "east")
	if err != nil {
		t.Fatal(err)
	}
	for prefix, value := range map[string]string{"pgb": identity.BindingID, "inst": identity.InstanceID, "pgr": identity.ReleaseID} {
		id, parseErr := typeid.Parse(value)
		if parseErr != nil || id.Prefix() != prefix {
			t.Fatalf("invalid %s identity: %s %v", prefix, value, parseErr)
		}
	}
	for _, member := range []string{"../west", "east/west", "", "name%2fescape"} {
		if _, err = NewMemberIdentity("group-a", 1, member); err == nil {
			t.Fatal("invalid member identity accepted")
		}
	}
}
