package security

import (
	"slices"
	"testing"

	"github.com/xraph/warden/permission"
	"github.com/xraph/warden/store/memory"
)

var aiPermissions = []Permission{AIRead, AIEdit, AIToolSecret, AIPublishNonProduction, AIManage}

func rolePermissions(t *testing.T, slug string) []Permission {
	t.Helper()
	for _, r := range Catalog() {
		if r.Slug == slug {
			return r.Permissions
		}
	}
	t.Fatalf("no role %s in the catalog", slug)
	return nil
}

func assertHolds(t *testing.T, slug string, held []Permission, want ...Permission) {
	t.Helper()
	for _, p := range aiPermissions {
		if got := slices.Contains(held, p); got != slices.Contains(want, p) {
			t.Errorf("%s holds %s: %v", slug, p.Name(), got)
		}
	}
}

func TestViewersReadAIOnly(t *testing.T) {
	assertHolds(t, RoleViewer, rolePermissions(t, RoleViewer), AIRead)
}

func TestDevelopersBuildAI(t *testing.T) {
	assertHolds(t, RoleDeveloper, rolePermissions(t, RoleDeveloper), AIRead, AIEdit, AIToolSecret, AIPublishNonProduction)
}

func TestAdminsManageAI(t *testing.T) {
	for _, slug := range []string{RoleAdmin, RoleOwner} {
		assertHolds(t, slug, rolePermissions(t, slug), aiPermissions...)
	}
}

func TestAIPermissionNames(t *testing.T) {
	want := map[Permission]string{
		AIRead: "ai:read", AIEdit: "ai:edit", AIToolSecret: "ai:tool_secret",
		AIPublishNonProduction: "ai:publish_nonproduction", AIManage: "ai:manage",
	}
	all := AllPermissions()
	for p, name := range want {
		if p.Name() != name {
			t.Errorf("name %q, want %q", p.Name(), name)
		}
		count := 0
		for _, a := range all {
			if a == p {
				count++
			}
		}
		if count != 1 {
			t.Errorf("%s listed %d times in AllPermissions", name, count)
		}
	}
}

func TestEnsureTenantAddsAIToExistingRoles(t *testing.T) {
	store := memory.New()
	if err := NewAuthorizer(nil, store, nil).EnsureTenant(t.Context(), org); err != nil {
		t.Fatal(err)
	}
	// Rewrite every role as the catalog was before the AI permissions.
	for _, def := range Catalog() {
		r, err := store.GetRoleBySlug(t.Context(), org, rootNamespace, def.Slug)
		if err != nil {
			t.Fatal(err)
		}
		var refs []permission.Ref
		for _, p := range def.Permissions {
			if p.Resource != "ai" {
				refs = append(refs, permission.Ref{NamespacePath: rootNamespace, Name: p.Name()})
			}
		}
		if err := store.SetRolePermissions(t.Context(), org, r.ID, refs); err != nil {
			t.Fatal(err)
		}
	}
	// A fresh process has an empty ensured cache and runs the new catalog.
	if err := NewAuthorizer(nil, store, nil).EnsureTenant(t.Context(), org); err != nil {
		t.Fatal(err)
	}
	for _, def := range Catalog() {
		r, err := store.GetRoleBySlug(t.Context(), org, rootNamespace, def.Slug)
		if err != nil {
			t.Fatal(err)
		}
		granted, err := store.ListRolePermissions(t.Context(), org, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		var held []Permission
		for _, g := range granted {
			held = append(held, Permission{g.Resource, g.Action})
		}
		var want []Permission
		for _, p := range def.Permissions {
			if p.Resource == "ai" {
				want = append(want, p)
			}
		}
		assertHolds(t, def.Slug, held, want...)
	}
}
