package platformadmin

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/authsome/rbac"
)

const app = "aapp_01"

func TestHoldsIgnoresOtherApps(t *testing.T) {
	cases := []struct {
		name  string
		roles []*rbac.Role
		want  bool
	}{
		{"none", nil, false},
		{"owner", []*rbac.Role{{AppID: app, Slug: rbac.PlatformOwnerSlug}}, true},
		{"admin", []*rbac.Role{{AppID: app, Slug: rbac.PlatformAdminSlug}}, true},
		{"other role", []*rbac.Role{{AppID: app, Slug: "member"}}, false},
		{"owner of another app", []*rbac.Role{{AppID: "aapp_02", Slug: rbac.PlatformOwnerSlug}}, false},
	}
	for _, c := range cases {
		if got := Holds(c.roles, app); got != c.want {
			t.Errorf("%s: Holds = %v, want %v", c.name, got, c.want)
		}
	}
}

type fakeUsers map[string]string

func (f fakeUsers) UserIDByEmail(_ context.Context, email string) (string, error) {
	if id, ok := f[email]; ok {
		return id, nil
	}
	return "", ErrNoUser
}

// fakeRoles is the slice of rbac.Store that Grant uses.
type fakeRoles struct {
	rbac.Store
	held    map[string][]*rbac.Role
	assigns []*rbac.UserRole
}

func (f *fakeRoles) GetRoleBySlug(_ context.Context, appID, slug string) (*rbac.Role, error) {
	return &rbac.Role{ID: "arol_" + slug, AppID: appID, Slug: slug}, nil
}

func (f *fakeRoles) ListUserRolesForApp(_ context.Context, _, userID string) ([]*rbac.Role, error) {
	return f.held[userID], nil
}

func (f *fakeRoles) AssignUserRole(_ context.Context, _ string, ur *rbac.UserRole) error {
	f.assigns = append(f.assigns, ur)
	return nil
}

func TestGrant(t *testing.T) {
	ctx := t.Context()
	users := fakeUsers{"ada@example.com": "ausr_1", "bob@example.com": "ausr_2"}
	roles := &fakeRoles{held: map[string][]*rbac.Role{
		"ausr_2": {{AppID: app, Slug: rbac.PlatformOwnerSlug}},
	}}

	if _, err := Grant(ctx, users, roles, app, "nobody@example.com"); !errors.Is(err, ErrNoUser) {
		t.Fatalf("unknown email: err = %v, want ErrNoUser", err)
	}

	granted, err := Grant(ctx, users, roles, app, "bob@example.com")
	if err != nil || granted || len(roles.assigns) != 0 {
		t.Fatalf("existing owner: granted=%v err=%v assigns=%d, want false, nil, 0", granted, err, len(roles.assigns))
	}

	granted, err = Grant(ctx, users, roles, app, "ada@example.com")
	if err != nil || !granted {
		t.Fatalf("plain user: granted=%v err=%v, want true, nil", granted, err)
	}
	if len(roles.assigns) != 1 || roles.assigns[0].UserID != "ausr_1" || roles.assigns[0].RoleID != "arol_"+rbac.PlatformOwnerSlug {
		t.Fatalf("assigns = %+v, want one platform-owner assignment for ausr_1", roles.assigns)
	}
}
