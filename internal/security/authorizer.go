package security

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/xraph/warden"
	"github.com/xraph/warden/assignment"
	wardenid "github.com/xraph/warden/id"
	"github.com/xraph/warden/permission"
	"github.com/xraph/warden/role"
)

// rootNamespace is Warden's top namespace. Loomplane doesn't nest
// namespaces inside an organization (yet), so every role, permission and
// assignment lives here.
const rootNamespace = ""

// Store is the part of Warden's store the Authorizer writes through.
type Store interface {
	role.Store
	permission.Store
	assignment.Store
}

// Engine is the part of Warden's engine the Authorizer checks through.
type Engine interface {
	Check(ctx context.Context, req *warden.CheckRequest, opts ...warden.CallOption) (*warden.CheckResult, error)
	InvalidateSubject(ctx context.Context, tenantID string, subjectKind warden.SubjectKind, subjectID string)
}

// MembershipFunc reports a user's role in an organization, as authsome
// knows it. found is false when the user isn't a member.
type MembershipFunc func(ctx context.Context, orgID, userID string) (memberRole string, found bool, err error)

// Authorizer decides what a principal may do, through Warden.
//
// Warden holds one tenant per organization, with Catalog's roles in it.
// Membership changes reach Warden through the authsome plugin in this
// package (Sync). Require also repairs a member who has no role yet, which
// covers members who joined before Loomplane ran, or whose sync hook
// failed: authsome logs hook errors rather than failing the request.
type Authorizer struct {
	engine  Engine
	store   Store
	members MembershipFunc
	now     func() time.Time

	// ensured remembers the tenants whose catalog this process has already
	// written, so steady-state checks don't touch the catalog at all.
	ensured  sync.Map
	ensuring singleflight.Group
}

// NewAuthorizer returns an Authorizer. members may be nil, which turns off
// the repair in Require.
func NewAuthorizer(engine Engine, store Store, members MembershipFunc) *Authorizer {
	return &Authorizer{engine: engine, store: store, members: members, now: time.Now}
}

// Require returns nil when p may perform perm on the resource, ErrForbidden
// when it may not, and any other error when Warden couldn't decide.
func (a *Authorizer) Require(ctx context.Context, p Principal, perm Permission, resourceID string) error {
	if p.ReadOnly && !perm.Reads() {
		return fmt.Errorf("%w (%s): this API key can only read", ErrForbidden, perm.Name())
	}
	res, err := a.check(ctx, p, perm, resourceID)
	if err != nil {
		return err
	}
	if res.Allowed {
		return nil
	}
	// Only people have memberships to repair from. A service account's
	// role comes from EnsureTenant alone.
	if res.Decision == warden.DecisionDenyNoRoles && a.members != nil && !p.IsServiceAccount() {
		repaired, err := a.repair(ctx, p)
		if err != nil {
			return err
		}
		if repaired {
			if res, err = a.check(ctx, p, perm, resourceID); err != nil {
				return err
			}
			if res.Allowed {
				return nil
			}
		}
	}
	return fmt.Errorf("%w (%s)", ErrForbidden, perm.Name())
}

// Allowed reports whether p may perform perm, for answers such as which
// buttons the dashboard shows. It never repairs.
func (a *Authorizer) Allowed(ctx context.Context, p Principal, perm Permission) (bool, error) {
	if p.ReadOnly && !perm.Reads() {
		return false, nil
	}
	res, err := a.check(ctx, p, perm, "")
	if errors.Is(err, ErrForbidden) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return res.Allowed, nil
}

func (a *Authorizer) check(ctx context.Context, p Principal, perm Permission, resourceID string) (*warden.CheckResult, error) {
	if p.UserID == "" || p.OrgID == "" {
		return nil, ErrUnauthenticated
	}
	// The automation acts only in its own organization.
	if p.IsSystem() && p.UserID != SystemSubject(p.OrgID) {
		return nil, ErrForbidden
	}
	// Retirement destroys recovery bytes. A cached Warden assignment alone
	// cannot authorize it when a membership hook has failed or is delayed,
	// and only a person who owns the organization may do it.
	if perm == BackupRetire {
		if a.members == nil || p.IsServiceAccount() {
			return nil, ErrForbidden
		}
		memberRole, found, err := a.members(ctx, p.OrgID, p.UserID)
		if err != nil {
			return nil, fmt.Errorf("look up retirement membership: %w", err)
		}
		if !found || memberRole != MemberOwner {
			return nil, ErrForbidden
		}
	}
	// Once per organization per process, so an upgrade that grows a role
	// reaches organizations whose members already have one. Assign is the
	// only other caller, and it only runs when membership changes.
	if err := a.EnsureTenant(ctx, p.OrgID); err != nil {
		return nil, err
	}
	res, err := a.engine.Check(ctx, &warden.CheckRequest{
		Subject:  subjectFor(p),
		Action:   warden.Action{Name: perm.Action},
		Resource: warden.Resource{Type: perm.Resource, ID: resourceID},
		TenantID: p.OrgID,
	})
	if err != nil {
		return nil, fmt.Errorf("authorize %s: %w", perm.Name(), err)
	}
	return res, nil
}

// subjectFor is the Warden subject p is checked as: a service account for
// every principal that isn't a person, else a user.
func subjectFor(p Principal) warden.Subject {
	if p.IsServiceAccount() {
		return warden.Subject{Kind: warden.SubjectServiceAcct, ID: p.UserID}
	}
	return warden.Subject{Kind: warden.SubjectUser, ID: p.UserID}
}

// repair gives a member who has no role the one their membership calls for.
func (a *Authorizer) repair(ctx context.Context, p Principal) (bool, error) {
	memberRole, found, err := a.members(ctx, p.OrgID, p.UserID)
	if err != nil {
		return false, fmt.Errorf("look up membership: %w", err)
	}
	if !found {
		return false, nil
	}
	if err := a.Assign(ctx, p.OrgID, p.UserID, RoleForMember(memberRole), "loomplane:repair"); err != nil {
		return false, err
	}
	return true, nil
}

// EnsureTenant writes the role catalog into the organization's tenant. It's
// idempotent, and it brings an existing tenant's roles up to the current
// catalog, so a Loomplane upgrade that grows a role reaches every org. It
// also gives the organization's Loomplane automation its role, and only
// that role, so the identity exists whether or not the AI agent is on.
func (a *Authorizer) EnsureTenant(ctx context.Context, orgID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, done := a.ensured.Load(orgID); done {
		return nil
	}
	result := a.ensuring.DoChan(orgID, func() (any, error) {
		// Another initializer may have finished after the caller's cache read.
		if _, done := a.ensured.Load(orgID); done {
			return nil, nil
		}
		return nil, a.ensureTenant(ctx, orgID)
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case complete := <-result:
		return complete.Err
	}
}

func (a *Authorizer) ensureTenant(ctx context.Context, orgID string) error {
	now := a.now().UTC()
	for _, perm := range AllPermissions() {
		if err := a.ensurePermission(ctx, orgID, perm, now); err != nil {
			return err
		}
	}
	for _, def := range append(Catalog(), SystemRole()) {
		r, err := a.ensureRole(ctx, orgID, def, now)
		if err != nil {
			return err
		}
		refs := make([]permission.Ref, 0, len(def.Permissions))
		for _, perm := range def.Permissions {
			refs = append(refs, permission.Ref{NamespacePath: rootNamespace, Name: perm.Name()})
		}
		if err := a.store.SetRolePermissions(ctx, orgID, r.ID, refs); err != nil {
			return fmt.Errorf("set permissions of role %s in %s: %w", def.Slug, orgID, err)
		}
	}
	system := SystemSubject(orgID)
	if err := a.assign(ctx, orgID, warden.SubjectServiceAcct, system, RoleSystem, "loomplane"); err != nil {
		return err
	}
	// Its role's permissions may have just changed, assignment or not.
	a.engine.InvalidateSubject(ctx, orgID, warden.SubjectServiceAcct, system)
	a.ensured.Store(orgID, struct{}{})
	return nil
}

func (a *Authorizer) ensurePermission(ctx context.Context, orgID string, perm Permission, now time.Time) error {
	_, err := a.store.GetPermissionByName(ctx, orgID, rootNamespace, perm.Name())
	if err == nil {
		return nil
	}
	if !errors.Is(err, warden.ErrNotFound) {
		return fmt.Errorf("look up permission %s in %s: %w", perm.Name(), orgID, err)
	}
	err = a.store.CreatePermission(ctx, &permission.Permission{
		ID: wardenid.NewPermissionID(), TenantID: orgID, NamespacePath: rootNamespace,
		Name: perm.Name(), Resource: perm.Resource, Action: perm.Action,
		IsSystem: true, CreatedBy: "loomplane", CreatedAt: now, UpdatedAt: now,
	})
	// Another process may have created it between the lookup and here.
	if err != nil && !errors.Is(err, warden.ErrAlreadyExists) {
		return fmt.Errorf("create permission %s in %s: %w", perm.Name(), orgID, err)
	}
	return nil
}

func (a *Authorizer) ensureRole(ctx context.Context, orgID string, def RoleDef, now time.Time) (*role.Role, error) {
	r, err := a.store.GetRoleBySlug(ctx, orgID, rootNamespace, def.Slug)
	if err == nil {
		return r, nil
	}
	if !errors.Is(err, warden.ErrNotFound) {
		return nil, fmt.Errorf("look up role %s in %s: %w", def.Slug, orgID, err)
	}
	r = &role.Role{
		ID: wardenid.NewRoleID(), TenantID: orgID, NamespacePath: rootNamespace,
		Name: def.Name, Description: def.Description, Slug: def.Slug,
		IsSystem: true, CreatedBy: "loomplane", CreatedAt: now, UpdatedAt: now,
	}
	if err := a.store.CreateRole(ctx, r); err != nil {
		if !errors.Is(err, warden.ErrAlreadyExists) {
			return nil, fmt.Errorf("create role %s in %s: %w", def.Slug, orgID, err)
		}
		return a.store.GetRoleBySlug(ctx, orgID, rootNamespace, def.Slug)
	}
	return r, nil
}

// Assign makes roleSlug the user's only role in the organization.
func (a *Authorizer) Assign(ctx context.Context, orgID, userID, roleSlug, grantedBy string) error {
	// The automation's role is never a person's.
	if roleSlug == RoleSystem {
		return fmt.Errorf("%w: %s isn't a member role", ErrForbidden, RoleSystem)
	}
	if err := a.EnsureTenant(ctx, orgID); err != nil {
		return err
	}
	return a.assign(ctx, orgID, warden.SubjectUser, userID, roleSlug, grantedBy)
}

// assign makes roleSlug the subject's only role in the organization, and
// drops the engine's cache of the subject when that changes anything.
func (a *Authorizer) assign(ctx context.Context, orgID string, kind warden.SubjectKind, subjectID, roleSlug, grantedBy string) error {
	r, err := a.store.GetRoleBySlug(ctx, orgID, rootNamespace, roleSlug)
	if err != nil {
		return fmt.Errorf("look up role %s in %s: %w", roleSlug, orgID, err)
	}
	current, err := a.store.ListRolesForSubject(ctx, orgID, []string{rootNamespace}, string(kind), subjectID)
	if err != nil {
		return fmt.Errorf("list roles of %s in %s: %w", subjectID, orgID, err)
	}
	if len(current) == 1 && current[0] == r.ID {
		return nil
	}
	if len(current) > 0 {
		if err = a.store.DeleteAssignmentsBySubject(ctx, orgID, string(kind), subjectID); err != nil {
			return fmt.Errorf("clear roles of %s in %s: %w", subjectID, orgID, err)
		}
	}
	err = a.store.CreateAssignment(ctx, &assignment.Assignment{
		ID: wardenid.NewAssignmentID(), TenantID: orgID, NamespacePath: rootNamespace,
		RoleID: r.ID, SubjectKind: string(kind), SubjectID: subjectID,
		GrantedBy: grantedBy, CreatedAt: a.now().UTC(),
	})
	if err != nil && !errors.Is(err, warden.ErrAlreadyExists) {
		return fmt.Errorf("assign %s to %s in %s: %w", roleSlug, subjectID, orgID, err)
	}
	a.engine.InvalidateSubject(ctx, orgID, kind, subjectID)
	return nil
}

// Revoke removes every role the user holds in the organization.
func (a *Authorizer) Revoke(ctx context.Context, orgID, userID string) error {
	if err := a.store.DeleteAssignmentsBySubject(ctx, orgID, string(warden.SubjectUser), userID); err != nil {
		return fmt.Errorf("revoke roles of %s in %s: %w", userID, orgID, err)
	}
	a.engine.InvalidateSubject(ctx, orgID, warden.SubjectUser, userID)
	return nil
}
