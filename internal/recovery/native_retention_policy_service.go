package recovery

import (
	"context"
	"errors"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

type NativeRetentionPolicyState struct {
	Configured bool                   `json:"configured"`
	Policy     *NativeRetentionPolicy `json:"policy,omitempty"`
}

func (s *NativeService) NativeRetentionPolicy(ctx context.Context, p security.Principal, source string) (*NativeRetentionPolicy, error) {
	store, ok := s.Store.(NativeRetentionPolicyStore)
	if !ok || s.Authz == nil || s.Projects == nil {
		return nil, ErrInvalid
	}
	if _, err := s.authorizedSource(ctx, p, source, security.BackupRead); err != nil {
		return nil, err
	}
	policy, err := store.NativeRetentionPolicyForSource(ctx, p.OrgID, source)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return policy, err
}
func (s *NativeService) SetNativeRetentionPolicy(ctx context.Context, p security.Principal, source string, in RetentionPolicyInput) (*NativeRetentionPolicy, error) {
	store, ok := s.Store.(NativeRetentionPolicyStore)
	if !ok || in.Revision < 0 || in.Revision >= 2147483647 {
		return nil, ErrInvalid
	}
	guard := s.nativeRetirementGuard(p, source)
	if err := guard(ctx); err != nil {
		return nil, err
	}
	if err := in.Policy.Validate(); err != nil {
		return nil, err
	}
	if in.Enabled {
		preview, err := s.PreviewNativeRetention(ctx, p, source, in.Policy)
		if err != nil {
			return nil, err
		}
		if !preview.Complete || preview.VerifiedBackupID == "" {
			return nil, ErrConflict
		}
	}
	retained, err := s.Store.GetNativeSource(ctx, p.OrgID, source)
	if err != nil {
		return nil, err
	}
	key, err := typeid.Generate("nativeretentionpolicy")
	if err != nil {
		return nil, err
	}
	policy, err := store.PutNativeRetentionPolicy(ctx, &NativeRetentionPolicy{ID: key.String(), OrgID: p.OrgID, ProjectID: retained.ProjectID, SourceID: source, Policy: in.Policy, Enabled: in.Enabled, AuthorizedBy: p.UserID}, in.Revision, guard)
	if err == nil && s.Audit != nil {
		s.Audit.Record(ctx, projects.AuditEvent{Action: "service.native.retention.policy", Severity: "info", ProjectID: retained.ProjectID, Principal: p, Attributes: map[string]any{"policy_id": policy.ID, "source_id": source, "revision": policy.Revision, "enabled": policy.Enabled}})
	}
	return policy, err
}
func (s *NativeService) ReconcileNativeRetentionPolicies(ctx context.Context) error {
	store, ok := s.Store.(NativeRetentionPolicyStore)
	if !ok || s.Retirements == nil || s.EnqueueDeletion == nil {
		return ErrInvalid
	}
	policies, err := store.DueNativeRetentionPolicies(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, policy := range policies {
		if err = ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		operation, applyErr := s.applyNativeRetentionPolicy(ctx, &policy)
		if operation == nil && (errors.Is(applyErr, context.Canceled) || errors.Is(applyErr, context.DeadlineExceeded)) {
			failures = append(failures, &retentionPolicyRunError{policy.ID, applyErr})
			continue
		}
		id := ""
		if operation != nil {
			id = operation.ID
		}
		finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		advanceErr := store.AdvanceNativeRetentionPolicy(finish, &policy, id, applyErr)
		cancel()
		if failed := errors.Join(applyErr, advanceErr); failed != nil {
			failures = append(failures, &retentionPolicyRunError{policy.ID, failed})
		}
	}
	return errors.Join(failures...)
}
func (s *NativeService) applyNativeRetentionPolicy(ctx context.Context, policy *NativeRetentionPolicy) (*NativeRetirement, error) {
	if policy == nil || policy.ID == "" || policy.SourceID == "" || policy.Revision < 1 || policy.NextAt.IsZero() {
		return nil, ErrInvalid
	}
	if !policy.Enabled {
		return nil, nil
	}
	if err := policy.Policy.Validate(); err != nil {
		return nil, err
	}
	p := security.Principal{OrgID: policy.OrgID, UserID: policy.AuthorizedBy}
	guard := s.nativeRetirementGuard(p, policy.SourceID)
	if err := guard(ctx); err != nil {
		return nil, err
	}
	retained, err := s.Store.GetNativeSource(ctx, p.OrgID, policy.SourceID)
	if err != nil {
		return nil, err
	}
	if retained.ProjectID != policy.ProjectID {
		return nil, ErrConflict
	}
	request := nativeRetentionPolicyRequestKey(policy)
	existing, err := s.Retirements.NativeRetirementByRequest(ctx, p.OrgID, policy.SourceID, request)
	if err == nil {
		return s.resumeNativePolicyRetirement(ctx, policy, existing)
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	active, ok := s.Store.(interface {
		ActiveNativeRetirement(context.Context, string, string) (*NativeRetirement, error)
	})
	if !ok {
		return nil, ErrInvalid
	}
	earlier, err := active.ActiveNativeRetirement(ctx, p.OrgID, policy.SourceID)
	if err == nil {
		if earlier.Status == "failed" {
			return nil, errors.New("an earlier native deletion needs owner attention")
		}
		return nil, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	preview, err := s.PreviewNativeRetention(ctx, p, policy.SourceID, policy.Policy)
	if err != nil {
		return nil, err
	}
	if !preview.Complete || preview.VerifiedBackupID == "" {
		return nil, ErrNativeUnavailable
	}
	for i := len(preview.Points) - 1; i >= 0; i-- {
		point := preview.Points[i]
		if !point.Eligible || !point.DeletionSupported {
			continue
		}
		key, keyErr := typeid.Generate("nativeretirement")
		if keyErr != nil {
			return nil, keyErr
		}
		operation, admitErr := s.Retirements.AdmitNativeRetirement(ctx, &NativeRetirement{ID: key.String(), OrgID: p.OrgID, ProjectID: policy.ProjectID, SourceID: policy.SourceID, PointID: point.BackupID, RequestID: request, CreatedBy: p.UserID, Policy: policy.Policy, PolicyID: policy.ID, PolicyRevision: policy.Revision, ExpectedPolicyAt: policy.NextAt}, guard)
		if operation != nil {
			if s.Audit != nil {
				s.Audit.Record(ctx, projects.AuditEvent{Action: "service.native.backup.retirement", Severity: "warning", ProjectID: policy.ProjectID, Principal: p, Attributes: map[string]any{"retirement_id": operation.ID, "point_id": operation.PointID, "witness_id": operation.WitnessID, "policy_id": policy.ID, "policy_revision": policy.Revision}})
			}
			return s.resumeNativePolicyRetirement(ctx, policy, operation)
		}
		resolve, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		known, lookupErr := s.Retirements.NativeRetirementByRequest(resolve, p.OrgID, policy.SourceID, request)
		cancel()
		if lookupErr == nil {
			return s.resumeNativePolicyRetirement(ctx, policy, known)
		}
		if !errors.Is(lookupErr, ErrNotFound) {
			return nil, errors.Join(admitErr, lookupErr)
		}
		if errors.Is(admitErr, ErrConflict) {
			current, readErr := s.NativeRetentionPolicy(ctx, p, policy.SourceID)
			if readErr != nil {
				return nil, errors.Join(admitErr, readErr)
			}
			if current != nil && (!current.Enabled || current.Revision != policy.Revision || !current.NextAt.Equal(policy.NextAt)) {
				return nil, nil
			}
		}
		return nil, admitErr
	}
	return nil, nil
}
func (s *NativeService) resumeNativePolicyRetirement(ctx context.Context, p *NativeRetentionPolicy, r *NativeRetirement) (*NativeRetirement, error) {
	if r == nil || r.OrgID != p.OrgID || r.ProjectID != p.ProjectID || r.SourceID != p.SourceID || r.PolicyID != p.ID || r.PolicyRevision != p.Revision || r.CreatedBy != p.AuthorizedBy || r.Policy != p.Policy || r.RequestID != nativeRetentionPolicyRequestKey(p) {
		return nil, ErrConflict
	}
	switch r.Status {
	case "queued":
		return r, s.EnqueueDeletion(ctx, r.OrgID, r.ID)
	case "running", "succeeded":
		return r, nil
	case "failed":
		return r, errors.New("an accepted native deletion needs owner attention")
	default:
		return nil, ErrConflict
	}
}
func (s *PgStore) ActiveNativeRetirement(ctx context.Context, org, source string) (*NativeRetirement, error) {
	return scanNativeRetirement(s.db.QueryRow(ctx, `SELECT `+nativeRetirementColumns+` FROM loomplane_native_retirements WHERE org_id=$1 AND source_id=$2 AND status<>'succeeded'`, org, source))
}
