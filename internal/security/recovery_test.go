package security

import (
	"context"
	"errors"
	"testing"
)

func TestOnlyOwnersMayRetireServiceBackups(t *testing.T) {
	for _, role := range Catalog() {
		holds := false
		for _, permission := range role.Permissions {
			if permission == BackupRetire {
				holds = true
			}
		}
		if holds != (role.Slug == RoleOwner) {
			t.Fatalf("retirement permission on %s: %v", role.Slug, holds)
		}
	}
	if BackupRetire.Reads() {
		t.Fatal("read-only keys may retire backups")
	}
}

func TestRetirementAuthorityUsesCurrentMembershipAndKeyScope(t *testing.T) {
	members := map[string]string{alice: "owner", bob: "admin"}
	auth := newAuthorizer(t, members)
	owner := Principal{OrgID: org, UserID: alice}
	if err := auth.Require(t.Context(), owner, BackupRetire, "project"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []Principal{{OrgID: org, UserID: bob}, {OrgID: org, UserID: alice, ReadOnly: true}, {OrgID: "foreign", UserID: alice}} {
		if err := auth.Require(t.Context(), p, BackupRetire, "project"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("unexpected retirement authority: %+v %v", p, err)
		}
	}
	members[alice] = "admin"
	if err := auth.Require(t.Context(), owner, BackupRetire, "project"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("demoted owner retained retirement authority: %v", err)
	}
	delete(members, alice)
	if err := auth.Require(t.Context(), owner, BackupRetire, "project"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("removed owner retained retirement authority: %v", err)
	}
}

func TestRetirementFailsClosedWithoutCurrentMembership(t *testing.T) {
	auth := newAuthorizer(t, map[string]string{alice: "owner"})
	principal := Principal{OrgID: org, UserID: alice}
	if err := auth.Require(t.Context(), principal, BackupRetire, "project"); err != nil {
		t.Fatal(err)
	}
	unavailable := errors.New("membership store unavailable")
	auth.members = func(context.Context, string, string) (string, bool, error) { return "", false, unavailable }
	if err := auth.Require(t.Context(), principal, BackupRetire, "project"); !errors.Is(err, unavailable) {
		t.Fatalf("lookup failure authorized retirement: %v", err)
	}
	auth.members = nil
	if err := auth.Require(t.Context(), principal, BackupRetire, "project"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing lookup authorized retirement: %v", err)
	}
}

func TestRetirementDenialDoesNotFailPermissionListing(t *testing.T) {
	auth := newAuthorizer(t, map[string]string{bob: "admin"})
	allowed, err := auth.Allowed(t.Context(), Principal{OrgID: org, UserID: bob}, BackupRetire)
	if err != nil || allowed {
		t.Fatalf("denial broke permission listing: allowed=%v err=%v", allowed, err)
	}
}
