package currentauthority

import (
	"context"
	"time"

	"github.com/xraph/warden/assignment"
	"github.com/xraph/warden/checklog"
	"github.com/xraph/warden/id"
	"github.com/xraph/warden/permission"
	"github.com/xraph/warden/policy"
	"github.com/xraph/warden/relation"
	"github.com/xraph/warden/resourcetype"
	"github.com/xraph/warden/role"
)

func (s *recordingStore) AttachPermission(ctx context.Context, tenantID string, roleID id.RoleID, ref permission.Ref) error {
	err := s.record("AttachPermission", ctx, tenantID, roleID, ref)
	return err
}

func (s *recordingStore) CheckDirectRelation(ctx context.Context, tenantID string, namespacePaths []string, objectType, objectID, rel, subjectType, subjectID string) (bool, error) {
	err := s.record("CheckDirectRelation", ctx, tenantID, namespacePaths, objectType, objectID, rel, subjectType, subjectID)
	return recordedValue[bool](s), err
}

func (s *recordingStore) CountAssignments(ctx context.Context, filter *assignment.ListFilter) (int64, error) {
	err := s.record("CountAssignments", ctx, filter)
	return recordedValue[int64](s), err
}

func (s *recordingStore) CountCheckLogs(ctx context.Context, filter *checklog.QueryFilter) (int64, error) {
	err := s.record("CountCheckLogs", ctx, filter)
	return recordedValue[int64](s), err
}

func (s *recordingStore) CountPermissions(ctx context.Context, filter *permission.ListFilter) (int64, error) {
	err := s.record("CountPermissions", ctx, filter)
	return recordedValue[int64](s), err
}

func (s *recordingStore) CountPolicies(ctx context.Context, filter *policy.ListFilter) (int64, error) {
	err := s.record("CountPolicies", ctx, filter)
	return recordedValue[int64](s), err
}

func (s *recordingStore) CountRelations(ctx context.Context, filter *relation.ListFilter) (int64, error) {
	err := s.record("CountRelations", ctx, filter)
	return recordedValue[int64](s), err
}

func (s *recordingStore) CountResourceTypes(ctx context.Context, filter *resourcetype.ListFilter) (int64, error) {
	err := s.record("CountResourceTypes", ctx, filter)
	return recordedValue[int64](s), err
}

func (s *recordingStore) CountRoles(ctx context.Context, filter *role.ListFilter) (int64, error) {
	err := s.record("CountRoles", ctx, filter)
	return recordedValue[int64](s), err
}

func (s *recordingStore) CreateAssignment(ctx context.Context, a *assignment.Assignment) error {
	err := s.record("CreateAssignment", ctx, a)
	return err
}

func (s *recordingStore) CreateCheckLog(ctx context.Context, e *checklog.Entry) error {
	err := s.record("CreateCheckLog", ctx, e)
	return err
}

func (s *recordingStore) CreatePermission(ctx context.Context, p *permission.Permission) error {
	err := s.record("CreatePermission", ctx, p)
	return err
}

func (s *recordingStore) CreatePolicy(ctx context.Context, p *policy.Policy) error {
	err := s.record("CreatePolicy", ctx, p)
	return err
}

func (s *recordingStore) CreateRelation(ctx context.Context, t *relation.Tuple) error {
	err := s.record("CreateRelation", ctx, t)
	return err
}

func (s *recordingStore) CreateResourceType(ctx context.Context, rt *resourcetype.ResourceType) error {
	err := s.record("CreateResourceType", ctx, rt)
	return err
}

func (s *recordingStore) CreateRole(ctx context.Context, r *role.Role) error {
	err := s.record("CreateRole", ctx, r)
	return err
}

func (s *recordingStore) DeleteAssignment(ctx context.Context, tenantID string, assID id.AssignmentID) error {
	err := s.record("DeleteAssignment", ctx, tenantID, assID)
	return err
}

func (s *recordingStore) DeleteAssignmentsByRole(ctx context.Context, tenantID string, roleID id.RoleID) error {
	err := s.record("DeleteAssignmentsByRole", ctx, tenantID, roleID)
	return err
}

func (s *recordingStore) DeleteAssignmentsBySubject(ctx context.Context, tenantID, subjectKind, subjectID string) error {
	err := s.record("DeleteAssignmentsBySubject", ctx, tenantID, subjectKind, subjectID)
	return err
}

func (s *recordingStore) DeleteAssignmentsByTenant(ctx context.Context, tenantID string) error {
	err := s.record("DeleteAssignmentsByTenant", ctx, tenantID)
	return err
}

func (s *recordingStore) DeleteCheckLogsBySubject(ctx context.Context, tenantID, subjectKind, subjectID string) (int64, error) {
	err := s.record("DeleteCheckLogsBySubject", ctx, tenantID, subjectKind, subjectID)
	return recordedValue[int64](s), err
}

func (s *recordingStore) DeleteCheckLogsByTenant(ctx context.Context, tenantID string) error {
	err := s.record("DeleteCheckLogsByTenant", ctx, tenantID)
	return err
}

func (s *recordingStore) DeleteExpiredAssignments(ctx context.Context, now time.Time) (int64, error) {
	err := s.record("DeleteExpiredAssignments", ctx, now)
	return recordedValue[int64](s), err
}

func (s *recordingStore) DeleteExpiredAssignmentsForTenant(ctx context.Context, tenantID string, now time.Time) (int64, error) {
	err := s.record("DeleteExpiredAssignmentsForTenant", ctx, tenantID, now)
	return recordedValue[int64](s), err
}

func (s *recordingStore) DeletePermission(ctx context.Context, tenantID string, permID id.PermissionID) error {
	err := s.record("DeletePermission", ctx, tenantID, permID)
	return err
}

func (s *recordingStore) DeletePermissionsByTenant(ctx context.Context, tenantID string) error {
	err := s.record("DeletePermissionsByTenant", ctx, tenantID)
	return err
}

func (s *recordingStore) DeletePoliciesByTenant(ctx context.Context, tenantID string) error {
	err := s.record("DeletePoliciesByTenant", ctx, tenantID)
	return err
}

func (s *recordingStore) DeletePolicy(ctx context.Context, tenantID string, polID id.PolicyID) error {
	err := s.record("DeletePolicy", ctx, tenantID, polID)
	return err
}

func (s *recordingStore) DeleteRelation(ctx context.Context, tenantID string, relID id.RelationID) error {
	err := s.record("DeleteRelation", ctx, tenantID, relID)
	return err
}

func (s *recordingStore) DeleteRelationTuple(ctx context.Context, tenantID, namespacePath, objectType, objectID, rel, subjectType, subjectID, subjectRelation string) error {
	err := s.record("DeleteRelationTuple", ctx, tenantID, namespacePath, objectType, objectID, rel, subjectType, subjectID, subjectRelation)
	return err
}

func (s *recordingStore) DeleteRelationsByObject(ctx context.Context, tenantID, objectType, objectID string) error {
	err := s.record("DeleteRelationsByObject", ctx, tenantID, objectType, objectID)
	return err
}

func (s *recordingStore) DeleteRelationsBySubject(ctx context.Context, tenantID, subjectType, subjectID string) error {
	err := s.record("DeleteRelationsBySubject", ctx, tenantID, subjectType, subjectID)
	return err
}

func (s *recordingStore) DeleteRelationsByTenant(ctx context.Context, tenantID string) error {
	err := s.record("DeleteRelationsByTenant", ctx, tenantID)
	return err
}

func (s *recordingStore) DeleteResourceType(ctx context.Context, tenantID string, rtID id.ResourceTypeID) error {
	err := s.record("DeleteResourceType", ctx, tenantID, rtID)
	return err
}

func (s *recordingStore) DeleteResourceTypesByTenant(ctx context.Context, tenantID string) error {
	err := s.record("DeleteResourceTypesByTenant", ctx, tenantID)
	return err
}

func (s *recordingStore) DeleteRole(ctx context.Context, tenantID string, roleID id.RoleID) error {
	err := s.record("DeleteRole", ctx, tenantID, roleID)
	return err
}

func (s *recordingStore) DeleteRolesByTenant(ctx context.Context, tenantID string) error {
	err := s.record("DeleteRolesByTenant", ctx, tenantID)
	return err
}

func (s *recordingStore) DetachPermission(ctx context.Context, tenantID string, roleID id.RoleID, ref permission.Ref) error {
	err := s.record("DetachPermission", ctx, tenantID, roleID, ref)
	return err
}

func (s *recordingStore) GetAssignment(ctx context.Context, tenantID string, assID id.AssignmentID) (*assignment.Assignment, error) {
	err := s.record("GetAssignment", ctx, tenantID, assID)
	return recordedValue[*assignment.Assignment](s), err
}

func (s *recordingStore) GetCheckLog(ctx context.Context, tenantID string, logID id.CheckLogID) (*checklog.Entry, error) {
	err := s.record("GetCheckLog", ctx, tenantID, logID)
	return recordedValue[*checklog.Entry](s), err
}

func (s *recordingStore) GetPermission(ctx context.Context, tenantID string, permID id.PermissionID) (*permission.Permission, error) {
	err := s.record("GetPermission", ctx, tenantID, permID)
	return recordedValue[*permission.Permission](s), err
}

func (s *recordingStore) GetPermissionByName(ctx context.Context, tenantID, namespacePath, name string) (*permission.Permission, error) {
	err := s.record("GetPermissionByName", ctx, tenantID, namespacePath, name)
	return recordedValue[*permission.Permission](s), err
}

func (s *recordingStore) GetPolicy(ctx context.Context, tenantID string, polID id.PolicyID) (*policy.Policy, error) {
	err := s.record("GetPolicy", ctx, tenantID, polID)
	return recordedValue[*policy.Policy](s), err
}

func (s *recordingStore) GetPolicyByName(ctx context.Context, tenantID, namespacePath, name string) (*policy.Policy, error) {
	err := s.record("GetPolicyByName", ctx, tenantID, namespacePath, name)
	return recordedValue[*policy.Policy](s), err
}

func (s *recordingStore) GetRelation(ctx context.Context, tenantID string, relID id.RelationID) (*relation.Tuple, error) {
	err := s.record("GetRelation", ctx, tenantID, relID)
	return recordedValue[*relation.Tuple](s), err
}

func (s *recordingStore) GetResourceType(ctx context.Context, tenantID string, rtID id.ResourceTypeID) (*resourcetype.ResourceType, error) {
	err := s.record("GetResourceType", ctx, tenantID, rtID)
	return recordedValue[*resourcetype.ResourceType](s), err
}

func (s *recordingStore) GetResourceTypeByName(ctx context.Context, tenantID, namespacePath, name string) (*resourcetype.ResourceType, error) {
	err := s.record("GetResourceTypeByName", ctx, tenantID, namespacePath, name)
	return recordedValue[*resourcetype.ResourceType](s), err
}

func (s *recordingStore) GetRole(ctx context.Context, tenantID string, roleID id.RoleID) (*role.Role, error) {
	err := s.record("GetRole", ctx, tenantID, roleID)
	return recordedValue[*role.Role](s), err
}

func (s *recordingStore) GetRoleBySlug(ctx context.Context, tenantID, namespacePath, slug string) (*role.Role, error) {
	err := s.record("GetRoleBySlug", ctx, tenantID, namespacePath, slug)
	return recordedValue[*role.Role](s), err
}

func (s *recordingStore) GetRoles(ctx context.Context, tenantID string, roleIDs []id.RoleID) ([]*role.Role, error) {
	err := s.record("GetRoles", ctx, tenantID, roleIDs)
	return recordedValue[[]*role.Role](s), err
}

func (s *recordingStore) ListActivePolicies(ctx context.Context, tenantID string, namespacePaths []string) ([]*policy.Policy, error) {
	err := s.record("ListActivePolicies", ctx, tenantID, namespacePaths)
	return recordedValue[[]*policy.Policy](s), err
}

func (s *recordingStore) ListAssignments(ctx context.Context, filter *assignment.ListFilter) ([]*assignment.Assignment, error) {
	err := s.record("ListAssignments", ctx, filter)
	return recordedValue[[]*assignment.Assignment](s), err
}

func (s *recordingStore) ListCheckLogs(ctx context.Context, filter *checklog.QueryFilter) ([]*checklog.Entry, error) {
	err := s.record("ListCheckLogs", ctx, filter)
	return recordedValue[[]*checklog.Entry](s), err
}

func (s *recordingStore) ListChildRoles(ctx context.Context, tenantID, parentSlug string) ([]*role.Role, error) {
	err := s.record("ListChildRoles", ctx, tenantID, parentSlug)
	return recordedValue[[]*role.Role](s), err
}

func (s *recordingStore) ListExpiringAssignments(ctx context.Context, tenantID string, before time.Time, limit int) ([]*assignment.Assignment, error) {
	err := s.record("ListExpiringAssignments", ctx, tenantID, before, limit)
	return recordedValue[[]*assignment.Assignment](s), err
}

func (s *recordingStore) ListPermissions(ctx context.Context, filter *permission.ListFilter) ([]*permission.Permission, error) {
	err := s.record("ListPermissions", ctx, filter)
	return recordedValue[[]*permission.Permission](s), err
}

func (s *recordingStore) ListPermissionsByRole(ctx context.Context, tenantID string, roleID id.RoleID) ([]*permission.Permission, error) {
	err := s.record("ListPermissionsByRole", ctx, tenantID, roleID)
	return recordedValue[[]*permission.Permission](s), err
}

func (s *recordingStore) ListPermissionsBySubject(ctx context.Context, tenantID, subjectKind, subjectID string) ([]*permission.Permission, error) {
	err := s.record("ListPermissionsBySubject", ctx, tenantID, subjectKind, subjectID)
	return recordedValue[[]*permission.Permission](s), err
}

func (s *recordingStore) ListPolicies(ctx context.Context, filter *policy.ListFilter) ([]*policy.Policy, error) {
	err := s.record("ListPolicies", ctx, filter)
	return recordedValue[[]*policy.Policy](s), err
}

func (s *recordingStore) ListRelationObjects(ctx context.Context, tenantID, namespacePath, subjectType, subjectID, rel string, limit int) ([]*relation.Tuple, error) {
	err := s.record("ListRelationObjects", ctx, tenantID, namespacePath, subjectType, subjectID, rel, limit)
	return recordedValue[[]*relation.Tuple](s), err
}

func (s *recordingStore) ListRelationSubjects(ctx context.Context, tenantID string, namespacePaths []string, objectType, objectID, rel string, limit int) ([]*relation.Tuple, error) {
	err := s.record("ListRelationSubjects", ctx, tenantID, namespacePaths, objectType, objectID, rel, limit)
	return recordedValue[[]*relation.Tuple](s), err
}

func (s *recordingStore) ListRelations(ctx context.Context, filter *relation.ListFilter) ([]*relation.Tuple, error) {
	err := s.record("ListRelations", ctx, filter)
	return recordedValue[[]*relation.Tuple](s), err
}

func (s *recordingStore) ListResourceTypes(ctx context.Context, filter *resourcetype.ListFilter) ([]*resourcetype.ResourceType, error) {
	err := s.record("ListResourceTypes", ctx, filter)
	return recordedValue[[]*resourcetype.ResourceType](s), err
}

func (s *recordingStore) ListRolePermissions(ctx context.Context, tenantID string, roleID id.RoleID) ([]*permission.Permission, error) {
	err := s.record("ListRolePermissions", ctx, tenantID, roleID)
	return recordedValue[[]*permission.Permission](s), err
}

func (s *recordingStore) ListRolePermissionsForRoles(ctx context.Context, tenantID string, roleIDs []id.RoleID) (map[id.RoleID][]*permission.Permission, error) {
	err := s.record("ListRolePermissionsForRoles", ctx, tenantID, roleIDs)
	return recordedValue[map[id.RoleID][]*permission.Permission](s), err
}

func (s *recordingStore) ListRoles(ctx context.Context, filter *role.ListFilter) ([]*role.Role, error) {
	err := s.record("ListRoles", ctx, filter)
	return recordedValue[[]*role.Role](s), err
}

func (s *recordingStore) ListRolesForSubject(ctx context.Context, tenantID string, namespacePaths []string, subjectKind, subjectID string) ([]id.RoleID, error) {
	err := s.record("ListRolesForSubject", ctx, tenantID, namespacePaths, subjectKind, subjectID)
	return recordedValue[[]id.RoleID](s), err
}

func (s *recordingStore) ListRolesForSubjectOnResource(ctx context.Context, tenantID string, namespacePaths []string, subjectKind, subjectID, resourceType, resourceID string) ([]id.RoleID, error) {
	err := s.record("ListRolesForSubjectOnResource", ctx, tenantID, namespacePaths, subjectKind, subjectID, resourceType, resourceID)
	return recordedValue[[]id.RoleID](s), err
}

func (s *recordingStore) ListSubjectsForRole(ctx context.Context, tenantID string, roleID id.RoleID) ([]*assignment.Assignment, error) {
	err := s.record("ListSubjectsForRole", ctx, tenantID, roleID)
	return recordedValue[[]*assignment.Assignment](s), err
}

func (s *recordingStore) Migrate(ctx context.Context) error {
	err := s.record("Migrate", ctx)
	return err
}

func (s *recordingStore) Ping(ctx context.Context) error {
	err := s.record("Ping", ctx)
	return err
}

func (s *recordingStore) PurgeCheckLogs(ctx context.Context, before time.Time) (int64, error) {
	err := s.record("PurgeCheckLogs", ctx, before)
	return recordedValue[int64](s), err
}

func (s *recordingStore) PurgeCheckLogsForTenant(ctx context.Context, tenantID string, before time.Time) (int64, error) {
	err := s.record("PurgeCheckLogsForTenant", ctx, tenantID, before)
	return recordedValue[int64](s), err
}

func (s *recordingStore) SetPolicyVersion(ctx context.Context, tenantID string, polID id.PolicyID, version int) error {
	err := s.record("SetPolicyVersion", ctx, tenantID, polID, version)
	return err
}

func (s *recordingStore) SetRolePermissions(ctx context.Context, tenantID string, roleID id.RoleID, refs []permission.Ref) error {
	err := s.record("SetRolePermissions", ctx, tenantID, roleID, refs)
	return err
}

func (s *recordingStore) UpdatePermission(ctx context.Context, p *permission.Permission) error {
	err := s.record("UpdatePermission", ctx, p)
	return err
}

func (s *recordingStore) UpdatePolicy(ctx context.Context, p *policy.Policy) error {
	err := s.record("UpdatePolicy", ctx, p)
	return err
}

func (s *recordingStore) UpdatePolicyIfVersion(ctx context.Context, p *policy.Policy, expected int) error {
	err := s.record("UpdatePolicyIfVersion", ctx, p, expected)
	return err
}

func (s *recordingStore) UpdateResourceType(ctx context.Context, rt *resourcetype.ResourceType) error {
	err := s.record("UpdateResourceType", ctx, rt)
	return err
}

func (s *recordingStore) UpdateRole(ctx context.Context, r *role.Role) error {
	err := s.record("UpdateRole", ctx, r)
	return err
}
