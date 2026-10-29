package deployments

import (
	"context"
	"errors"
	"fmt"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

// BackupPolicyInstaller persists a reviewed default once the source is bound.
type BackupPolicyInstaller interface {
	EnsurePolicy(context.Context, security.Principal, string, recovery.PolicyInput) (*recovery.Policy, error)
}

func (r *Runner) checkBackupPolicies(ctx context.Context, plan *planning.Plan, actions []planning.Action) error {
	for _, a := range actions {
		if a.BackupEveryHours == 0 {
			continue
		}
		if a.BackupEveryHours < 1 || a.BackupEveryHours > 168 || a.Op != planning.Create || a.Subject != planning.ResourceSubject || a.Resource == nil || a.Name == "" || a.Resource.Provider != a.Provider || recovery.Protection(a.Resource) != recipes.ProtectionNative {
			return fmt.Errorf("invalid default backup schedule for %s", a.Name)
		}
		if r.backupPolicies == nil {
			return errors.New("default backup policy installer is unavailable")
		}
	}
	return planning.RequireBackupPolicyPermission(ctx, r.authz, security.ActorPrincipal(plan.OrgID, plan.DecidedBy), plan.ProjectID, actions)
}

func (r *Runner) installBackupPolicy(ctx context.Context, d *Deployment, a planning.Action) error {
	if a.BackupEveryHours == 0 {
		return nil
	}
	_, err := r.backupPolicies.EnsurePolicy(ctx, security.ActorPrincipal(d.OrgID, d.StartedBy), d.ProjectID, recovery.PolicyInput{Environment: d.Environment, Resource: a.Name, EveryHours: a.BackupEveryHours, Enabled: true})
	if err != nil {
		return fmt.Errorf("install default backup schedule for %s: %w", a.Name, err)
	}
	return nil
}
