package platformadmin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/xraph/authsome/rbac"
	"github.com/xraph/warden/assignment"
	wardenid "github.com/xraph/warden/id"
)

// RoleFinder finds a role of an app by its slug. authsome's rbac.Store is
// one.
type RoleFinder interface {
	GetRoleBySlug(ctx context.Context, appID, slug string) (*rbac.Role, error)
}

// RoleHolders lists the assignments of a role, as Warden's assignment store
// does. An app is Warden's tenant.
type RoleHolders interface {
	ListSubjectsForRole(ctx context.Context, tenantID string, roleID wardenid.RoleID) ([]*assignment.Assignment, error)
}

// OwnerAccount is what Owners needs to know about a user.
type OwnerAccount struct {
	Email  string
	Banned bool
}

// AccountLookup reads a user of the app by ID. It returns ErrNoUser when
// the user is gone.
type AccountLookup func(ctx context.Context, userID string) (OwnerAccount, error)

// OwnerEmails returns the email addresses of everyone who holds the
// platform-owner role in app appID at now, sorted and without repeats.
//
// The holders come from Warden's own assignment store, the same rows its
// role checks read, rather than from a query over Warden's or authsome's
// tables. A grant scoped to a resource, an expired grant, a subject that is
// not a user, a deleted or banned user and a user without an email are left
// out. A user that can't be read fails the call, so a hiccup isn't mistaken
// for having no owners.
func OwnerEmails(ctx context.Context, roles RoleFinder, holders RoleHolders, accounts AccountLookup, appID string, now time.Time) ([]string, error) {
	role, err := roles.GetRoleBySlug(ctx, appID, rbac.PlatformOwnerSlug)
	if err != nil {
		return nil, fmt.Errorf("find the %s role: %w", rbac.PlatformOwnerSlug, err)
	}
	roleID, err := wardenid.ParseRoleID(role.ID)
	if err != nil {
		return nil, fmt.Errorf("the %s role's id: %w", rbac.PlatformOwnerSlug, err)
	}
	held, err := holders.ListSubjectsForRole(ctx, appID, roleID)
	if err != nil {
		return nil, fmt.Errorf("list %s holders: %w", rbac.PlatformOwnerSlug, err)
	}
	seen := map[string]bool{}
	var emails []string
	for _, a := range held {
		if a.SubjectKind != "user" || a.ResourceType != "" || a.ResourceID != "" || !assignment.IsLive(a, now) || seen[a.SubjectID] {
			continue
		}
		seen[a.SubjectID] = true
		acct, lookupErr := accounts(ctx, a.SubjectID)
		if errors.Is(lookupErr, ErrNoUser) {
			continue
		}
		if lookupErr != nil {
			return nil, fmt.Errorf("read platform owner %s: %w", a.SubjectID, lookupErr)
		}
		email := strings.TrimSpace(acct.Email)
		if acct.Banned || email == "" || slices.Contains(emails, email) {
			continue
		}
		emails = append(emails, email)
	}
	slices.Sort(emails)
	return emails, nil
}
