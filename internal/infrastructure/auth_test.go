package infrastructure

import (
	"context"
	"errors"
	"testing"

	cpauth "github.com/xraph/ctrlplane/auth"

	"github.com/xraph/loomplane/internal/security"
)

type fakeAuthz struct {
	allow map[security.Permission]bool
	calls []security.Principal
}

func (f *fakeAuthz) Require(_ context.Context, p security.Principal, perm security.Permission, _ string) error {
	f.calls = append(f.calls, p)
	if f.allow[perm] {
		return nil
	}
	return security.ErrForbidden
}

func TestAuthProvider_refusesUntilBound(t *testing.T) {
	a := NewAuthProvider()
	if _, err := a.Authenticate(context.Background(), "Bearer tok"); !errors.Is(err, cpauth.ErrUnauthorized) {
		t.Fatalf("unbound Authenticate: %v", err)
	}
	if ok, err := a.Authorize(context.Background(), cpauth.AuthzRequest{SubjectID: "u", TenantID: "o", Action: "read"}); ok || err != nil {
		t.Fatalf("unbound Authorize: %v %v", ok, err)
	}
}

func TestAuthProvider_authenticate(t *testing.T) {
	a := NewAuthProvider()
	var seen string
	a.Bind(func(_ context.Context, token string) (string, string, error) {
		seen = token
		switch token {
		case "good":
			return "ausr_1", "aorg_1", nil
		case "no-org":
			return "ausr_1", "", nil
		}
		return "", "", errors.New("unknown token")
	}, &fakeAuthz{})

	c, err := a.Authenticate(context.Background(), "Bearer good")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if seen != "good" || c.SubjectID != "ausr_1" || c.TenantID != "aorg_1" || c.IsSystemAdmin() {
		t.Fatalf("claims %+v from token %q", c, seen)
	}
	for _, tok := range []string{"", "Bearer ", "Bearer bad", "Bearer no-org"} {
		if _, err := a.Authenticate(context.Background(), tok); !errors.Is(err, cpauth.ErrUnauthorized) {
			t.Errorf("token %q: %v, want ErrUnauthorized", tok, err)
		}
	}
}

func TestAuthProvider_authorizeMapsToWarden(t *testing.T) {
	authz := &fakeAuthz{allow: map[security.Permission]bool{security.InfrastructureRead: true}}
	a := NewAuthProvider()
	a.Bind(func(context.Context, string) (string, string, error) { return "", "", nil }, authz)
	ctx := context.Background()
	for action, want := range map[string]bool{"read": true, "list": true, "GET": true, "create": false, "delete": false, "scale": false} {
		ok, err := a.Authorize(ctx, cpauth.AuthzRequest{SubjectID: "ausr_1", TenantID: "aorg_1", Action: action})
		if err != nil || ok != want {
			t.Errorf("action %q: %v %v, want %v", action, ok, err, want)
		}
	}
	if got := authz.calls[0]; got.UserID != "ausr_1" || got.OrgID != "aorg_1" {
		t.Fatalf("authorized as %+v", got)
	}
}

func TestWithPrincipal(t *testing.T) {
	ctx := WithPrincipal(context.Background(), security.Principal{UserID: "ausr_1", OrgID: "aorg_1"})
	if got := NewAuthProvider().GetTenantID(ctx); got != "aorg_1" {
		t.Fatalf("tenant %q", got)
	}
}
