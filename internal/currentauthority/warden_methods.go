// Explicit forwarding for the reviewed Warden v1.8.1 Store contract.
package currentauthority

import (
	"context"
	"time"

	"github.com/xraph/warden"
	"github.com/xraph/warden/assignment"
	"github.com/xraph/warden/checklog"
	"github.com/xraph/warden/id"
	"github.com/xraph/warden/permission"
	"github.com/xraph/warden/policy"
	"github.com/xraph/warden/relation"
	"github.com/xraph/warden/resourcetype"
	"github.com/xraph/warden/role"
	"github.com/xraph/warden/store"
)

func (router *storeRouter) AttachPermission(ctx context.Context, tenantID string, roleID id.RoleID, ref permission.Ref) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.AttachPermission(ctx, tenantID, roleID, ref)
	})
	return err
}

func (router *storeRouter) CheckDirectRelation(ctx context.Context, tenantID string, namespacePaths []string, objectType, objectID, rel, subjectType, subjectID string) (bool, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) (bool, error) {
		return s.CheckDirectRelation(ctx, tenantID, namespacePaths, objectType, objectID, rel, subjectType, subjectID)
	})
}

func (router *storeRouter) CountAssignments(ctx context.Context, filter *assignment.ListFilter) (int64, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) (int64, error) {
		return s.CountAssignments(ctx, filter)
	})
}

func (router *storeRouter) CountCheckLogs(ctx context.Context, filter *checklog.QueryFilter) (int64, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) (int64, error) {
		return s.CountCheckLogs(ctx, filter)
	})
}

func (router *storeRouter) CountPermissions(ctx context.Context, filter *permission.ListFilter) (int64, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) (int64, error) {
		return s.CountPermissions(ctx, filter)
	})
}

func (router *storeRouter) CountPolicies(ctx context.Context, filter *policy.ListFilter) (int64, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) (int64, error) {
		return s.CountPolicies(ctx, filter)
	})
}

func (router *storeRouter) CountRelations(ctx context.Context, filter *relation.ListFilter) (int64, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) (int64, error) {
		return s.CountRelations(ctx, filter)
	})
}

func (router *storeRouter) CountResourceTypes(ctx context.Context, filter *resourcetype.ListFilter) (int64, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) (int64, error) {
		return s.CountResourceTypes(ctx, filter)
	})
}

func (router *storeRouter) CountRoles(ctx context.Context, filter *role.ListFilter) (int64, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) (int64, error) {
		return s.CountRoles(ctx, filter)
	})
}

func (router *storeRouter) CreateAssignment(ctx context.Context, a *assignment.Assignment) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.CreateAssignment(ctx, a)
	})
	return err
}

func (router *storeRouter) CreateCheckLog(ctx context.Context, e *checklog.Entry) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.CreateCheckLog(ctx, e)
	})
	return err
}

func (router *storeRouter) CreatePermission(ctx context.Context, p *permission.Permission) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.CreatePermission(ctx, p)
	})
	return err
}

func (router *storeRouter) CreatePolicy(ctx context.Context, p *policy.Policy) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.CreatePolicy(ctx, p)
	})
	return err
}

func (router *storeRouter) CreateRelation(ctx context.Context, t *relation.Tuple) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.CreateRelation(ctx, t)
	})
	return err
}

func (router *storeRouter) CreateResourceType(ctx context.Context, rt *resourcetype.ResourceType) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.CreateResourceType(ctx, rt)
	})
	return err
}

func (router *storeRouter) CreateRole(ctx context.Context, r *role.Role) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.CreateRole(ctx, r)
	})
	return err
}

func (router *storeRouter) DeleteAssignment(ctx context.Context, tenantID string, assID id.AssignmentID) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DeleteAssignment(ctx, tenantID, assID)
	})
	return err
}

func (router *storeRouter) DeleteAssignmentsByRole(ctx context.Context, tenantID string, roleID id.RoleID) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DeleteAssignmentsByRole(ctx, tenantID, roleID)
	})
	return err
}

func (router *storeRouter) DeleteAssignmentsBySubject(ctx context.Context, tenantID, subjectKind, subjectID string) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DeleteAssignmentsBySubject(ctx, tenantID, subjectKind, subjectID)
	})
	return err
}

func (router *storeRouter) DeleteAssignmentsByTenant(ctx context.Context, tenantID string) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DeleteAssignmentsByTenant(ctx, tenantID)
	})
	return err
}

func (router *storeRouter) DeleteCheckLogsBySubject(ctx context.Context, tenantID, subjectKind, subjectID string) (int64, error) {
	return routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (int64, error) {
		return s.DeleteCheckLogsBySubject(ctx, tenantID, subjectKind, subjectID)
	})
}

func (router *storeRouter) DeleteCheckLogsByTenant(ctx context.Context, tenantID string) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DeleteCheckLogsByTenant(ctx, tenantID)
	})
	return err
}

func (router *storeRouter) DeleteExpiredAssignments(ctx context.Context, now time.Time) (int64, error) {
	return routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (int64, error) {
		return s.DeleteExpiredAssignments(ctx, now)
	})
}

func (router *storeRouter) DeleteExpiredAssignmentsForTenant(ctx context.Context, tenantID string, now time.Time) (int64, error) {
	return routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (int64, error) {
		return s.DeleteExpiredAssignmentsForTenant(ctx, tenantID, now)
	})
}

func (router *storeRouter) DeletePermission(ctx context.Context, tenantID string, permID id.PermissionID) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DeletePermission(ctx, tenantID, permID)
	})
	return err
}

func (router *storeRouter) DeletePermissionsByTenant(ctx context.Context, tenantID string) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DeletePermissionsByTenant(ctx, tenantID)
	})
	return err
}

func (router *storeRouter) DeletePoliciesByTenant(ctx context.Context, tenantID string) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DeletePoliciesByTenant(ctx, tenantID)
	})
	return err
}

func (router *storeRouter) DeletePolicy(ctx context.Context, tenantID string, polID id.PolicyID) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DeletePolicy(ctx, tenantID, polID)
	})
	return err
}

func (router *storeRouter) DeleteRelation(ctx context.Context, tenantID string, relID id.RelationID) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DeleteRelation(ctx, tenantID, relID)
	})
	return err
}

func (router *storeRouter) DeleteRelationTuple(ctx context.Context, tenantID, namespacePath, objectType, objectID, rel, subjectType, subjectID, subjectRelation string) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DeleteRelationTuple(ctx, tenantID, namespacePath, objectType, objectID, rel, subjectType, subjectID, subjectRelation)
	})
	return err
}

func (router *storeRouter) DeleteRelationsByObject(ctx context.Context, tenantID, objectType, objectID string) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DeleteRelationsByObject(ctx, tenantID, objectType, objectID)
	})
	return err
}

func (router *storeRouter) DeleteRelationsBySubject(ctx context.Context, tenantID, subjectType, subjectID string) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DeleteRelationsBySubject(ctx, tenantID, subjectType, subjectID)
	})
	return err
}

func (router *storeRouter) DeleteRelationsByTenant(ctx context.Context, tenantID string) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DeleteRelationsByTenant(ctx, tenantID)
	})
	return err
}

func (router *storeRouter) DeleteResourceType(ctx context.Context, tenantID string, rtID id.ResourceTypeID) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DeleteResourceType(ctx, tenantID, rtID)
	})
	return err
}

func (router *storeRouter) DeleteResourceTypesByTenant(ctx context.Context, tenantID string) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DeleteResourceTypesByTenant(ctx, tenantID)
	})
	return err
}

func (router *storeRouter) DeleteRole(ctx context.Context, tenantID string, roleID id.RoleID) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DeleteRole(ctx, tenantID, roleID)
	})
	return err
}

func (router *storeRouter) DeleteRolesByTenant(ctx context.Context, tenantID string) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DeleteRolesByTenant(ctx, tenantID)
	})
	return err
}

func (router *storeRouter) DetachPermission(ctx context.Context, tenantID string, roleID id.RoleID, ref permission.Ref) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.DetachPermission(ctx, tenantID, roleID, ref)
	})
	return err
}

func (router *storeRouter) GetAssignment(ctx context.Context, tenantID string, assID id.AssignmentID) (*assignment.Assignment, error) {
	return routeStore(ctx, router, true, warden.ErrAssignmentNotFound, func(ctx context.Context, s store.Store) (*assignment.Assignment, error) {
		return s.GetAssignment(ctx, tenantID, assID)
	})
}

func (router *storeRouter) GetCheckLog(ctx context.Context, tenantID string, logID id.CheckLogID) (*checklog.Entry, error) {
	return routeStore(ctx, router, true, warden.ErrCheckLogNotFound, func(ctx context.Context, s store.Store) (*checklog.Entry, error) {
		return s.GetCheckLog(ctx, tenantID, logID)
	})
}

func (router *storeRouter) GetPermission(ctx context.Context, tenantID string, permID id.PermissionID) (*permission.Permission, error) {
	return routeStore(ctx, router, true, warden.ErrPermissionNotFound, func(ctx context.Context, s store.Store) (*permission.Permission, error) {
		return s.GetPermission(ctx, tenantID, permID)
	})
}

func (router *storeRouter) GetPermissionByName(ctx context.Context, tenantID, namespacePath, name string) (*permission.Permission, error) {
	return routeStore(ctx, router, true, warden.ErrPermissionNotFound, func(ctx context.Context, s store.Store) (*permission.Permission, error) {
		return s.GetPermissionByName(ctx, tenantID, namespacePath, name)
	})
}

func (router *storeRouter) GetPolicy(ctx context.Context, tenantID string, polID id.PolicyID) (*policy.Policy, error) {
	return routeStore(ctx, router, true, warden.ErrPolicyNotFound, func(ctx context.Context, s store.Store) (*policy.Policy, error) {
		return s.GetPolicy(ctx, tenantID, polID)
	})
}

func (router *storeRouter) GetPolicyByName(ctx context.Context, tenantID, namespacePath, name string) (*policy.Policy, error) {
	return routeStore(ctx, router, true, warden.ErrPolicyNotFound, func(ctx context.Context, s store.Store) (*policy.Policy, error) {
		return s.GetPolicyByName(ctx, tenantID, namespacePath, name)
	})
}

func (router *storeRouter) GetRelation(ctx context.Context, tenantID string, relID id.RelationID) (*relation.Tuple, error) {
	return routeStore(ctx, router, true, warden.ErrRelationNotFound, func(ctx context.Context, s store.Store) (*relation.Tuple, error) {
		return s.GetRelation(ctx, tenantID, relID)
	})
}

func (router *storeRouter) GetResourceType(ctx context.Context, tenantID string, rtID id.ResourceTypeID) (*resourcetype.ResourceType, error) {
	return routeStore(ctx, router, true, warden.ErrResourceTypeNotFound, func(ctx context.Context, s store.Store) (*resourcetype.ResourceType, error) {
		return s.GetResourceType(ctx, tenantID, rtID)
	})
}

func (router *storeRouter) GetResourceTypeByName(ctx context.Context, tenantID, namespacePath, name string) (*resourcetype.ResourceType, error) {
	return routeStore(ctx, router, true, warden.ErrResourceTypeNotFound, func(ctx context.Context, s store.Store) (*resourcetype.ResourceType, error) {
		return s.GetResourceTypeByName(ctx, tenantID, namespacePath, name)
	})
}

func (router *storeRouter) GetRole(ctx context.Context, tenantID string, roleID id.RoleID) (*role.Role, error) {
	return routeStore(ctx, router, true, warden.ErrRoleNotFound, func(ctx context.Context, s store.Store) (*role.Role, error) {
		return s.GetRole(ctx, tenantID, roleID)
	})
}

func (router *storeRouter) GetRoleBySlug(ctx context.Context, tenantID, namespacePath, slug string) (*role.Role, error) {
	return routeStore(ctx, router, true, warden.ErrRoleNotFound, func(ctx context.Context, s store.Store) (*role.Role, error) {
		return s.GetRoleBySlug(ctx, tenantID, namespacePath, slug)
	})
}

func (router *storeRouter) GetRoles(ctx context.Context, tenantID string, roleIDs []id.RoleID) ([]*role.Role, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]*role.Role, error) {
		return s.GetRoles(ctx, tenantID, roleIDs)
	})
}

func (router *storeRouter) ListActivePolicies(ctx context.Context, tenantID string, namespacePaths []string) ([]*policy.Policy, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]*policy.Policy, error) {
		return s.ListActivePolicies(ctx, tenantID, namespacePaths)
	})
}

func (router *storeRouter) ListAssignments(ctx context.Context, filter *assignment.ListFilter) ([]*assignment.Assignment, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]*assignment.Assignment, error) {
		return s.ListAssignments(ctx, filter)
	})
}

func (router *storeRouter) ListCheckLogs(ctx context.Context, filter *checklog.QueryFilter) ([]*checklog.Entry, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]*checklog.Entry, error) {
		return s.ListCheckLogs(ctx, filter)
	})
}

func (router *storeRouter) ListChildRoles(ctx context.Context, tenantID, parentSlug string) ([]*role.Role, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]*role.Role, error) {
		return s.ListChildRoles(ctx, tenantID, parentSlug)
	})
}

func (router *storeRouter) ListExpiringAssignments(ctx context.Context, tenantID string, before time.Time, limit int) ([]*assignment.Assignment, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]*assignment.Assignment, error) {
		return s.ListExpiringAssignments(ctx, tenantID, before, limit)
	})
}

func (router *storeRouter) ListPermissions(ctx context.Context, filter *permission.ListFilter) ([]*permission.Permission, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]*permission.Permission, error) {
		return s.ListPermissions(ctx, filter)
	})
}

func (router *storeRouter) ListPermissionsByRole(ctx context.Context, tenantID string, roleID id.RoleID) ([]*permission.Permission, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]*permission.Permission, error) {
		return s.ListPermissionsByRole(ctx, tenantID, roleID)
	})
}

func (router *storeRouter) ListPermissionsBySubject(ctx context.Context, tenantID, subjectKind, subjectID string) ([]*permission.Permission, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]*permission.Permission, error) {
		return s.ListPermissionsBySubject(ctx, tenantID, subjectKind, subjectID)
	})
}

func (router *storeRouter) ListPolicies(ctx context.Context, filter *policy.ListFilter) ([]*policy.Policy, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]*policy.Policy, error) {
		return s.ListPolicies(ctx, filter)
	})
}

func (router *storeRouter) ListRelationObjects(ctx context.Context, tenantID, namespacePath, subjectType, subjectID, rel string, limit int) ([]*relation.Tuple, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]*relation.Tuple, error) {
		return s.ListRelationObjects(ctx, tenantID, namespacePath, subjectType, subjectID, rel, limit)
	})
}

func (router *storeRouter) ListRelationSubjects(ctx context.Context, tenantID string, namespacePaths []string, objectType, objectID, rel string, limit int) ([]*relation.Tuple, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]*relation.Tuple, error) {
		return s.ListRelationSubjects(ctx, tenantID, namespacePaths, objectType, objectID, rel, limit)
	})
}

func (router *storeRouter) ListRelations(ctx context.Context, filter *relation.ListFilter) ([]*relation.Tuple, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]*relation.Tuple, error) {
		return s.ListRelations(ctx, filter)
	})
}

func (router *storeRouter) ListResourceTypes(ctx context.Context, filter *resourcetype.ListFilter) ([]*resourcetype.ResourceType, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]*resourcetype.ResourceType, error) {
		return s.ListResourceTypes(ctx, filter)
	})
}

func (router *storeRouter) ListRolePermissions(ctx context.Context, tenantID string, roleID id.RoleID) ([]*permission.Permission, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]*permission.Permission, error) {
		return s.ListRolePermissions(ctx, tenantID, roleID)
	})
}

func (router *storeRouter) ListRolePermissionsForRoles(ctx context.Context, tenantID string, roleIDs []id.RoleID) (map[id.RoleID][]*permission.Permission, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) (map[id.RoleID][]*permission.Permission, error) {
		return s.ListRolePermissionsForRoles(ctx, tenantID, roleIDs)
	})
}

func (router *storeRouter) ListRoles(ctx context.Context, filter *role.ListFilter) ([]*role.Role, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]*role.Role, error) {
		return s.ListRoles(ctx, filter)
	})
}

func (router *storeRouter) ListRolesForSubject(ctx context.Context, tenantID string, namespacePaths []string, subjectKind, subjectID string) ([]id.RoleID, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]id.RoleID, error) {
		return s.ListRolesForSubject(ctx, tenantID, namespacePaths, subjectKind, subjectID)
	})
}

func (router *storeRouter) ListRolesForSubjectOnResource(ctx context.Context, tenantID string, namespacePaths []string, subjectKind, subjectID, resourceType, resourceID string) ([]id.RoleID, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]id.RoleID, error) {
		return s.ListRolesForSubjectOnResource(ctx, tenantID, namespacePaths, subjectKind, subjectID, resourceType, resourceID)
	})
}

func (router *storeRouter) ListSubjectsForRole(ctx context.Context, tenantID string, roleID id.RoleID) ([]*assignment.Assignment, error) {
	return routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) ([]*assignment.Assignment, error) {
		return s.ListSubjectsForRole(ctx, tenantID, roleID)
	})
}

func (router *storeRouter) Migrate(ctx context.Context) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.Migrate(ctx)
	})
	return err
}

func (router *storeRouter) Ping(ctx context.Context) error {
	_, err := routeStore(ctx, router, true, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.Ping(ctx)
	})
	return err
}

func (router *storeRouter) PurgeCheckLogs(ctx context.Context, before time.Time) (int64, error) {
	return routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (int64, error) {
		return s.PurgeCheckLogs(ctx, before)
	})
}

func (router *storeRouter) PurgeCheckLogsForTenant(ctx context.Context, tenantID string, before time.Time) (int64, error) {
	return routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (int64, error) {
		return s.PurgeCheckLogsForTenant(ctx, tenantID, before)
	})
}

func (router *storeRouter) SetPolicyVersion(ctx context.Context, tenantID string, polID id.PolicyID, version int) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.SetPolicyVersion(ctx, tenantID, polID, version)
	})
	return err
}

func (router *storeRouter) SetRolePermissions(ctx context.Context, tenantID string, roleID id.RoleID, refs []permission.Ref) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.SetRolePermissions(ctx, tenantID, roleID, refs)
	})
	return err
}

func (router *storeRouter) UpdatePermission(ctx context.Context, p *permission.Permission) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.UpdatePermission(ctx, p)
	})
	return err
}

func (router *storeRouter) UpdatePolicy(ctx context.Context, p *policy.Policy) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.UpdatePolicy(ctx, p)
	})
	return err
}

func (router *storeRouter) UpdatePolicyIfVersion(ctx context.Context, p *policy.Policy, expected int) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.UpdatePolicyIfVersion(ctx, p, expected)
	})
	return err
}

func (router *storeRouter) UpdateResourceType(ctx context.Context, rt *resourcetype.ResourceType) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.UpdateResourceType(ctx, rt)
	})
	return err
}

func (router *storeRouter) UpdateRole(ctx context.Context, r *role.Role) error {
	_, err := routeStore(ctx, router, false, nil, func(ctx context.Context, s store.Store) (struct{}, error) {
		return struct{}{}, s.UpdateRole(ctx, r)
	})
	return err
}
