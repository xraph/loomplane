package recovery

import (
	"context"
	"errors"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// RetentionPolicyInput is an explicit owner decision, separate from backup cadence.
// Revision zero creates a policy; updates must use its observed revision.
type RetentionPolicyInput struct {
	Revision int64          `json:"revision"`
	Policy   RetentionInput `json:"policy"`
	Enabled  bool           `json:"enabled"`
}

func (s *Service) SetRetentionPolicy(ctx context.Context, p security.Principal, anchorID string, in RetentionPolicyInput) (*RetentionPolicy, error) {
	anchor, err := s.Store.Get(ctx, p.OrgID, anchorID)
	if err != nil {
		return nil, err
	}
	if p.OrgID == "" || anchor.OrgID != p.OrgID {
		return nil, security.ErrForbidden
	}
	guard := func(ctx context.Context) error { return s.requireRetirementOwner(ctx, p, anchor.ProjectID) }
	if in.Enabled {
		guard = s.retirementGuard(p, anchor.ProjectID, anchor.Object)
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	if in.Revision < 0 || in.Revision >= 2147483647 || s.RetentionPolicies == nil {
		return nil, ErrInvalid
	}
	if checkErr := in.Policy.Validate(); checkErr != nil {
		return nil, checkErr
	}
	source, err := retirementSourceKey(anchor)
	if err != nil {
		return nil, err
	}
	key, err := typeid.Generate("retentionpolicy")
	if err != nil {
		return nil, err
	}
	policy, err := s.RetentionPolicies.PutRetentionPolicy(ctx, &RetentionPolicy{ID: key.String(), OrgID: p.OrgID, ProjectID: anchor.ProjectID, AnchorID: anchorID, SourceKey: source, Policy: in.Policy, Enabled: in.Enabled, AuthorizedBy: p.UserID}, in.Revision, guard)
	if err == nil && s.Audit != nil {
		s.Audit.Record(ctx, projects.AuditEvent{Action: "service.backup.retention.policy", Severity: "info", ProjectID: anchor.ProjectID, Principal: p, Attributes: map[string]any{"policy_id": policy.ID, "revision": policy.Revision, "enabled": policy.Enabled, "older_than_days": policy.Policy.OlderThanDays, "keep_last": policy.Policy.KeepLast}})
	}
	return policy, err
}

func (s *Service) RetentionPolicyForBackup(ctx context.Context, p security.Principal, anchorID string) (*RetentionPolicy, error) {
	anchor, err := s.Store.Get(ctx, p.OrgID, anchorID)
	if err != nil {
		return nil, err
	}
	if p.OrgID == "" || anchor.OrgID != p.OrgID {
		return nil, security.ErrForbidden
	}
	if checkErr := s.Authz.Require(ctx, p, security.BackupRead, anchor.ProjectID); checkErr != nil {
		return nil, checkErr
	}
	if s.RetentionPolicies == nil {
		return nil, ErrInvalid
	}
	source, err := retirementSourceKey(anchor)
	if err != nil {
		return nil, err
	}
	policy, err := s.RetentionPolicies.FindRetentionPolicy(ctx, p.OrgID, anchorID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if policy == nil || policy.OrgID != p.OrgID || policy.ProjectID != anchor.ProjectID || policy.SourceKey != source {
		return nil, ErrConflict
	}
	return policy, nil
}
