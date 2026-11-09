package ops

import (
	"bytes"
	"context"
	"testing"

	"github.com/xraph/authsome/rbac"
	"github.com/xraph/chronicle"
	"github.com/xraph/chronicle/audit"
	chronstore "github.com/xraph/chronicle/store"
	chronmem "github.com/xraph/chronicle/store/memory"

	"github.com/xraph/loomplane/internal/platformadmin"
)

const grantApp = "aapp_test"

type grantUsers map[string]string

func (g grantUsers) UserIDByEmail(_ context.Context, email string) (string, error) {
	if id, ok := g[email]; ok {
		return id, nil
	}
	return "", platformadmin.ErrNoUser
}

// grantRoles is the slice of rbac.Store that platformadmin.Grant uses.
type grantRoles struct {
	rbac.Store
	held map[string][]*rbac.Role
}

func (g *grantRoles) ListUserRolesForApp(_ context.Context, _, userID string) ([]*rbac.Role, error) {
	return g.held[userID], nil
}

func (g *grantRoles) GetRoleBySlug(_ context.Context, appID, slug string) (*rbac.Role, error) {
	return &rbac.Role{ID: "arol_" + slug, AppID: appID, Slug: slug}, nil
}

func (g *grantRoles) AssignUserRole(_ context.Context, appID string, ur *rbac.UserRole) error {
	g.held[ur.UserID] = append(g.held[ur.UserID], &rbac.Role{ID: ur.RoleID, AppID: appID, Slug: rbac.PlatformOwnerSlug})
	return nil
}

// grantFixture runs grant-platform-admin against fakes and a Chronicle in
// memory.
func grantFixture(t *testing.T) (grantDeps, *chronmem.Store) {
	t.Helper()
	mem := chronmem.New()
	trail, err := chronicle.New(chronicle.WithStore(chronstore.NewAdapter(mem)))
	if err != nil {
		t.Fatal(err)
	}
	roles := &grantRoles{held: map[string][]*rbac.Role{}}
	return grantDeps{
		open: func(context.Context, string, string) (platformadmin.UserFinder, rbac.Store, func(), error) {
			return grantUsers{"ada@example.com": "ausr_ada"}, roles, func() {}, nil
		},
		openAudit: func(context.Context, string) (*chronicle.Chronicle, func(), error) {
			return trail, func() {}, nil
		},
	}, mem
}

// Granting the platform role is a staff action like any other, so it goes
// to the platform stream, with the CLI as the actor and the user granted as
// the resource. A grant that changes nothing records nothing.
func TestGrantPlatformAdminRecordsThePlatformStream(t *testing.T) {
	ctx := t.Context()
	deps, mem := grantFixture(t)
	s := Settings{AuthAppID: grantApp}
	var out bytes.Buffer
	for range 2 {
		if err := grantPlatformAdmin(ctx, s, "ada@example.com", &out, deps); err != nil {
			t.Fatal(err)
		}
	}
	res, err := mem.Query(ctx, &audit.Query{AppID: grantApp})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 1 {
		t.Fatalf("recorded %d events, want one grant", len(res.Events))
	}
	ev := res.Events[0]
	if ev.Action != "platform_admin.granted" || ev.TenantID != platformadmin.AuditTenant ||
		ev.UserID != platformadmin.CLIActor || ev.Resource != "user" || ev.ResourceID != "ausr_ada" {
		t.Fatalf("event %s by %q on %s %s in tenant %q; want platform_admin.granted by %q on user ausr_ada in %q",
			ev.Action, ev.UserID, ev.Resource, ev.ResourceID, ev.TenantID, platformadmin.CLIActor, platformadmin.AuditTenant)
	}
	if ev.Metadata["role"] != rbac.PlatformOwnerSlug {
		t.Fatalf("metadata %v, want the role granted", ev.Metadata)
	}
}
