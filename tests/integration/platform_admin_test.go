//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/xraph/authsome/rbac"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/platformadmin"
)

func TestGrantPlatformAdmin(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, nil)

	ctx := t.Context()
	users, roles, closeDB, err := platformadmin.Open(ctx, cfg.Database.URL, cfg.Identity.AppID)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()

	// The address is typed with different capitals than it was stored with.
	for i, want := range []bool{true, false} {
		granted, grantErr := platformadmin.Grant(ctx, users, roles, cfg.Identity.AppID, "Ada@Example.com")
		if grantErr != nil || granted != want {
			t.Fatalf("grant %d: granted=%v err=%v, want %v", i+1, granted, grantErr, want)
		}
	}
	if _, err = platformadmin.Grant(ctx, users, roles, cfg.Identity.AppID, "nobody@example.com"); err == nil {
		t.Fatal("granting an unknown email succeeded")
	}

	id, err := users.UserIDByEmail(ctx, "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	held, err := roles.ListUserRolesForApp(ctx, cfg.Identity.AppID, id)
	if err != nil {
		t.Fatal(err)
	}
	owners := 0
	for _, role := range held {
		if role.Slug == rbac.PlatformOwnerSlug {
			owners++
		}
	}
	if owners != 1 {
		t.Fatalf("platform-owner held %d times, want once: %+v", owners, held)
	}
}
