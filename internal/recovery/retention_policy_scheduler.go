package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

// PostgreSQL timestamps round-trip at microsecond precision. Include the due
// time, not the candidate, so one persisted evaluation cannot select twice.
func retentionPolicyRequestKey(p *RetentionPolicy) string {
	raw := fmt.Sprintf("retention-v1\x00%s\x00%s\x00%s\x00%d\x00%d", p.OrgID, p.ProjectID, p.ID, p.Revision, p.NextAt.UnixMicro())
	sum := sha256.Sum256([]byte(raw))
	return "retention_" + hex.EncodeToString(sum[:])
}

type retentionPolicyRunError struct {
	policy string
	cause  error
}

func (e *retentionPolicyRunError) Error() string {
	return "retention policy " + e.policy + " could not finish; check its saved status"
}
func (e *retentionPolicyRunError) Unwrap() error { return e.cause }

func (s *Service) reconcileRetentionPolicies(ctx context.Context) error {
	policies, err := s.RetentionPolicies.DueRetentionPolicies(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, policy := range policies {
		if err = ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		operation, applyErr := s.applyRetentionPolicy(ctx, &policy)
		if operation == nil && (errors.Is(applyErr, context.Canceled) || errors.Is(applyErr, context.DeadlineExceeded)) {
			failures = append(failures, &retentionPolicyRunError{policy.ID, applyErr})
			continue
		}
		id := ""
		if operation != nil {
			id = operation.ID
		}
		finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		advanceErr := s.RetentionPolicies.AdvanceRetentionPolicy(finish, &policy, id, applyErr)
		cancel()
		if failed := errors.Join(applyErr, advanceErr); failed != nil {
			failures = append(failures, &retentionPolicyRunError{policy.ID, failed})
		}
	}
	return errors.Join(failures...)
}

func (s *Service) applyRetentionPolicy(ctx context.Context, policy *RetentionPolicy) (*Retirement, error) {
	if !policy.Enabled {
		return nil, nil
	}
	p := security.Principal{OrgID: policy.OrgID, UserID: policy.AuthorizedBy}
	if err := s.requireRetirementOwner(ctx, p, policy.ProjectID); err != nil {
		return nil, err
	}
	if policy.ID == "" || policy.Revision <= 0 || policy.NextAt.IsZero() {
		return nil, ErrInvalid
	}
	if err := policy.Policy.Validate(); err != nil {
		return nil, err
	}
	anchor, err := s.Store.Get(ctx, p.OrgID, policy.AnchorID)
	if err != nil {
		return nil, err
	}
	source, err := retirementSourceKey(anchor)
	if err != nil {
		return nil, err
	}
	if anchor.ProjectID != policy.ProjectID || source != policy.SourceKey {
		return nil, ErrConflict
	}
	request := retentionPolicyRequestKey(policy)
	existing, err := s.Retirements.RetirementByRequest(ctx, p.OrgID, policy.ProjectID, request)
	if err == nil {
		return s.resumePolicyRetirement(ctx, p, policy, existing)
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	active, err := s.Retirements.ActiveRetirement(ctx, p.OrgID, source)
	if err == nil {
		if active == nil || active.OrgID != p.OrgID || active.ProjectID != policy.ProjectID || active.SourceKey != source {
			return nil, ErrConflict
		}
		var cause error
		if active.Status == "failed" {
			cause = errors.New("an earlier backup deletion needs owner attention")
		}
		if active.RetentionPolicyID == policy.ID && active.RetentionRevision == policy.Revision {
			return active, cause
		}
		return nil, cause
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	preview, err := s.PreviewRetention(ctx, p, policy.AnchorID, policy.Policy)
	if err != nil {
		return nil, err
	}
	if !preview.Complete {
		return nil, errors.New("retention inventory is incomplete")
	}
	for n := len(preview.Points) - 1; n >= 0; n-- {
		point := preview.Points[n]
		if point.Reason == "no_verified_backup" {
			return nil, errors.New("a restore-tested recovery point is required")
		}
		if !point.Eligible {
			continue
		}
		if !point.DeletionSupported {
			return nil, errors.New("captured backup storage does not support safe deletion")
		}
		operation, retireErr := s.retire(ctx, p, point.BackupID, RetirementInput{RequestID: request, Policy: policy.Policy}, policy)
		if operation != nil {
			return operation, retireErr
		}
		// A commit can outlive its response or caller. Resolve only this exact
		// request with a bounded context; never start another selection here.
		resolve, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		known, lookupErr := s.Retirements.RetirementByRequest(resolve, p.OrgID, policy.ProjectID, request)
		cancel()
		if lookupErr == nil {
			return s.resumePolicyRetirement(ctx, p, policy, known)
		}
		if errors.Is(lookupErr, ErrNotFound) {
			if errors.Is(retireErr, ErrConflict) {
				current, currentErr := s.RetentionPolicies.GetRetentionPolicy(ctx, p.OrgID, policy.ID)
				if currentErr != nil {
					return nil, errors.Join(retireErr, currentErr)
				}
				if current != nil && (!current.Enabled || current.Revision != policy.Revision || !current.NextAt.Equal(policy.NextAt)) {
					return nil, nil
				}
			}
			return nil, retireErr
		}
		return nil, errors.Join(retireErr, lookupErr)
	}
	return nil, nil
}

func (s *Service) resumePolicyRetirement(ctx context.Context, p security.Principal, policy *RetentionPolicy, r *Retirement) (*Retirement, error) {
	if r == nil || r.OrgID != p.OrgID || r.ProjectID != policy.ProjectID || r.SourceKey != policy.SourceKey || r.RetentionPolicyID != policy.ID || r.RetentionRevision != policy.Revision || r.CreatedBy != policy.AuthorizedBy || r.Policy != policy.Policy {
		return nil, ErrConflict
	}
	switch r.Status {
	case "queued":
		return s.publishRetirement(ctx, p, r, "request")
	case "running", "succeeded":
		return r, nil
	case "failed":
		return r, errors.New("an accepted backup deletion needs owner attention")
	default:
		return nil, ErrConflict
	}
}
