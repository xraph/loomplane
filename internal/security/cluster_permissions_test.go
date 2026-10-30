package security

import (
	"slices"
	"testing"
)

func TestClusterManageCatalog(t *testing.T) {
	for _, role := range Catalog() {
		got := slices.Contains(role.Permissions, ClusterManage)
		want := role.Slug == RoleOwner || role.Slug == RoleAdmin
		if got != want {
			t.Fatalf("%s cluster:manage = %v", role.Slug, got)
		}
	}
	if ClusterManage.Name() != "cluster:manage" || ClusterManage.Reads() {
		t.Fatal("invalid maintenance permission")
	}
}
