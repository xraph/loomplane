package security

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/xraph/warden"
	"github.com/xraph/warden/assignment"
	wardenid "github.com/xraph/warden/id"
	"github.com/xraph/warden/role"
)

func currentCatalog() []*role.Role {
	out := make([]*role.Role, 0, 4)
	for _, slug := range []string{RoleOwner, RoleAdmin, RoleDeveloper, RoleViewer} {
		out = append(out, &role.Role{ID: wardenid.NewRoleID(), TenantID: org, Slug: slug})
	}
	return out
}

func TestCurrentManagedRolesEffectiveSources(t *testing.T) {
	project := "proj_01h455vb4pex5vsknk084sn02q"
	ns, _ := ProjectNamespace(project)
	for _, grants := range [][]ScopedRole{
		{{Role: RoleAdmin, TeamID: "team1", ProjectID: project}},
		{{Role: RoleAdmin, TeamID: "team2", ProjectID: project}},
		{{Role: RoleAdmin, TeamID: "team1", ProjectID: project}, {Role: RoleAdmin, TeamID: "team2", ProjectID: project}},
	} {
		got, err := CurrentManagedRoles(MemberMember, grants)
		if err != nil || !reflect.DeepEqual(got, map[string]string{"": RoleDeveloper, ns: RoleAdmin}) {
			t.Fatalf("got=%v err=%v", got, err)
		}
	}
	got, err := CurrentManagedRoles(MemberOwner, []ScopedRole{{Role: RoleAdmin, TeamID: "team1", ProjectID: project}})
	if err != nil || !reflect.DeepEqual(got, map[string]string{"": RoleOwner}) {
		t.Fatalf("redundant project grant=%v %v", got, err)
	}
	for _, g := range []ScopedRole{{Role: RoleOwner, TeamID: "team"}, {Role: RoleAdmin}, {Role: RoleAdmin, TeamID: "team", ProjectID: "bad"}} {
		if _, err := CurrentManagedRoles(MemberMember, []ScopedRole{g}); err == nil {
			t.Fatal("malformed grant accepted")
		}
	}
}

func TestVerifyCurrentManagedClassifier(t *testing.T) {
	catalog := currentCatalog()
	bySlug := map[string]*role.Role{}
	for _, r := range catalog {
		bySlug[r.Slug] = r
	}
	project := "proj_01h455vb4pex5vsknk084sn02q"
	ns, _ := ProjectNamespace(project)
	makeRows := func() []*assignment.Assignment {
		return []*assignment.Assignment{
			{ID: wardenid.NewAssignmentID(), TenantID: org, SubjectKind: string(warden.SubjectUser), SubjectID: alice, RoleID: bySlug[RoleDeveloper].ID, GrantedBy: "authsome:member.add"},
			{ID: wardenid.NewAssignmentID(), TenantID: org, SubjectKind: string(warden.SubjectUser), SubjectID: alice, RoleID: bySlug[RoleAdmin].ID, NamespacePath: ns, GrantedBy: SyncGrantedBy},
		}
	}
	want := map[string]string{"": RoleDeveloper, ns: RoleAdmin}
	expiry := time.Now().Add(time.Hour)
	tests := []struct {
		name   string
		change func([]*assignment.Assignment) []*assignment.Assignment
		valid  bool
	}{
		{"marked exact", nil, true},
		{"stale sources", func(r []*assignment.Assignment) []*assignment.Assignment {
			r[1].Metadata = map[string]any{"sources": []string{"old-team"}}
			return r
		}, true},
		{"malformed sources diagnostic", func(r []*assignment.Assignment) []*assignment.Assignment {
			r[1].Metadata = map[string]any{"sources": 123}
			return r
		}, true},
		{"unmarked equivalent", func(r []*assignment.Assignment) []*assignment.Assignment {
			r[0].GrantedBy = ""
			r[1].GrantedBy = "manual"
			return r
		}, true},
		{"managed wrong role", func(r []*assignment.Assignment) []*assignment.Assignment {
			r[0].RoleID = bySlug[RoleAdmin].ID
			return r
		}, false},
		{"legacy wrong role", func(r []*assignment.Assignment) []*assignment.Assignment {
			r[0].GrantedBy = "manual"
			r[0].RoleID = bySlug[RoleAdmin].ID
			return r
		}, false},
		{"legacy extra", func(r []*assignment.Assignment) []*assignment.Assignment {
			x := *r[1]
			x.ID = wardenid.NewAssignmentID()
			x.GrantedBy = "manual"
			x.NamespacePath = "proj-01h455vb4pex5vsknk084sn02r"
			return append(r, &x)
		}, false},
		{"duplicate", func(r []*assignment.Assignment) []*assignment.Assignment {
			x := *r[1]
			x.ID = wardenid.NewAssignmentID()
			return append(r, &x)
		}, false},
		{"missing", func(r []*assignment.Assignment) []*assignment.Assignment { return r[:1] }, false},
		{"authsome project marker", func(r []*assignment.Assignment) []*assignment.Assignment {
			r[1].GrantedBy = "authsome:member.add"
			return r
		}, false},
		{"managed resource", func(r []*assignment.Assignment) []*assignment.Assignment { r[1].ResourceID = "x"; return r }, false},
		{"managed expiry", func(r []*assignment.Assignment) []*assignment.Assignment { r[1].ExpiresAt = &expiry; return r }, false},
		{"unrelated custom", func(r []*assignment.Assignment) []*assignment.Assignment {
			x := *r[1]
			x.ID = wardenid.NewAssignmentID()
			x.GrantedBy = "manual"
			x.RoleID = wardenid.NewRoleID()
			x.Metadata = map[string]any{"sources": "unrelated"}
			return append(r, &x)
		}, true},
		{"manual resource", func(r []*assignment.Assignment) []*assignment.Assignment {
			x := *r[1]
			x.ID = wardenid.NewAssignmentID()
			x.GrantedBy = "manual"
			x.ResourceType = "project"
			x.ResourceID = "x"
			return append(r, &x)
		}, true},
		{"manual expiry", func(r []*assignment.Assignment) []*assignment.Assignment {
			x := *r[1]
			x.ID = wardenid.NewAssignmentID()
			x.GrantedBy = "manual"
			x.ExpiresAt = &expiry
			return append(r, &x)
		}, true},
		{"manual other namespace", func(r []*assignment.Assignment) []*assignment.Assignment {
			x := *r[1]
			x.ID = wardenid.NewAssignmentID()
			x.GrantedBy = "manual"
			x.NamespacePath = "custom"
			return append(r, &x)
		}, true},
		{"wrong tenant", func(r []*assignment.Assignment) []*assignment.Assignment { r[0].TenantID = "other"; return r }, false},
		{"wrong subject", func(r []*assignment.Assignment) []*assignment.Assignment { r[0].SubjectID = bob; return r }, false},
		{"nil row", func(r []*assignment.Assignment) []*assignment.Assignment { return append(r, nil) }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := makeRows()
			if tt.change != nil {
				rows = tt.change(rows)
			}
			before, err := json.Marshal(rows)
			if err != nil {
				t.Fatal(err)
			}
			err = VerifyCurrentManagedRoles(org, alice, want, catalog, rows)
			if (err == nil) != tt.valid {
				t.Fatalf("err=%v expectedValid=%v", err, tt.valid)
			}
			after, err := json.Marshal(rows)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("assignments mutated")
			}
		})
	}
	// Losing the final winning team changes the desired slot even if provenance
	// still names the previous team, so the stale broad assignment cannot survive.
	if err := VerifyCurrentManagedRoles(org, alice, map[string]string{"": RoleDeveloper}, catalog, makeRows()); err == nil {
		t.Fatal("removed winning source retained authority")
	}
	for _, change := range []func([]*role.Role){func(r []*role.Role) { r[0].TenantID = "foreign" }, func(r []*role.Role) { r[0].NamespacePath = "foreign" }, func(r []*role.Role) { r[0].ID = r[1].ID }, func(r []*role.Role) { r[0].Slug = r[1].Slug }} {
		c := currentCatalog()
		change(c)
		if err := VerifyCurrentManagedRoles(org, alice, want, c, makeRows()); err == nil {
			t.Fatal("unqualified catalog accepted")
		}
	}
}
