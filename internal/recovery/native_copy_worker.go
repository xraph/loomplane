package recovery

import (
	"bytes"
	"context"
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
	if err == nil {
		err = nativeCopyScope(frozen)
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

func (s *NativeService) copyControl(p security.Principal, r NativeRestore) NativeCopyControl {
	control := s.nativeControl(p, r, "running", s.Copies)
	return NativeCopyControl{
		NativeWorkerControl: control,
		Selected: func(ctx context.Context) (*NativePoint, error) {
			if err := control.Check(ctx); err != nil {
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
			if err := control.Check(ctx); err != nil {
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
	return reconcileNative(ctx, s.Copies.PendingNativeRestores, s.EnqueueCopy)
}
