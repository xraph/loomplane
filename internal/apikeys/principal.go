package apikeys

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/xraph/keysmith/id"
	"github.com/xraph/keysmith/key"
	"github.com/xraph/keysmith/store"

	"github.com/xraph/loomplane/internal/security"
)

// ValidatePrincipal checks a saved key identity without retaining its secret.
// The caller separately checks the user's current operation permission.
func (s *Service) ValidatePrincipal(ctx context.Context, p security.Principal) error {
	if p.IsFleet() {
		return s.validateFleetPrincipal(ctx, p)
	}
	if p.Fleet != nil || security.IsFleetSubject(p.UserID) {
		return ErrRejected
	}
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
	if err := validateSavedKeyRecordAt(p, s.appID, k, s.now()); err != nil {
		return err
	}
	// GetKey does not hydrate persisted links. This preserves the ordinary
	// path's existing check; current authority must separately read link existence.
	if len(k.Scopes) > 0 {
		return ErrRejected
	}
	return nil
}

// ValidateSavedKeyRecordAt validates a loaded ordinary key at the supplied time.
// It does not establish persisted scope absence or current person eligibility.
// Those facts require separate reads by the current-authority adapter.
func ValidateSavedKeyRecordAt(p security.Principal, appID string, k *key.Key, at time.Time) error {
	if p.Kind != security.KindUser || p.OnBehalfOf != nil || p.Fleet != nil ||
		security.IsFleetSubject(p.UserID) || strings.HasPrefix(p.UserID, "loomplane:") || strings.HasPrefix(p.UserID, "agent:") || p.ReadOnly || p.UserID == "" || p.OrgID == "" || p.KeyID == "" || appID == "" || at.IsZero() {
		return ErrRejected
	}
	if _, err := id.ParseKeyID(p.KeyID); err != nil {
		return ErrRejected
	}
	return validateSavedKeyRecordAt(p, appID, k, at)
}

func validateSavedKeyRecordAt(p security.Principal, appID string, k *key.Key, at time.Time) error {
	if k == nil || k.ID.String() != p.KeyID || k.AppID != appID || k.TenantID != p.OrgID || k.CreatedBy != p.UserID || k.State != key.StateActive || k.RevokedAt != nil || k.PolicyID != nil || k.ExpiresAt == nil || !k.ExpiresAt.After(at) || access(k) != Write {
		return ErrRejected
	}
	if _, scoped := k.Metadata["kind"]; scoped {
		return ErrRejected
	}
	return nil
}

var _ security.PrincipalVerifier = (*Service)(nil)
