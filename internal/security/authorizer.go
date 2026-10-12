package security

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

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
	ensured sync.Map
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
	if res.Decision == warden.DecisionDenyNoRoles && a.members != nil {
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
	if err != nil {
		return false, err
	}
	return res.Allowed, nil
}

func (a *Authorizer) check(ctx context.Context, p Principal, perm Permission, resourceID string) (*warden.CheckResult, error) {
	if p.UserID == "" || p.OrgID == "" {
		return nil, ErrUnauthenticated
	}
	// Once per organization per process, so an upgrade that grows a role
	// reaches organizations whose members already have one. Assign is the
	// only other caller, and it only runs when membership changes.
	if err := a.EnsureTenant(ctx, p.OrgID); err != nil {
		return nil, err
	}
	res, err := a.engine.Check(ctx, &warden.CheckRequest{
		Subject:  warden.Subject{Kind: warden.SubjectUser, ID: p.UserID},
		Action:   warden.Action{Name: perm.Action},
		Resource: warden.Resource{Type: perm.Resource, ID: resourceID},
		TenantID: p.OrgID,
	})
	if err != nil {
		return nil, fmt.Errorf("authorize %s: %w", perm.Name(), err)
	}
	return res, nil
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
// catalog, so a Loomplane upgrade that grows a role reaches every org.
func (a *Authorizer) EnsureTenant(ctx context.Context, orgID string) error {
	if _, done := a.ensured.Load(orgID); done {
		return nil
	}
	now := a.now().UTC()
	for _, perm := range AllPermissions() {
		if err := a.ensurePermission(ctx, orgID, perm, now); err != nil {
			return err
		}
	}
	for _, def := range Catalog() {
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
	if err := a.EnsureTenant(ctx, orgID); err != nil {
		return err
	}
	r, err := a.store.GetRoleBySlug(ctx, orgID, rootNamespace, roleSlug)
	if err != nil {
		return fmt.Errorf("look up role %s in %s: %w", roleSlug, orgID, err)
	}
	current, err := a.store.ListRolesForSubject(ctx, orgID, []string{rootNamespace}, string(warden.SubjectUser), userID)
	if err != nil {
		return fmt.Errorf("list roles of %s in %s: %w", userID, orgID, err)
	}
	if len(current) == 1 && current[0] == r.ID {
		return nil
	}
	if len(current) > 0 {
		if err = a.store.DeleteAssignmentsBySubject(ctx, orgID, string(warden.SubjectUser), userID); err != nil {
			return fmt.Errorf("clear roles of %s in %s: %w", userID, orgID, err)
		}
	}
	err = a.store.CreateAssignment(ctx, &assignment.Assignment{
		ID: wardenid.NewAssignmentID(), TenantID: orgID, NamespacePath: rootNamespace,
		RoleID: r.ID, SubjectKind: string(warden.SubjectUser), SubjectID: userID,
		GrantedBy: grantedBy, CreatedAt: a.now().UTC(),
	})
	if err != nil && !errors.Is(err, warden.ErrAlreadyExists) {
		return fmt.Errorf("assign %s to %s in %s: %w", roleSlug, userID, orgID, err)
	}
	a.engine.InvalidateSubject(ctx, orgID, warden.SubjectUser, userID)
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
