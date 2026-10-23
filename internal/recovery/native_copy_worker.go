package recovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/security"
)

// RunCopy resumes the retained native operation under a fresh lease. A backend
// must use the controls before writes and polls, and Selected before its first
// copy request. A captured operation may outlive the original backup.
func (s *NativeService) RunCopy(ctx context.Context, org, id string) error {
	if s.Copies == nil || s.CopyBackend == nil || s.Backend == nil || s.Authz == nil || s.Projects == nil {
		return ErrInvalid
	}
	token, err := typeid.Generate("nativeattempt")
	if err != nil {
		return err
	}
	claimed, err := s.Copies.ClaimNativeRestore(ctx, org, id, token.String())
	if errors.Is(err, ErrConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	if claimed == nil || claimed.OrgID != org || claimed.ID != id || claimed.Token != token.String() {
		return ErrConflict
	}
	// Keep guard and completion inputs separate from the backend's mutable bytes.
	frozen := copyNativeRestore(*claimed)
	ctx, cancel := context.WithTimeout(ctx, 40*time.Minute)
	defer cancel()
	started := time.Now()
	p := security.Principal{OrgID: org, UserID: frozen.CreatedBy}
	control := s.copyControl(p, frozen)
	err = control.Check(ctx)
	if err == nil && (frozen.Source.OrgID != org || frozen.Source.ProjectID != frozen.ProjectID || frozen.Source.EnvironmentID != frozen.EnvironmentID || frozen.Source.Provider != frozen.Target.Provider || frozen.Source.PhysicalID == "" || frozen.Point.ID != frozen.PointID || frozen.Point.SourceID != frozen.SourceID) {
		err = ErrConflict
	}
	if err == nil && bytes.Equal(bytes.TrimSpace(frozen.Checkpoint), []byte("{}")) {
		_, err = control.Selected(ctx)
	}
	var result NativeRestoreResult
	if err == nil {
		result, err = s.CopyBackend.RestoreNative(ctx, p, copyNativeRestore(frozen), control)
	}
	if err == nil && (!result.SQLVerified || result.ExternalID == "" || result.PhysicalID == "") {
		err = ErrInvalid
	}
	if err == nil {
		err = control.Check(ctx)
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer stop()
	return errors.Join(err, s.Copies.FinishNativeRestore(finish, &frozen, result, time.Since(started), err))
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
	return r
}

func (s *NativeService) copyControl(p security.Principal, r NativeRestore) NativeCopyControl {
	check := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p.OrgID == "" || p.UserID == "" || r.ProjectID == "" {
			return security.ErrForbidden
		}
		if err := s.Authz.Require(ctx, p, security.RestoreManage, r.ProjectID); err != nil {
			return err
		}
		project, err := s.Projects.GetProject(ctx, p.OrgID, r.ProjectID)
		if err != nil {
			return err
		}
		if project == nil || project.OrgID != p.OrgID || project.ID != r.ProjectID {
			return ErrNotFound
		}
		return s.Copies.CheckNativeLease(ctx, &r, "running")
	}
	return NativeCopyControl{
		Check: check,
		Save: func(ctx context.Context, state json.RawMessage) error {
			if err := check(ctx); err != nil {
				return err
			}
			return s.Copies.SaveNativeCheckpoint(ctx, &r, state)
		},
		Selected: func(ctx context.Context) (*NativePoint, error) {
			if err := check(ctx); err != nil {
				return nil, err
			}
			selectionCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			input := copyNativeRestore(r)
			current, err := s.Backend.CheckNativeSnapshot(selectionCtx, &input.Source, input.Point)
			if err != nil {
				return nil, ErrNativeUnavailable
			}
			if err := checkNativePoint(r.Target.Provider, r.Point, current, time.Now().UTC()); err != nil {
				return nil, err
			}
			if err := check(ctx); err != nil {
				return nil, err
			}
			return current, nil
		},
	}
}

// ReconcileCopies repairs the commit/enqueue gap and abandoned worker claims.
func (s *NativeService) ReconcileCopies(ctx context.Context) error {
	if s.Copies == nil || s.EnqueueCopy == nil {
		return ErrInvalid
	}
	var failures []error
	page := NativeCopyPage{Through: time.Now().UTC()}
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		pending, err := s.Copies.PendingNativeRestores(ctx, page)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		for _, r := range pending {
			failures = append(failures, s.EnqueueCopy(ctx, r.OrgID, r.ID))
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
