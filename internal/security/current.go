package security

import (
	"context"
	"fmt"
	"strings"

	"github.com/xraph/warden"
)

func checkRequest(p Principal, perm Permission, resourceID string) *warden.CheckRequest {
	ns, _ := ProjectNamespace(resourceID)
	return &warden.CheckRequest{
		Subject: subjectFor(p), Action: warden.Action{Name: perm.Action},
		Resource: warden.Resource{Type: perm.Resource, ID: resourceID},
		TenantID: p.OrgID, NamespacePath: ns,
	}
}

// CheckUnrepaired checks the exact resource through the canonical engine without
// catalog writes, repair or identity callbacks. The caller must supply current
// membership evidence and qualify the engine/store context before using the result.
func (a *Authorizer) CheckUnrepaired(ctx context.Context, p Principal, perm Permission, resourceID, memberRole string) (*warden.CheckResult, error) {
	if p.Kind != KindUser || p.OnBehalfOf != nil || p.Fleet != nil || p.ReadOnly ||
		p.UserID == "" || p.OrgID == "" || strings.Contains(p.UserID, ":") || IsFleetSubject(p.UserID) || memberRole == "" || perm.Resource == "" || perm.Action == "" {
		return nil, ErrForbidden
	}
	if perm == BackupRetire && memberRole != MemberOwner {
		return nil, ErrForbidden
	}
	if a == nil || a.engine == nil {
		return nil, fmt.Errorf("current permission engine is unavailable")
	}
	res, err := a.engine.Check(ctx, checkRequest(p, perm, resourceID))
	if err != nil {
		return nil, fmt.Errorf("authorize %s: %w", perm.Name(), err)
	}
	if res == nil {
		return nil, fmt.Errorf("current permission engine returned no result")
	}
	if !res.Allowed {
		return res, fmt.Errorf("%w (%s)", ErrForbidden, perm.Name())
	}
	return res, nil
}
