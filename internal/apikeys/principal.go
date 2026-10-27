package apikeys

import (
	"context"
	"errors"

	"github.com/xraph/keysmith/id"
	"github.com/xraph/keysmith/key"
	"github.com/xraph/keysmith/store"

	"github.com/xraph/loomplane/internal/security"
)

// ValidatePrincipal checks a saved key identity without retaining its secret.
// The caller separately checks the user's current operation permission.
func (s *Service) ValidatePrincipal(ctx context.Context, p security.Principal) error {
	if p.OrgID == "" || p.UserID == "" || p.KeyID == "" || p.ReadOnly {
		return ErrRejected
	}
	kid, err := id.ParseKeyID(p.KeyID)
	if err != nil {
		return ErrRejected
	}
	k, err := s.engine.GetKey(s.tenant(ctx, p.OrgID), kid)
	if err != nil {
		if errors.Is(err, store.ErrKeyNotFound) {
			return ErrRejected
		}
		// Saved authority fails closed, but an outage is not a revoked key.
		// Keep backend details out of durable activity errors and API responses.
		return errors.New("saved API key authority is temporarily unavailable")
	}
	if k == nil || k.ID.String() != p.KeyID || k.AppID != s.appID || k.TenantID != p.OrgID || k.CreatedBy != p.UserID || k.State != key.StateActive || k.RevokedAt != nil || k.PolicyID != nil || len(k.Scopes) > 0 || k.ExpiresAt == nil || !k.ExpiresAt.After(s.now()) || access(k) != Write {
		return ErrRejected
	}
	return nil
}

var _ security.PrincipalVerifier = (*Service)(nil)
