package fleets

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

var (
	ErrApprovalRequired = errors.New("this instance operation requires human review")
	ErrAuthorityRevoked = errors.New("instance operation authority expired or was revoked")
)

// OperationDemand comes from the server's reviewed planner, never a public request.
type OperationDemand struct {
	CPUMillis        int64  `json:"cpu_millis"`
	MemoryMiB        int64  `json:"memory_mib"`
	StorageGiB       int64  `json:"storage_gib"`
	HourlyCostMicros int64  `json:"hourly_cost_micros"`
	Currency         string `json:"currency"`
	PriceKnown       bool   `json:"price_known"`
}

// OperationRequest pins the rendered inputs and resolved placement before admission.
type OperationRequest struct {
	GrantID        string          `json:"grant_id"`
	Kind           string          `json:"kind"`
	StageID        string          `json:"stage_id"`
	ReleaseID      string          `json:"release_id"`
	ProfileID      string          `json:"profile_id"`
	CloudID        string          `json:"cloud_id"`
	Region         string          `json:"region"`
	ExternalRef    string          `json:"external_ref"`
	IdempotencyKey string          `json:"idempotency_key"`
	SnapshotDigest string          `json:"snapshot_digest"`
	Demand         OperationDemand `json:"demand"`
}

// OperationAuthority is checked against its persisted copy at every effect boundary.
// It contains credential identities only, never bearer tokens or provider secrets.
type OperationAuthority struct {
	ID            string             `json:"id"`
	Scope         FleetScope         `json:"scope"`
	Principal     security.Principal `json:"principal"`
	GrantRevision int64              `json:"grant_revision"`
	StageRevision int64              `json:"stage_revision"`
	Request       OperationRequest   `json:"request"`
	ExpiresAt     time.Time          `json:"expires_at"`
}
type GrantStore interface {
	CreateGrant(context.Context, FleetScope, *fleet.Grant) error
	GetGrant(context.Context, FleetScope, string) (*fleet.Grant, error)
	RevokeGrant(context.Context, FleetScope, string, int64) error
	ReserveAuthority(context.Context, *OperationAuthority) (*OperationAuthority, error)
	GetAuthority(context.Context, FleetScope, string) (*OperationAuthority, error)
	CheckAuthorityPolicy(context.Context, OperationAuthority) error
}

func (s *Service) grantOwner(ctx context.Context, p security.Principal) error {
	if p.IsServiceAccount() || p.Fleet != nil || p.KeyID != "" || p.ReadOnly {
		return security.ErrForbidden
	}
	membership, ok := s.authz.(interface {
		RequireCurrentMembership(context.Context, security.Principal) error
	})
	if !ok {
		return security.ErrForbidden
	}
	if err := membership.RequireCurrentMembership(ctx, p); err != nil {
		return err
	}
	return s.authz.Require(ctx, p, security.OrganizationAdminister, "")
}
func validGrant(in fleet.CreateGrantInput) bool {
	if in.CredentialID == "" || len(in.CredentialID) > 128 || in.StageID == "" || !in.ExpiresAt.After(time.Now()) || in.ExpiresAt.After(time.Now().Add(366*24*time.Hour)) {
		return false
	}
	for _, values := range [][]string{in.ReleaseIDs, in.ProfileIDs, in.CloudIDs, in.Regions, in.Actions} {
		if len(values) == 0 || len(values) > 100 {
			return false
		}
		seen := map[string]bool{}
		for _, v := range values {
			if strings.TrimSpace(v) == "" || len(v) > 128 || seen[v] {
				return false
			}
			seen[v] = true
		}
	}
	for _, action := range in.Actions {
		if !slices.Contains([]string{"create", "update", "scale", "resize", "suspend", "resume"}, action) {
			return false
		}
	}
	l := in.Limits
	if l.MaxInstances < 1 || l.MaxInstances > 100000 || l.CPUMillis < 1 || l.MemoryMiB < 1 || l.StorageGiB < 0 || l.HourlyCostMicros < 0 || len(l.Currency) != 3 || strings.Trim(l.Currency, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return false
	}
	for _, v := range []int64{l.CPUMillis, l.MemoryMiB, l.StorageGiB, l.HourlyCostMicros} {
		if v > 1_000_000_000_000_000 {
			return false
		}
	}
	return true
}

// CreateGrant is restricted to current human owner sessions.
func (s *Service) CreateGrant(ctx context.Context, p security.Principal, fleetID string, in fleet.CreateGrantInput) (*fleet.Grant, error) {
	if err := s.grantOwner(ctx, p); err != nil {
		return nil, err
	}
	f, err := s.store.GetFleet(ctx, p.OrgID, fleetID)
	if err != nil {
		return nil, err
	}
	if !validGrant(in) || f.Status != "active" {
		return nil, ErrInvalid
	}
	scope := FleetScope{OrgID: p.OrgID, ProjectID: f.ProjectID, FleetID: f.ID}
	releases, ok := s.store.(ReleaseStore)
	if !ok {
		return nil, ErrInvalid
	}
	for _, releaseID := range in.ReleaseIDs {
		release, loadErr := releases.GetRelease(ctx, scope, releaseID)
		if loadErr != nil {
			return nil, loadErr
		}
		for _, profile := range in.ProfileIDs {
			if _, found := release.Template.Profiles[profile]; !found {
				return nil, ErrInvalid
			}
		}
	}
	store, ok := s.store.(GrantStore)
	if !ok {
		return nil, ErrInvalid
	}
	grant := &fleet.Grant{ID: uuid.NewString(), FleetID: f.ID, Revision: 1, Status: "active", Input: in, CreatedBy: p.UserID, CreatedAt: time.Now().UTC()}
	if createErr := store.CreateGrant(ctx, scope, grant); createErr != nil {
		return nil, createErr
	}
	s.record(ctx, p, f.ProjectID, "fleet.grant.create", grant.ID)
	return grant, nil
}

// RevokeGrant invalidates queued authority without deleting recovery records.
func (s *Service) RevokeGrant(ctx context.Context, p security.Principal, fleetID, id string, revision int64) error {
	if err := s.grantOwner(ctx, p); err != nil {
		return err
	}
	f, err := s.store.GetFleet(ctx, p.OrgID, fleetID)
	if err != nil {
		return err
	}
	store, ok := s.store.(GrantStore)
	if !ok {
		return ErrInvalid
	}
	if revokeErr := store.RevokeGrant(ctx, FleetScope{OrgID: p.OrgID, ProjectID: f.ProjectID, FleetID: f.ID}, id, revision); revokeErr != nil {
		return revokeErr
	}
	s.record(ctx, p, f.ProjectID, "fleet.grant.revoke", id)
	return nil
}
func (s *Service) credentialScope(ctx context.Context, p security.Principal, scope FleetScope) error {
	if !p.IsFleet() || p.Fleet == nil || p.OrgID != scope.OrgID || p.Fleet.ProjectID != scope.ProjectID || p.Fleet.FleetID != scope.FleetID {
		return ErrNotFound
	}
	if s.keys == nil {
		return security.ErrForbidden
	}
	return s.keys.ValidatePrincipal(ctx, p)
}

// AuthorizeOperation reserves grant ceilings atomically. Later lifecycle actions
// use their reviewed workflow; this admission path currently accepts creation.
func (s *Service) AuthorizeOperation(ctx context.Context, p security.Principal, scope FleetScope, request OperationRequest) (*OperationAuthority, error) {
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	if !p.IsFleet() {
		if err := s.authz.Require(ctx, p, security.DeploymentCreate, scope.ProjectID); err != nil {
			return nil, err
		}
		return nil, ErrApprovalRequired
	}
	if err := s.credentialScope(ctx, p, scope); err != nil {
		return nil, err
	}
	f, err := s.store.GetFleet(ctx, p.OrgID, scope.FleetID)
	if err != nil {
		return nil, err
	}
	if f.ProjectID != scope.ProjectID {
		return nil, ErrNotFound
	}
	if f.Status != "active" {
		return nil, ErrConflict
	}
	store, ok := s.store.(GrantStore)
	if !ok {
		return nil, ErrInvalid
	}
	grant, err := store.GetGrant(ctx, scope, request.GrantID)
	if err != nil {
		return nil, err
	}
	if grantErr := acceptGrant(grant, p, request); grantErr != nil {
		return nil, grantErr
	}
	releases, ok := s.store.(ReleaseStore)
	if !ok {
		return nil, ErrInvalid
	}
	release, err := releases.GetRelease(ctx, scope, request.ReleaseID)
	if err != nil {
		return nil, err
	}
	profile, ok := release.Template.Profiles[request.ProfileID]
	if !ok {
		return nil, ErrInvalid
	}
	minimum, err := templateWorkloadDemand(release.Template, profile)
	if err != nil {
		return nil, err
	}
	if request.Demand.CPUMillis < minimum.CPUMillis || request.Demand.MemoryMiB < minimum.MemoryMiB || request.Demand.StorageGiB < minimum.StorageGiB {
		return nil, ErrApprovalRequired
	}

	authority := &OperationAuthority{ID: uuid.NewString(), Scope: scope, Principal: p, GrantRevision: grant.Revision, Request: request, ExpiresAt: grant.Input.ExpiresAt}
	return store.ReserveAuthority(ctx, authority)
}
func acceptGrant(g *fleet.Grant, p security.Principal, r OperationRequest) error {
	return acceptGrantAt(g, p, r, time.Now())
}

func acceptGrantAt(g *fleet.Grant, p security.Principal, r OperationRequest, at time.Time) error {
	if g == nil || at.IsZero() || g.Status != "active" || !g.Input.ExpiresAt.After(at) {
		return ErrAuthorityRevoked
	}
	if r.Kind != "create" || g.Input.CredentialID != p.KeyID || g.Input.StageID != r.StageID || !slices.Contains(g.Input.Actions, r.Kind) || !slices.Contains(g.Input.ReleaseIDs, r.ReleaseID) || !slices.Contains(g.Input.ProfileIDs, r.ProfileID) || !slices.Contains(g.Input.CloudIDs, r.CloudID) || !slices.Contains(g.Input.Regions, r.Region) || !r.Demand.PriceKnown || r.Demand.Currency != g.Input.Limits.Currency {
		return ErrApprovalRequired
	}
	for _, v := range []string{r.ExternalRef, r.IdempotencyKey} {
		if strings.TrimSpace(v) == "" || len(v) > 128 {
			return ErrInvalid
		}
	}
	if len(r.SnapshotDigest) != 71 || !strings.HasPrefix(r.SnapshotDigest, "sha256:") {
		return ErrInvalid
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(r.SnapshotDigest, "sha256:")); err != nil {
		return ErrInvalid
	}
	d := r.Demand
	l := g.Input.Limits
	if d.CPUMillis < 1 || d.MemoryMiB < 1 || d.StorageGiB < 0 || d.HourlyCostMicros < 0 || d.CPUMillis > l.CPUMillis || d.MemoryMiB > l.MemoryMiB || d.StorageGiB > l.StorageGiB || d.HourlyCostMicros > l.HourlyCostMicros {
		return ErrCapacity
	}
	return nil
}

// RevalidateAuthority rejects mutated input, stale grants and revoked credentials.
func (s *Service) RevalidateAuthority(ctx context.Context, a OperationAuthority) error {
	store, ok := s.store.(GrantStore)
	if !ok {
		return ErrInvalid
	}
	stored, err := store.GetAuthority(ctx, a.Scope, a.ID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	original, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, original) {
		return fmt.Errorf("%w: authority input changed", ErrConflict)
	}
	if credentialErr := s.credentialScope(ctx, stored.Principal, stored.Scope); credentialErr != nil {
		return credentialErr
	}
	grant, err := store.GetGrant(ctx, stored.Scope, stored.Request.GrantID)
	if err != nil {
		return err
	}
	if grant.Revision != stored.GrantRevision || !stored.ExpiresAt.After(time.Now()) {
		return ErrAuthorityRevoked
	}
	if grantErr := acceptGrant(grant, stored.Principal, stored.Request); grantErr != nil {
		return grantErr
	}
	return store.CheckAuthorityPolicy(ctx, *stored)
}
