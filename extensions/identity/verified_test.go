package identity

import (
	"context"
	"errors"
	"testing"

	authid "github.com/xraph/authsome/id"
	authmw "github.com/xraph/authsome/middleware"
	"github.com/xraph/authsome/user"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/security"
)

func signedIn(verified bool) context.Context {
	uid, oid := authid.NewUserID(), authid.NewOrgID()
	ctx := authmw.WithUserID(context.Background(), uid)
	ctx = authmw.WithOrgID(ctx, oid)
	return authmw.WithUser(ctx, &user.User{ID: uid, EmailVerified: verified})
}

func TestUnverifiedEmailIsRefusedWhenVerificationIsRequired(t *testing.T) {
	cfg := config.Defaults()
	cfg.Identity.RequireEmailVerification = true
	e := New(cfg)
	// The session sign-up hands out works in any tab, so every API call
	// checks, not just sign-in.
	if _, err := e.principalFor(signedIn(false), ""); !errors.Is(err, security.ErrEmailNotVerified) {
		t.Fatalf("unverified: %v", err)
	}
	if security.StatusFor(security.ErrEmailNotVerified) != 403 {
		t.Fatal("an unverified email should be a 403")
	}
	if _, err := e.principalFor(signedIn(true), ""); err != nil {
		t.Fatalf("verified: %v", err)
	}
	if !e.staticPublicConfig().RequireEmailVerification {
		t.Fatal("the dashboard should be told verification is required")
	}
}

func TestUnverifiedEmailIsFineWhenVerificationIsOff(t *testing.T) {
	e := New(config.Defaults())
	if _, err := e.principalFor(signedIn(false), ""); err != nil {
		t.Fatalf("verification off: %v", err)
	}
}
