package projects

import (
	"context"
	"fmt"

	"github.com/xraph/loomplane/internal/security"
)

// MaxStagingSoakSeconds is the longest minimum staging soak a project can
// ask for: a week.
const MaxStagingSoakSeconds = 7 * 24 * 60 * 60

// AutonomyInput changes how much the AI agent may do on its own in a
// project. A nil field is left alone.
type AutonomyInput struct {
	AutonomousPromotion *bool `json:"autonomous_promotion,omitempty"`
	// MinStagingSoakSeconds is 0 to 604800, in whole minutes.
	MinStagingSoakSeconds *int64 `json:"min_staging_soak_seconds,omitempty"`
}

// SetAutonomy changes whether the AI agent may promote a release it was
// asked for to production without asking first, and how long the release
// must have soaked in staging before it may. It needs deployment:approve
// on the project, since it hands the agent part of that permission.
//
// Only a person can change it. The agent is refused before any role check,
// even when it acts for an admin, so the model can never turn its own
// autonomy on; Loomplane's automation is refused too.
func (s *Service) SetAutonomy(ctx context.Context, p security.Principal, projectID string, in AutonomyInput) (*Project, error) {
	if p.IsServiceAccount() {
		return nil, fmt.Errorf("%w: only a person can change what the agent may do on its own", security.ErrForbidden)
	}
	if err := s.authz.Require(ctx, p, security.DeploymentApprove, projectID); err != nil {
		return nil, err
	}
	if in.AutonomousPromotion == nil && in.MinStagingSoakSeconds == nil {
		return nil, fmt.Errorf("%w: send autonomous_promotion, min_staging_soak_seconds or both", ErrInvalid)
	}
	if soak := in.MinStagingSoakSeconds; soak != nil && (*soak < 0 || *soak > MaxStagingSoakSeconds || *soak%60 != 0) {
		return nil, fmt.Errorf("%w: the minimum staging soak is whole minutes, from none up to a week", ErrInvalid)
	}
	proj, err := s.store.SetAutonomy(ctx, p.OrgID, projectID, in, s.now().UTC())
	if err != nil {
		return nil, err
	}
	// Letting the agent promote alone is worth noticing in the trail.
	severity := "info"
	if proj.AutonomousPromotion {
		severity = "warning"
	}
	s.record(ctx, AuditEvent{Action: "project.autonomy.update", Severity: severity, ProjectID: proj.ID, Principal: p,
		Attributes: map[string]any{
			"autonomous_promotion":     proj.AutonomousPromotion,
			"min_staging_soak_seconds": proj.MinStagingSoakSeconds,
		}})
	return proj, nil
}
