package platformadmin

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/xraph/authsome/rbac"
	"github.com/xraph/warden/assignment"
	wardenid "github.com/xraph/warden/id"
)

type ownerRole struct {
	role *rbac.Role
	err  error
}

func (f ownerRole) GetRoleBySlug(_ context.Context, appID, slug string) (*rbac.Role, error) {
	if f.err != nil {
		return nil, f.err
	}
	if appID != "aapp_1" || slug != rbac.PlatformOwnerSlug {
		return nil, rbac.ErrRoleNotFound
	}
	return f.role, nil
}

type fakeHolders struct {
	tenant string
	role   wardenid.RoleID
	held   []*assignment.Assignment
	err    error
}

func (f *fakeHolders) ListSubjectsForRole(_ context.Context, tenantID string, roleID wardenid.RoleID) ([]*assignment.Assignment, error) {
	f.tenant, f.role = tenantID, roleID
	return f.held, f.err
}

func TestOwnerEmailsListsLiveOwnersOnly(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	roleID := wardenid.NewRoleID()
	holders := &fakeHolders{held: []*assignment.Assignment{
		{SubjectKind: "user", SubjectID: "ausr_b"},
		{SubjectKind: "user", SubjectID: "ausr_a", ExpiresAt: &future},
		{SubjectKind: "user", SubjectID: "ausr_a"},                            // the same user twice
		{SubjectKind: "user", SubjectID: "ausr_expired", ExpiresAt: &past},    // expired grant
		{SubjectKind: "user", SubjectID: "ausr_scoped", ResourceType: "host"}, // not a platform-wide grant
		{SubjectKind: "group", SubjectID: "grp_1"},                            // not a user
		{SubjectKind: "user", SubjectID: "ausr_gone"},
		{SubjectKind: "user", SubjectID: "ausr_banned"},
		{SubjectKind: "user", SubjectID: "ausr_nomail"},
		{SubjectKind: "user", SubjectID: "ausr_alias"}, // shares b's address
	}}
	accounts := map[string]OwnerAccount{
		"ausr_a":       {Email: "ada@example.com"},
		"ausr_b":       {Email: " bo@example.com "},
		"ausr_expired": {Email: "expired@example.com"},
		"ausr_scoped":  {Email: "scoped@example.com"},
		"ausr_banned":  {Email: "banned@example.com", Banned: true},
		"ausr_nomail":  {},
		"ausr_alias":   {Email: "bo@example.com"},
	}
	var looked []string
	lookup := func(_ context.Context, userID string) (OwnerAccount, error) {
		looked = append(looked, userID)
		a, ok := accounts[userID]
		if !ok {
			return OwnerAccount{}, ErrNoUser
		}
		return a, nil
	}
	got, err := OwnerEmails(context.Background(), ownerRole{role: &rbac.Role{ID: roleID.String(), Slug: rbac.PlatformOwnerSlug}}, holders, lookup, "aapp_1", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ada@example.com", "bo@example.com"}; !slices.Equal(got, want) {
		t.Fatalf("owners = %v, want %v", got, want)
	}
	if holders.tenant != "aapp_1" || holders.role != roleID {
		t.Fatalf("listed role %s in %q", holders.role, holders.tenant)
	}
	for _, skipped := range []string{"ausr_expired", "ausr_scoped", "grp_1"} {
		if slices.Contains(looked, skipped) {
			t.Fatalf("read %s, which holds no live platform-wide grant", skipped)
		}
	}
	if n := len(slices.DeleteFunc(slices.Clone(looked), func(s string) bool { return s != "ausr_a" })); n != 1 {
		t.Fatalf("read ausr_a %d times", n)
	}
}

func TestOwnerEmailsFailsRatherThanReportingNoOwners(t *testing.T) {
	ctx := context.Background()
	roleID := wardenid.NewRoleID()
	roles := ownerRole{role: &rbac.Role{ID: roleID.String()}}
	boom := errors.New("boom")
	none := func(context.Context, string) (OwnerAccount, error) { return OwnerAccount{}, ErrNoUser }

	if _, err := OwnerEmails(ctx, ownerRole{err: boom}, &fakeHolders{}, none, "aapp_1", time.Now()); !errors.Is(err, boom) {
		t.Fatalf("role lookup error = %v", err)
	}
	if _, err := OwnerEmails(ctx, roles, &fakeHolders{err: boom}, none, "aapp_1", time.Now()); !errors.Is(err, boom) {
		t.Fatalf("holder list error = %v", err)
	}
	broken := func(context.Context, string) (OwnerAccount, error) { return OwnerAccount{}, boom }
	held := &fakeHolders{held: []*assignment.Assignment{{SubjectKind: "user", SubjectID: "ausr_a"}}}
	if _, err := OwnerEmails(ctx, roles, held, broken, "aapp_1", time.Now()); !errors.Is(err, boom) {
		t.Fatalf("account read error = %v", err)
	}
	got, err := OwnerEmails(ctx, roles, &fakeHolders{}, none, "aapp_1", time.Now())
	if err != nil || len(got) != 0 {
		t.Fatalf("no holders: %v, %v", got, err)
	}
}
