package security

import (
	"slices"
	"testing"
)

func TestNodePermissions(t *testing.T) {
	wantManage := map[string]bool{RoleAdmin: true, RoleOwner: true}
	for _, role := range Catalog() {
		if !slices.Contains(role.Permissions, NodeRead) {
			t.Fatalf("%s lacks nodes:read", role.Slug)
		}
		if got := slices.Contains(role.Permissions, NodeManage); got != wantManage[role.Slug] {
			t.Fatalf("%s nodes:manage = %v", role.Slug, got)
		}
	}
	if NodeRead.Name() != "nodes:read" || NodeManage.Name() != "nodes:manage" {
		t.Fatal("unexpected permission names")
	}
}
