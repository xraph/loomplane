package apikeys

import (
	"context"
	"strings"
	"time"

	"github.com/xraph/keysmith"
	"github.com/xraph/keysmith/id"
	"github.com/xraph/keysmith/key"

	"github.com/xraph/loomplane/internal/security"
)

// CreateFleetCredential issues a service identity from a human owner session.
// The fleet service resolves the project/fleet scope before calling this method.
func (s *Service) CreateFleetCredential(ctx context.Context, p security.Principal, scope security.FleetScope, in NewKey) (*CreatedKey, error) {
	if p.IsServiceAccount() || p.Fleet != nil || p.KeyID != "" || p.ReadOnly || p.OrgID == "" || p.UserID == "" {
		return nil, security.ErrForbidden
	}
	allowed, err := s.admin(ctx, p)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, security.ErrForbidden
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > MaxNameLength || (in.Access != Read && in.Access != Write) || in.ExpiresInDays < 0 || in.ExpiresInDays > int(MaxLifetime/(24*time.Hour)) || strings.TrimSpace(scope.ProjectID) == "" || len(scope.ProjectID) > 128 || strings.TrimSpace(scope.FleetID) == "" || len(scope.FleetID) > 128 {
		return nil, ErrInvalid
	}
	lifetime := DefaultLifetime
	if in.ExpiresInDays > 0 {
		lifetime = time.Duration(in.ExpiresInDays) * 24 * time.Hour
	}
	expires := s.now().Add(lifetime).UTC()
	res, err := s.engine.CreateKey(s.tenant(ctx, p.OrgID), &keysmith.CreateKeyInput{Name: name, Prefix: Prefix, Environment: key.EnvLive, TenantID: p.OrgID, CreatedBy: p.UserID, ExpiresAt: &expires, Metadata: map[string]any{"kind": string(security.KindFleet), "access": string(in.Access), "project_id": scope.ProjectID, "fleet_id": scope.FleetID}})
	if err != nil {
		return nil, err
	}
	out := &CreatedKey{Key: view(res.Key), Secret: res.RawKey}
	if s.audit != nil {
		s.audit.Record(ctx, "fleet.credential.created", p, out.Key)
	}
	return out, nil
}

func (s *Service) fleetPrincipal(k *key.Key) (security.Principal, error) {
	if k == nil || k.AppID != s.appID || k.TenantID == "" || k.CreatedBy == "" || k.State != key.StateActive || k.RevokedAt != nil || k.ExpiresAt == nil || !k.ExpiresAt.After(s.now()) || k.PolicyID != nil || len(k.Scopes) > 0 {
		return security.Principal{}, ErrRejected
	}
	kind, kindOK := k.Metadata["kind"].(string)
	project, projectOK := k.Metadata["project_id"].(string)
	fleetID, fleetOK := k.Metadata["fleet_id"].(string)
	accessMode, accessOK := k.Metadata["access"].(string)
	if !kindOK || !projectOK || !fleetOK || !accessOK || kind != string(security.KindFleet) || project == "" || fleetID == "" || (accessMode != string(Read) && accessMode != string(Write)) {
		return security.Principal{}, ErrRejected
	}
	return security.Principal{OrgID: k.TenantID, UserID: security.FleetSubject(k.ID.String()), KeyID: k.ID.String(), Kind: security.KindFleet, ReadOnly: accessMode == string(Read), Fleet: &security.FleetScope{ProjectID: project, FleetID: fleetID}}, nil
}
func (s *Service) validateFleetPrincipal(ctx context.Context, p security.Principal) error {
	if p.OrgID == "" || p.Fleet == nil || p.KeyID == "" {
		return ErrRejected
	}
	kid, err := id.ParseKeyID(p.KeyID)
	if err != nil {
		return ErrRejected
	}
	k, err := s.engine.GetKey(s.tenant(ctx, p.OrgID), kid)
	if err != nil {
		return ErrRejected
	}
	current, err := s.fleetPrincipal(k)
	if err != nil {
		return err
	}
	if current.KeyID != p.KeyID || current.OrgID != p.OrgID || current.UserID != p.UserID || current.ReadOnly != p.ReadOnly || *current.Fleet != *p.Fleet {
		return ErrRejected
	}
	return nil
}
