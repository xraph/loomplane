package planning

import (
	"github.com/xraph/loomplane/internal/approvaladmission"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/security"
)

func groupApprovalRequest(plan *Plan, actor security.Principal, requestID, note string) (approvaladmission.Request, error) {
	if plan.ExpiresAt == nil || plan.ExpiresAt.IsZero() || plan.OrgID != actor.OrgID {
		return approvaladmission.Request{}, approvaladmission.ErrInvalid
	}
	scope := approvaladmission.Scope{OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, PlanID: plan.ID, Revision: plan.Revision}
	pins := make([]approvaladmission.GroupPin, 0, len(plan.PlacementGroups))
	for _, g := range plan.PlacementGroups {
		if g.OrgID != plan.OrgID || g.ProjectID != plan.ProjectID || g.EnvironmentID != plan.EnvironmentID {
			return approvaladmission.Request{}, approvaladmission.ErrInvalid
		}
		pins = append(pins, approvaladmission.GroupPin{ID: g.GroupID, Generation: g.Generation, Revision: g.Revision, ConfigRevision: g.ConfigRevision})
	}
	digest, err := approvaladmission.GroupSetDigest(scope, pins)
	if err != nil {
		return approvaladmission.Request{}, err
	}
	return approvaladmission.Request{Scope: scope, RequestID: requestID, GroupSetDigest: digest, Note: note, Actor: actor, AcceptBefore: plan.ExpiresAt.UTC()}, nil
}

func (s *Service) verifyGroupApproval(plan *Plan, d orchestration.Decision) error {
	if s.approvalVerifier == nil || d.Actor == nil || d.Actor.UserID != d.By || d.RequestID == "" || d.GroupAdmission == "" {
		return ErrUnavailable
	}
	r, err := groupApprovalRequest(plan, *d.Actor, d.RequestID, d.Note)
	if err != nil {
		return ErrUnavailable
	}
	if _, err = s.approvalVerifier.Verify(approvaladmission.Envelope(d.GroupAdmission), approvaladmission.Expected{AppID: s.approvalAppID, Request: r}); err != nil {
		return ErrUnavailable
	}
	return nil
}
