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

func TestAuthProvider_refusesSessionsForTheAutomation(t *testing.T) {
	a := NewAuthProvider()
	a.Bind(func(context.Context, string) (string, string, error) { return "loomplane:aorg_1", "aorg_1", nil }, &fakeAuthz{})
	if _, err := a.Authenticate(context.Background(), "Bearer tok"); !errors.Is(err, cpauth.ErrUnauthorized) {
		t.Fatalf("a session resolved to the automation: %v", err)
	}
}

func TestAuthProvider_authorizesTheAutomationAsItself(t *testing.T) {
	authz := &fakeAuthz{allow: map[security.Permission]bool{security.InfrastructureModify: true}}
	a := NewAuthProvider()
	a.Bind(func(context.Context, string) (string, string, error) { return "", "", nil }, authz)
	ctx := context.Background()
	// A deployment the automation approved calls Ctrl Plane with its claims.
	for _, sub := range []string{security.SystemSubject("aorg_1"), security.SystemSubject("aorg_other"), "ausr_1"} {
		if _, err := a.Authorize(ctx, cpauth.AuthzRequest{SubjectID: sub, TenantID: "aorg_1", Action: "create"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := authz.calls[0]; got != security.SystemPrincipal("aorg_1") {
		t.Fatalf("the automation was authorized as %+v", got)
	}
	// Another organization's automation is never the automation here.
	if got := authz.calls[1]; got.IsServiceAccount() {
		t.Fatalf("another org's automation was authorized as %+v", got)
	}
	if got := authz.calls[2]; got != (security.Principal{UserID: "ausr_1", OrgID: "aorg_1"}) {
		t.Fatalf("a person was authorized as %+v", got)
	}
}

func TestAuthProvider_allowsConsoleReads(t *testing.T) {
	authz := &fakeAuthz{}
	a := NewAuthProvider()
	admin := &cpauth.Claims{SubjectID: "ausr_1", Roles: []string{"system:admin"}}
	read := cpauth.WithClaims(security.WithConsoleRead(context.Background()), admin)
	req := cpauth.AuthzRequest{SubjectID: "ausr_1", Action: "system.stats"}
	// Unbound, nothing is allowed, a console read included.
	if ok, err := a.Authorize(read, req); ok || err != nil {
		t.Fatalf("an unbound provider allowed a console read: %v %v", ok, err)
	}
	a.Bind(func(context.Context, string) (string, string, error) { return "", "", nil }, authz)
	// The console gate admitted the read, for a platform admin, as a system
	// admin with no organization to check it in.
	if ok, err := a.Authorize(read, req); !ok || err != nil {
		t.Fatalf("a console read: %v %v, want allowed", ok, err)
	}
	if len(authz.calls) != 0 {
		t.Fatalf("a console read asked Warden: %+v", authz.calls)
	}
	for name, tc := range map[string]struct {
		ctx context.Context
		req cpauth.AuthzRequest
	}{
		"no console mark":        {cpauth.WithClaims(context.Background(), admin), req},
		"no claims":              {security.WithConsoleRead(context.Background()), req},
		"not a system admin":     {cpauth.WithClaims(security.WithConsoleRead(context.Background()), &cpauth.Claims{SubjectID: "ausr_1"}), req},
		"claims name a tenant":   {cpauth.WithClaims(security.WithConsoleRead(context.Background()), &cpauth.Claims{SubjectID: "ausr_1", TenantID: "aorg_1", Roles: []string{"system:admin"}}), req},
		"request names a tenant": {read, cpauth.AuthzRequest{SubjectID: "ausr_1", TenantID: "aorg_1", Action: "system.stats"}},
		"a tenant's read":        {read, cpauth.AuthzRequest{SubjectID: "ausr_1", Action: "workloads.list"}},
		"a write":                {read, cpauth.AuthzRequest{SubjectID: "ausr_1", Action: "tenants.delete"}},
	} {
		if ok, err := a.Authorize(tc.ctx, tc.req); ok || err != nil {
			t.Errorf("%s: %v %v, want refused", name, ok, err)
		}
	}
}

func TestConsoleRead(t *testing.T) {
	for intent, want := range map[string]bool{
		"session.detail": true, "system.stats": true, "tenants.list": true, "tenants.detail": true, "tenants.quota": true,
		"providers.list": true, "workers.list": true, "workers.detail": true, "events.list": true, "config.detail": true, "audit.list": true,
		"workloads.list": false, "workloads.detail": false, "instances.list": false, "secrets.list": false, "deployments.list": false,
		"tenants.delete": false, "": false,
	} {
		if got := ConsoleRead(intent); got != want {
			t.Errorf("ConsoleRead(%q) = %v, want %v", intent, got, want)
		}
	}
}
