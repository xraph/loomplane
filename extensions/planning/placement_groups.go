package planning

import (
	"context"
	"errors"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type GroupDestinationMappingInput struct {
	EnvironmentID    string `json:"environment_id"`
	DestinationID    string `json:"destination_id"`
	TargetID         string `json:"target_id"`
	AttachmentID     string `json:"attachment_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}
type GroupVerificationResult struct {
	ID string `json:"id"`
}

func (e *Extension) placementGroupRoutes() []cloudRoute {
	return []cloudRoute{
		{http.MethodPost, "/placement-groups/renewal-authorities", "placementGroups.renewalAuthority", e.saveGroupRenewalAuthority, planning.TLSRenewalAuthorityInput{}, domain.TLSRenewalAuthority{}, http.StatusOK},
		{http.MethodPost, "/projects/:id/placement-groups/prerequisites", "placementGroups.prerequisite", e.saveGroupPrerequisite, planning.MemberPrerequisiteInput{}, domain.MemberPrerequisite{}, http.StatusOK},
		{http.MethodPost, "/projects/:id/placement-groups/timeout-verifications", "placementGroups.timeoutVerification", e.verifyGroupTimeout, planning.TimeoutVerificationInput{}, GroupVerificationResult{}, http.StatusOK},
		{http.MethodPost, "/projects/:id/placement-groups/destination-mappings", "placementGroups.destinationMapping", e.saveGroupDestinationMapping, GroupDestinationMappingInput{}, GroupVerificationResult{}, http.StatusOK},
	}
}
func placementGroupCall(c forge.Context, fn func(context.Context, security.Principal) (any, error)) error {
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		value, err := fn(ctx, p)
		switch {
		case errors.Is(err, connections.ErrCredentialRefreshNeeded):
			return nil, forge.NewHTTPError(http.StatusPreconditionFailed, connections.ErrCredentialRefreshNeeded.Error())
		case errors.Is(err, planning.ErrPrerequisiteAccessDenied):
			return nil, forge.NewHTTPError(http.StatusForbidden, planning.ErrPrerequisiteAccessDenied.Error())
		case errors.Is(err, planning.ErrPrerequisiteObservation):
			return nil, forge.NewHTTPError(http.StatusPreconditionFailed, planning.ErrPrerequisiteObservation.Error())
		case errors.Is(err, domain.ErrGroupProfile):
			return nil, forge.NewHTTPError(http.StatusUnprocessableEntity, domain.ErrGroupProfile.Error())
		}
		return value, err
	})
}
func (e *Extension) saveGroupRenewalAuthority(c forge.Context) error {
	in, err := cloudBody[planning.TLSRenewalAuthorityInput](c)
	if err != nil {
		return err
	}
	return placementGroupCall(c, func(ctx context.Context, p security.Principal) (any, error) {
		return e.memberPrerequisites.SaveRenewalAuthority(ctx, p, in)
	})
}
func (e *Extension) saveGroupPrerequisite(c forge.Context) error {
	in, err := cloudBody[planning.MemberPrerequisiteInput](c)
	if err != nil {
		return err
	}
	return placementGroupCall(c, func(ctx context.Context, p security.Principal) (any, error) {
		return e.memberPrerequisites.Save(ctx, p, c.Param("id"), in)
	})
}
func (e *Extension) verifyGroupTimeout(c forge.Context) error {
	in, err := cloudBody[planning.TimeoutVerificationInput](c)
	if err != nil {
		return err
	}
	return placementGroupCall(c, func(ctx context.Context, p security.Principal) (any, error) {
		id, verifyErr := e.memberPrerequisites.VerifyTimeout(ctx, p, c.Param("id"), in)
		return GroupVerificationResult{ID: id}, verifyErr
	})
}
func (e *Extension) saveGroupDestinationMapping(c forge.Context) error {
	in, err := cloudBody[GroupDestinationMappingInput](c)
	if err != nil {
		return err
	}
	return placementGroupCall(c, func(ctx context.Context, p security.Principal) (any, error) {
		err := e.memberPrerequisites.SaveDestinationMapping(ctx, p, domain.Scope{OrgID: p.OrgID, ProjectID: c.Param("id"), EnvironmentID: in.EnvironmentID}, in.DestinationID, in.TargetID, in.AttachmentID, in.ExpectedRevision)
		return GroupVerificationResult{ID: in.DestinationID}, err
	})
}
