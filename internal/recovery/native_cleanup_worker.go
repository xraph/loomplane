package recovery

import (
	"context"
	"errors"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/security"
)

func (s *NativeService) RequestCopyCleanup(ctx context.Context, p security.Principal, id string) (*NativeRestore, error) {
	if s.Cleanups == nil || s.EnqueueCleanup == nil {
		return nil, ErrInvalid
	}
	r, err := s.Cleanups.GetNativeRestore(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if r == nil || r.ID != id || r.OrgID != p.OrgID {
		return nil, ErrNotFound
	}
	guard := func(ctx context.Context) error { return s.requireNative(ctx, p, r.ProjectID) }
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	r, err = s.Cleanups.RequestNativeCleanup(ctx, p.OrgID, id, p.UserID, guard)
	if err != nil {
		return nil, err
	}
	if r.Status == "cleanup_queued" {
		return r, s.EnqueueCleanup(ctx, p.OrgID, r.ID)
	}
	return r, nil
}

// RunCleanup keeps the saved manual caller through retries. Scheduled expiry
// uses the creator only when no administrator has requested cleanup.
func (s *NativeService) RunCleanup(ctx context.Context, org, id string) error {
	if s.Cleanups == nil || s.CleanupBackend == nil || s.Authz == nil || s.Projects == nil {
		return ErrInvalid
	}
	token, err := typeid.Generate("nativecleanup")
	if err != nil {
		return err
	}
	claimed, err := s.Cleanups.ClaimNativeCleanup(ctx, org, id, token.String())
	if errors.Is(err, ErrConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	if claimed == nil || claimed.OrgID != org || claimed.ID != id || claimed.Token != token.String() {
		return ErrConflict
	}
	frozen := copyNativeRestore(*claimed)
	actor := frozen.CleanupBy
	if actor == "" {
		actor = frozen.CreatedBy
	}
	p := security.Principal{OrgID: org, UserID: actor}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	control := s.nativeControl(p, frozen, "deleting", s.Cleanups)
	err = control.Check(ctx)
	if err == nil {
		err = nativeCopyScope(frozen)
	}
	var result NativeCleanupResult
	if err == nil {
		result, err = s.CleanupBackend.DeleteNative(ctx, p, copyNativeRestore(frozen), control)
	}
	if err == nil && !result.ConfirmedAbsent {
		err = ErrInvalid
	}
	if err == nil {
		err = control.Check(ctx)
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer stop()
	return errors.Join(err, s.Cleanups.FinishNativeCleanup(finish, &frozen, err))
}

func (s *NativeService) ReconcileCleanups(ctx context.Context) error {
	if s.Cleanups == nil || s.EnqueueCleanup == nil {
		return ErrInvalid
	}
	return reconcileNative(ctx, s.Cleanups.PendingNativeCleanups, s.EnqueueCleanup)
}
