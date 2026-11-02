package security

import (
	"context"
	"errors"
	"testing"
)

func TestClusterApprovalRequiresCurrentMembership(t *testing.T) {
	member := true
	calls := 0
	authorizer := NewAuthorizer(nil, nil, func(context.Context, string, string) (string, bool, error) { calls++; return MemberOwner, member, nil })
	principal := Principal{OrgID: "org", UserID: "user"}
	if err := authorizer.RequireCurrentMembership(t.Context(), principal); err != nil {
		t.Fatal(err)
	}
	member = false
	if err := authorizer.RequireCurrentMembership(t.Context(), principal); !errors.Is(err, ErrNotMember) {
		t.Fatal("saved member survived revocation", err)
	}
	if calls != 2 {
		t.Fatal("membership used a stale cache")
	}
	if err := NewAuthorizer(nil, nil, nil).RequireCurrentMembership(t.Context(), principal); !errors.Is(err, ErrForbidden) {
		t.Fatal("missing identity source accepted", err)
	}
}
