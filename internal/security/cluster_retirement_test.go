package security

import "testing"

func TestClusterPermissionRetirement(t *testing.T) {
	for _, role := range Catalog() {
		found := false
		for _, permission := range role.Permissions {
			if permission.Name() == "cluster:delete" {
				found = true
			}
		}
		if found != (role.Slug == RoleOwner || role.Slug == RoleAdmin) {
			t.Fatalf("%s cluster:delete = %v", role.Slug, found)
		}
	}
}
