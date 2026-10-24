package recovery

import (
	"context"
	"errors"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

func (s *NativeService) nativeRetirementGuard(p security.Principal, sourceID string) RetirementGuard {
	return func(ctx context.Context) error {
		if p.OrgID == "" || p.UserID == "" || s.Store == nil || s.Authz == nil || s.Projects == nil {
			return security.ErrForbidden
		}
		source, err := s.authorizedSource(ctx, p, sourceID, security.BackupRetire)
		if err != nil {
			return err
		}
		return s.Authz.Require(ctx, p, security.BackupRead, source.ProjectID)
	}
}

func (s *NativeService) refreshNativeRetention(ctx context.Context, p security.Principal, sourceID string, guard RetirementGuard) error {
	if s.Store == nil || s.Backend == nil {
		return ErrInvalid
	}
	if authorityErr := guard(ctx); authorityErr != nil {
		return authorityErr
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	return s.Store.RefreshNativeInventory(ctx, p.OrgID, sourceID, func(ctx context.Context, source *NativeSource) ([]NativePoint, bool, error) {
		if authorityErr := guard(ctx); authorityErr != nil {
			return nil, false, authorityErr
		}
		points, complete, err := s.Backend.NativeSnapshots(ctx, &source.Binding)
		if err != nil {
			return nil, false, err
		}
		if authorityErr := guard(ctx); authorityErr != nil {
			return nil, false, authorityErr
		}
		return points, complete, nil
	})
}

func (s *NativeService) PreviewNativeRetention(ctx context.Context, p security.Principal, sourceID string, input RetentionInput) (*RetentionPreview, error) {
	if s.Retirements == nil {
		return nil, ErrInvalid
	}
	if checkErr := input.Validate(); checkErr != nil {
		return nil, checkErr
	}
	guard := func(ctx context.Context) error {
		_, err := s.authorizedSource(ctx, p, sourceID, security.BackupRead)
		return err
	}
	if refreshErr := s.refreshNativeRetention(ctx, p, sourceID, guard); refreshErr != nil {
		return nil, refreshErr
	}
	source, entries, err := s.Retirements.NativeRetentionSnapshot(ctx, p.OrgID, sourceID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return previewNativeRetention(now, source.Provider, input, entries, nativeRetentionComplete(source, now))
}

func (s *NativeService) CreatePointRetirement(ctx context.Context, p security.Principal, sourceID, pointID string, in NativeRetirementInput) (*NativeRetirement, error) {
	if s.Retirements == nil || s.EnqueueDeletion == nil || s.DeletionBackend == nil || !in.AcknowledgeDeletion || !requestKey.MatchString(in.RequestID) || pointID == "" {
		return nil, ErrInvalid
	}
	if checkErr := in.Policy.Validate(); checkErr != nil {
		return nil, checkErr
	}
	guard := s.nativeRetirementGuard(p, sourceID)
	if authorityErr := guard(ctx); authorityErr != nil {
		return nil, authorityErr
	}
	existing, err := s.Retirements.NativeRetirementByRequest(ctx, p.OrgID, sourceID, in.RequestID)
	if err == nil {
		if existing.PointID != pointID || existing.CreatedBy != p.UserID || existing.Policy != in.Policy {
			return nil, ErrConflict
		}
		if existing.Status == "queued" {
			return existing, s.EnqueueDeletion(ctx, p.OrgID, existing.ID)
		}
		return existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if refreshErr := s.refreshNativeRetention(ctx, p, sourceID, guard); refreshErr != nil {
		return nil, refreshErr
	}
	source, err := s.authorizedSource(ctx, p, sourceID, security.BackupRetire)
	if err != nil {
		return nil, err
	}
	key, err := typeid.Generate("nativeretirement")
	if err != nil {
		return nil, err
	}
	record, err := s.Retirements.AdmitNativeRetirement(ctx, &NativeRetirement{ID: key.String(), OrgID: p.OrgID, ProjectID: source.ProjectID, SourceID: sourceID, PointID: pointID, RequestID: in.RequestID, CreatedBy: p.UserID, Policy: in.Policy}, guard)
	if err != nil {
		return nil, err
	}
	if s.Audit != nil {
		s.Audit.Record(ctx, projects.AuditEvent{Action: "service.native.backup.retirement", Severity: "warning", ProjectID: source.ProjectID, Principal: p, Attributes: map[string]any{"retirement_id": record.ID, "point_id": record.PointID, "witness_id": record.WitnessID}})
	}
	return record, s.EnqueueDeletion(ctx, p.OrgID, record.ID)
}

func (s *NativeService) NativeRetirement(ctx context.Context, p security.Principal, id string) (*NativeRetirement, error) {
	if s.Retirements == nil {
		return nil, ErrInvalid
	}
	r, err := s.Retirements.GetNativeRetirement(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.authorizedSource(ctx, p, r.SourceID, security.BackupRead); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *NativeService) RetryPointRetirement(ctx context.Context, p security.Principal, id string) (*NativeRetirement, error) {
	r, err := s.NativeRetirement(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if s.EnqueueDeletion == nil {
		return nil, ErrInvalid
	}
	guard := s.nativeRetirementGuard(p, r.SourceID)
	next, err := s.Retirements.RetryNativeRetirement(ctx, p.OrgID, id, p.UserID, guard)
	if err != nil {
		return nil, err
	}
	return next, s.EnqueueDeletion(ctx, p.OrgID, id)
}

func (s *NativeService) RunNativeRetirement(ctx context.Context, org, id string) error {
	if s.Retirements == nil || s.DeletionBackend == nil || s.Backend == nil {
		return ErrInvalid
	}
	key, err := typeid.Generate("nativeattempt")
	if err != nil {
		return err
	}
	r, err := s.Retirements.ClaimNativeRetirement(ctx, org, id, key.String())
	if errors.Is(err, ErrConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	if r == nil || r.OrgID != org || r.ID != id || r.Token != key.String() {
		return ErrConflict
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	p := security.Principal{OrgID: org, UserID: r.CreatedBy}
	authority := s.nativeRetirementGuard(p, r.SourceID)
	check := func(ctx context.Context) error {
		if authorityErr := authority(ctx); authorityErr != nil {
			return authorityErr
		}
		if refreshErr := s.refreshNativeRetention(ctx, p, r.SourceID, authority); refreshErr != nil {
			return errors.Join(ErrNativePending, refreshErr)
		}
		if leaseErr := s.Retirements.CheckNativeRetirement(ctx, r); leaseErr != nil {
			return leaseErr
		}
		observed, readErr := s.Backend.CheckNativeSnapshot(ctx, &r.Source, r.Witness)
		if readErr != nil {
			return errors.Join(ErrNativePending, ErrNativeUnavailable)
		}
		return checkNativePoint(r.Source.Provider, r.Witness, observed, time.Now().UTC())
	}
	control := NativeDeletionControl{Check: check, Before: func() error {
		if checkErr := check(ctx); checkErr != nil {
			return checkErr
		}
		checkpointErr := s.Retirements.SaveNativeRetirementIntent(ctx, r, true)
		if checkpointErr != nil && !errors.Is(checkpointErr, ErrConflict) && !errors.Is(checkpointErr, ErrInvalid) {
			return errors.Join(ErrNativePending, checkpointErr)
		}
		return checkpointErr
	}, Rejected: func() error {
		if authorityErr := authority(ctx); authorityErr != nil {
			return authorityErr
		}
		return s.Retirements.SaveNativeRetirementIntent(ctx, r, false)
	}}
	err = check(ctx)
	if err == nil {
		var absent bool
		absent, err = s.DeletionBackend.DeleteNativePoint(ctx, p, *r, control)
		if err == nil && !absent {
			err = ErrInvalid
		}
	}
	if err == nil {
		err = check(ctx)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		err = errors.Join(ErrNativePending, err)
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer stop()
	return errors.Join(err, s.Retirements.FinishNativeRetirement(finish, r, err))
}

func (s *NativeService) ReconcileRetirements(ctx context.Context) error {
	if s.Retirements == nil || s.EnqueueDeletion == nil {
		return ErrInvalid
	}
	page := NativeCopyPage{Through: time.Now().UTC()}
	var failures []error
	for {
		work, err := s.Retirements.PendingNativeRetirements(ctx, page)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		if len(work) == 0 {
			return errors.Join(failures...)
		}
		for _, r := range work {
			if enqueueErr := s.EnqueueDeletion(ctx, r.OrgID, r.ID); enqueueErr != nil {
				failures = append(failures, enqueueErr)
			}
		}
		last := work[len(work)-1]
		if !last.CreatedAt.After(page.AfterCreated) && (last.CreatedAt.Before(page.AfterCreated) || last.ID <= page.AfterID) {
			return errors.Join(append(failures, ErrConflict)...)
		}
		page.AfterCreated, page.AfterID = last.CreatedAt, last.ID
	}
}
