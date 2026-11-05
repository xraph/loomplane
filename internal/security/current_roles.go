package security

import (
	"context"
	"fmt"
	"strings"

	"github.com/xraph/warden"
	"github.com/xraph/warden/assignment"
	"github.com/xraph/warden/role"
)

// CurrentManagedRoles derives effective managed slots from already qualified
// current membership and team facts. Historical sources metadata is diagnostic.
func CurrentManagedRoles(memberRole string, grants []ScopedRole) (map[string]string, error) {
	if memberRole == "" {
		return nil, ErrForbidden
	}
	for _, g := range grants {
		if g.TeamID == "" || (g.Role != RoleViewer && g.Role != RoleDeveloper && g.Role != RoleAdmin) {
			return nil, ErrForbidden
		}
		if g.ProjectID != "" {
			if _, ok := ProjectNamespace(g.ProjectID); !ok {
				return nil, ErrForbidden
			}
		}
	}
	reduced := reduceRoles(context.Background(), "", "", RoleForMember(memberRole), grants)
	out := make(map[string]string, len(reduced))
	for ns, r := range reduced {
		out[ns] = r.role
	}
	return out, nil
}

// VerifyCurrentManagedRoles compares every assignment against current effective
// roles. It accepts exact legacy equivalents without claiming writer provenance.
// Custom grants remain canonical Warden inputs. This function never mutates rows.
func VerifyCurrentManagedRoles(orgID, userID string, want map[string]string, catalog []*role.Role, rows []*assignment.Assignment) error {
	if orgID == "" || userID == "" || len(want) == 0 {
		return ErrForbidden
	}
	ids := make(map[string]string, 4)
	slugs := make(map[string]string, 4)
	for _, r := range catalog {
		if r == nil || r.ID.String() == "" || r.TenantID != orgID || r.NamespacePath != "" || RoleRank(r.Slug) == 0 {
			return fmt.Errorf("current managed catalog is unavailable")
		}
		id := r.ID.String()
		if ids[r.Slug] != "" || slugs[id] != "" {
			return fmt.Errorf("current managed catalog is ambiguous")
		}
		ids[r.Slug], slugs[id] = id, r.Slug
	}
	for _, slug := range []string{RoleOwner, RoleAdmin, RoleDeveloper, RoleViewer} {
		if ids[slug] == "" {
			return fmt.Errorf("current managed catalog is incomplete")
		}
	}
	for ns, slug := range want {
		if !managedNamespace(ns) || ids[slug] == "" {
			return ErrForbidden
		}
	}
	seen := make(map[string]bool, len(want))
	rowIDs := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row == nil || row.ID.String() == "" || rowIDs[row.ID.String()] || row.TenantID != orgID || row.SubjectKind != string(warden.SubjectUser) || row.SubjectID != userID {
			return ErrForbidden
		}
		rowIDs[row.ID.String()] = true
		marked, rootOnly := managedWriter(row.GrantedBy)
		canonicalRole := slugs[row.RoleID.String()] != ""
		shape := row.ResourceType == "" && row.ResourceID == "" && row.ExpiresAt == nil && managedNamespace(row.NamespacePath)
		if !marked && (!canonicalRole || !shape) {
			continue
		}
		if !canonicalRole || !shape || (rootOnly && row.NamespacePath != "") || seen[row.NamespacePath] {
			return ErrForbidden
		}
		desired, ok := want[row.NamespacePath]
		if !ok || row.RoleID.String() != ids[desired] {
			return ErrForbidden
		}
		seen[row.NamespacePath] = true
	}
	if len(seen) != len(want) {
		return ErrForbidden
	}
	return nil
}

func managedWriter(by string) (marked, rootOnly bool) {
	switch by {
	case SyncGrantedBy:
		return true, false
	case "authsome:org.create", "authsome:member.add", "authsome:member.role_change":
		return true, true
	default:
		return false, false
	}
}

func managedNamespace(ns string) bool {
	if ns == "" {
		return true
	}
	if !strings.HasPrefix(ns, "proj-") {
		return false
	}
	canonical, ok := ProjectNamespace("proj_" + strings.TrimPrefix(ns, "proj-"))
	return ok && canonical == ns
}
