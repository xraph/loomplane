package federation

import (
	"context"

	"github.com/xraph/loomplane/internal/security"
)

func (s *Service) authorizeTrustChange(ctx context.Context, p security.Principal, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.UserID == "" || p.OrgID == "" {
		return security.ErrUnauthenticated
	}
	if p.ReadOnly {
		return security.ErrForbidden
	}
	if s == nil || s.authz == nil {
		return ErrDisabled
	}
	if err := s.authorize(ctx, p, security.DestinationManage, id); err != nil {
		return err
	}
	return s.authorize(ctx, p, security.TrustManage, id)
}
