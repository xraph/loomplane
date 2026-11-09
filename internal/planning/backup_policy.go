package planning

import (
	"context"
	"slices"

	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/security"
)

// RequireBackupPolicyPermission checks the actor who would own a reviewed
// schedule. Plans without defaults do not acquire another permission gate.
func RequireBackupPolicyPermission(ctx context.Context, auth Authorizer, p security.Principal, project string, actions []Action) error {
	if !hasBackupPolicies(actions) {
		return nil
	}
	if auth == nil || p.OrgID == "" || p.UserID == "" || p.ReadOnly {
		return security.ErrForbidden
	}
	return auth.Require(ctx, p, security.BackupManage, project)
}

func hasBackupPolicies(actions []Action) bool {
	return slices.ContainsFunc(actions, func(a Action) bool { return a.BackupEveryHours != 0 })
}

func (s *Service) checkBackupDecision(ctx context.Context, plan *Plan, d orchestration.Decision) error {
	if !hasBackupPolicies(plan.Actions) {
		return nil
	}
	p := security.ActorPrincipal(plan.OrgID, d.By)
	if d.Actor != nil {
		p = *d.Actor
	}
	if p.OrgID != plan.OrgID || p.UserID != d.By {
		return security.ErrForbidden
	}
	return RequireBackupPolicyPermission(ctx, s.authz, p, plan.ProjectID, plan.Actions)
}

func (s *Service) checkBackupPreview(ctx context.Context, plan *Plan, result *Result) {
	p := security.ActorPrincipal(plan.OrgID, plan.CreatedBy)
	if err := RequireBackupPolicyPermission(ctx, s.authz, p, plan.ProjectID, result.Actions); err != nil {
		result.Problems = append(result.Problems, Finding{Code: "backup-policy-permission", Message: "Backup management permission is required to create the default backup schedule."})
		result.Executable = false
	}
}
