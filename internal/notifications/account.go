package notifications

import (
	"context"

	authid "github.com/xraph/authsome/id"
)

// AccountCleanup is an authsome plugin that removes a deleted user's phones.
type AccountCleanup struct{ svc *Service }

func NewAccountCleanup(s *Service) *AccountCleanup { return &AccountCleanup{svc: s} }

func (a *AccountCleanup) Name() string { return "loomplane-push-devices" }

func (a *AccountCleanup) OnAfterUserDelete(ctx context.Context, userID authid.UserID) error {
	return a.svc.DeleteUserDevices(ctx, userID.String())
}
