package recovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

// Reconcile repairs copy and cleanup enqueue gaps independently. A copy scan
// failure must not keep an expired temporary database from reaching cleanup.
func (s *NativeService) Reconcile(ctx context.Context) error {
	copyErr, cleanupErr := s.ReconcileCopies(ctx), s.ReconcileCleanups(ctx)
	var retirementErr, policyErr error
	if s.Retirements != nil && s.EnqueueDeletion != nil {
		retirementErr = s.ReconcileRetirements(ctx)
		if _, ok := s.Store.(NativeRetentionPolicyStore); ok {
			policyErr = s.ReconcileNativeRetentionPolicies(ctx)
		}
	}
	return errors.Join(copyErr, cleanupErr, retirementErr, policyErr)
}

func copyNativeRestore(r NativeRestore) NativeRestore {
	r.Checkpoint = bytes.Clone(r.Checkpoint)
	r.Source.Config = bytes.Clone(r.Source.Config)
	// Replacement history is not a worker input. Native copies use the saved
	// original incarnation, never another binding's replacement chain.
	r.Source.Replaced = nil
	if r.Point.ExpiresAt != nil {
		expires := *r.Point.ExpiresAt
		r.Point.ExpiresAt = &expires
	}
	if r.CleanupRequestedAt != nil {
		requested := *r.CleanupRequestedAt
		r.CleanupRequestedAt = &requested
	}
	return r
}

func reconcileNative(ctx context.Context, pendingPage func(context.Context, NativeCopyPage) ([]NativeRestore, error), enqueue func(context.Context, string, string) error) error {
	var failures []error
	page := NativeCopyPage{Through: time.Now().UTC()}
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		pending, err := pendingPage(ctx, page)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		for _, r := range pending {
			failures = append(failures, enqueue(ctx, r.OrgID, r.ID))
		}
		if len(pending) < nativeCopyPageSize {
			return errors.Join(failures...)
		}
		last := pending[len(pending)-1]
		if last.CreatedAt.Before(page.AfterCreated) || last.CreatedAt.Equal(page.AfterCreated) && last.ID <= page.AfterID {
			return errors.Join(append(failures, ErrConflict)...)
		}
		page.AfterCreated, page.AfterID = last.CreatedAt, last.ID
	}
}

func nativeCopyScope(r NativeRestore) error {
	if r.OrgID == "" || r.Source.OrgID != r.OrgID || r.Source.ProjectID != r.ProjectID || r.Source.EnvironmentID != r.EnvironmentID || r.Source.Provider != r.Target.Provider || r.Source.PhysicalID == "" || r.Point.ID != r.PointID || r.Point.SourceID != r.SourceID {
		return ErrConflict
	}
	return nil
}

func (s *NativeService) requireNative(ctx context.Context, p security.Principal, projectID string) error {
	return s.requireNativePermission(ctx, p, projectID, security.RestoreManage)
}

func (s *NativeService) requireNativePermission(ctx context.Context, p security.Principal, projectID string, permission security.Permission) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.OrgID == "" || p.UserID == "" || projectID == "" {
		return security.ErrForbidden
	}
	if s.Authz == nil || s.Projects == nil {
		return ErrInvalid
	}
	if err := s.Authz.Require(ctx, p, permission, projectID); err != nil {
		return err
	}
	project, err := s.Projects.GetProject(ctx, p.OrgID, projectID)
	if err != nil {
		return err
	}
	if project == nil || project.OrgID != p.OrgID || project.ID != projectID {
		return ErrNotFound
	}
	return nil
}

type nativeLeaseStore interface {
	CheckNativeLease(context.Context, *NativeRestore, string) error
	SaveNativeCheckpoint(context.Context, *NativeRestore, json.RawMessage) error
}

func (s *NativeService) nativeControl(p security.Principal, r NativeRestore, status string, store nativeLeaseStore) NativeWorkerControl {
	check := func(ctx context.Context) error {
		if err := s.requireNative(ctx, p, r.ProjectID); err != nil {
			return err
		}
		if status == "running" && nativeTargetHasReview(r.Target) {
			if s.Admission == nil {
				return ErrInvalid
			}
			if err := s.Admission.CheckNativeTarget(ctx, &r); err != nil {
				return err
			}
		}
		return store.CheckNativeLease(ctx, &r, status)
	}
	return NativeWorkerControl{
		Check: check,
		Save: func(ctx context.Context, state json.RawMessage) error {
			if err := check(ctx); err != nil {
				return err
			}
			return store.SaveNativeCheckpoint(ctx, &r, state)
		},
	}
}
