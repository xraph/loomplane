package identity

import (
	"context"
	"encoding/json"
	"testing"

	authid "github.com/xraph/authsome/id"
	authmw "github.com/xraph/authsome/middleware"
	"github.com/xraph/authsome/rbac"

	"github.com/xraph/loomplane/internal/config"
)

// The route itself needs the running engine (setup state, the waitlist
// setting, the session); the integration tests cover it. This checks the
// part the config decides.
func TestPublicConfigReportsTheAIFlag(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := config.Defaults()
		cfg.AI.Enabled = enabled
		raw, err := json.Marshal(New(cfg).staticPublicConfig())
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Features struct {
				AI bool `json:"ai"`
			} `json:"features"`
			ConsolePath *string `json:"console_path"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		if body.Features.AI != enabled {
			t.Fatalf("features.ai = %v with cfg.AI.Enabled = %v", body.Features.AI, enabled)
		}
		if body.ConsolePath != nil {
			t.Fatalf("the static config carries a console path: %s", raw)
		}
	}
}

// An API key's request never runs the session middleware, so its context
// has no user. That must read as "not a platform user" before any role is
// looked up (the extension here has no role reader to look in).
func TestPlatformUserSaysNoWithoutASessionUser(t *testing.T) {
	e := New(config.Defaults())
	if id, ok := e.platformUser(context.Background()); ok || id != "" {
		t.Fatalf("platformUser without a session user = %q, %v; want no", id, ok)
	}
	if e.holdsPlatformRole(context.Background()) {
		t.Fatal("holdsPlatformRole without a session user")
	}
}

// fakePlatformRoles is the roles fixture the platform checks read: each
// user's roles in an app.
type fakePlatformRoles struct {
	rbac.Store
	held map[string][]*rbac.Role
}

func (f fakePlatformRoles) ListUserRolesForApp(_ context.Context, _, userID string) ([]*rbac.Role, error) {
	return f.held[userID], nil
}

// A platform admin is a platform user but never the platform owner; the
// owner is both; a session with no platform role and an API key (no
// session user) are neither.
func TestPlatformOwner_AdminIsNot(t *testing.T) {
	cfg := config.Defaults()
	cfg.Identity.AppID = "aapp_test"
	e := New(cfg)
	owner, admin, member := authid.NewUserID(), authid.NewUserID(), authid.NewUserID()
	e.platformRoles = fakePlatformRoles{held: map[string][]*rbac.Role{
		owner.String():  {{AppID: cfg.Identity.AppID, Slug: rbac.PlatformOwnerSlug}},
		admin.String():  {{AppID: cfg.Identity.AppID, Slug: rbac.PlatformAdminSlug}},
		member.String(): {{AppID: "aapp_other", Slug: rbac.PlatformOwnerSlug}},
	}}
	as := func(u authid.UserID) context.Context { return authmw.WithUserID(context.Background(), u) }

	if id, ok := e.platformOwner(as(owner)); !ok || id != owner.String() {
		t.Fatalf("the owner: %q %v", id, ok)
	}
	if _, ok := e.platformUser(as(admin)); !ok {
		t.Fatal("the admin isn't a platform user")
	}
	if id, ok := e.platformOwner(as(admin)); ok || id != "" {
		t.Fatalf("the admin passed as the owner: %q", id)
	}
	if _, ok := e.platformOwner(as(member)); ok {
		t.Fatal("another app's owner passed as the owner")
	}
	if _, ok := e.platformOwner(context.Background()); ok {
		t.Fatal("no session user passed as the owner")
	}
}

// The public config tells the dashboard who the owner is: true for the
// owner alone, not for an admin and not without a session.
func TestPublicConfig_PlatformOwnerFlag(t *testing.T) {
	cfg := config.Defaults()
	cfg.Identity.AppID = "aapp_test"
	e := New(cfg)
	owner, admin := authid.NewUserID(), authid.NewUserID()
	e.platformRoles = fakePlatformRoles{held: map[string][]*rbac.Role{
		owner.String(): {{AppID: cfg.Identity.AppID, Slug: rbac.PlatformOwnerSlug}},
		admin.String(): {{AppID: cfg.Identity.AppID, Slug: rbac.PlatformAdminSlug}},
	}}
	flag := func(ctx context.Context) bool {
		var out PublicConfig
		e.markOwner(ctx, &out)
		return out.PlatformOwner
	}
	if !flag(authmw.WithUserID(context.Background(), owner)) {
		t.Fatal("the owner isn't marked")
	}
	if flag(authmw.WithUserID(context.Background(), admin)) {
		t.Fatal("a platform admin was marked as the owner")
	}
	if flag(context.Background()) {
		t.Fatal("an anonymous request was marked as the owner")
	}
}
