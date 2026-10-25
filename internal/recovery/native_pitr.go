package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

var ErrNativePITRUnavailable = errors.New("PITR is unavailable for this captured source and recovery profile")

// NativePITRBounds are provider observations. Protocol pins copy isolation rules;
// bounds may roll while a signed request retains its exact selected UTC time.
type NativePITRBounds struct {
	Protocol int       `json:"protocol"`
	Earliest time.Time `json:"earliest"`
	Latest   time.Time `json:"latest"`
	// SettingsDigest pins Cloud SQL clone isolation to the reviewed source settings.
	SettingsDigest string `json:"settings_digest,omitempty"`
}

func validNativePITRIdentity(provider string, bounds *NativePITRBounds) bool {
	if bounds == nil || bounds.Protocol != 1 {
		return false
	}
	if provider == "rds" {
		return bounds.SettingsDigest == ""
	}
	if provider != "cloud-sql" || len(bounds.SettingsDigest) != sha256.Size*2 || strings.ToLower(bounds.SettingsDigest) != bounds.SettingsDigest {
		return false
	}
	_, err := hex.DecodeString(bounds.SettingsDigest)
	return err == nil
}

// NativePITRWindow is a dedicated window response, separate from snapshots.
type NativePITRWindow struct {
	SourceID          string     `json:"source_id"`
	Provider          string     `json:"provider"`
	Available         bool       `json:"available"`
	PointID           string     `json:"point_id,omitempty"`
	Earliest          *time.Time `json:"earliest,omitempty"`
	Latest            *time.Time `json:"latest,omitempty"`
	ObservedAt        *time.Time `json:"observed_at,omitempty"`
	UnavailableReason string     `json:"unavailable_reason,omitempty"`
}
type NativePITRBackend interface {
	NativePITRWindow(context.Context, *planning.Binding) (*NativePoint, error)
}
type NativePITRFetch func(context.Context, *NativeSource) (*NativePoint, error)
type NativePITRStore interface {
	RefreshNativePITR(context.Context, string, string, NativePITRFetch) (*NativePoint, error)
}

// PITRWindow always reads current provider bounds. Cached windows remain useful
// for request identity but never establish that a new restore is still possible.
func (s *NativeService) PITRWindow(ctx context.Context, p security.Principal, sourceID string) (*NativePITRWindow, error) {
	source, err := s.authorizedSource(ctx, p, sourceID, security.BackupRead)
	if err != nil {
		return nil, err
	}
	out := &NativePITRWindow{SourceID: source.ID, Provider: source.Provider}
	store, stored := s.Store.(NativePITRStore)
	backend, supported := s.Backend.(NativePITRBackend)
	if !stored || !supported {
		out.UnavailableReason = ErrNativePITRUnavailable.Error()
		return out, nil
	}
	point, err := store.RefreshNativePITR(ctx, p.OrgID, sourceID, func(fetchCtx context.Context, retained *NativeSource) (*NativePoint, error) {
		if authErr := s.Authz.Require(fetchCtx, p, security.BackupRead, retained.ProjectID); authErr != nil {
			return nil, authErr
		}
		point, readErr := backend.NativePITRWindow(fetchCtx, &retained.Binding)
		if authErr := s.Authz.Require(fetchCtx, p, security.BackupRead, retained.ProjectID); authErr != nil {
			return nil, authErr
		}
		return point, readErr
	})
	if errors.Is(err, ErrNativePITRUnavailable) {
		out.UnavailableReason = ErrNativePITRUnavailable.Error()
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if point == nil || point.PITR == nil {
		return nil, ErrNativeUnavailable
	}
	out.Available, out.PointID = true, point.ID
	out.Earliest, out.Latest, out.ObservedAt = &point.PITR.Earliest, &point.PITR.Latest, &point.LastSeenAt
	return out, nil
}

// SelectTime never supplies a default time or changes a previously captured
// selection. Snapshots retain their existing mode and serialized scope.
func (p NativePoint) SelectTime(provider string, at *time.Time) (NativePoint, error) {
	if p.Kind != "pitr" {
		if at != nil || p.RestoreTime != nil || p.PITR != nil {
			return NativePoint{}, ErrInvalid
		}
		return cloneNativePoint(p), nil
	}
	if at == nil || at.IsZero() || !validNativePITRIdentity(provider, p.PITR) {
		return NativePoint{}, ErrInvalid
	}
	_, offset := at.Zone()
	bounds := p.PITR
	if offset != 0 || provider == "rds" && at.Nanosecond() != 0 || bounds.Earliest.IsZero() || bounds.Latest.IsZero() || !bounds.Earliest.Before(bounds.Latest) || bounds.Latest.After(time.Now()) || at.Before(bounds.Earliest) || !at.Before(bounds.Latest) {
		return NativePoint{}, ErrInvalid
	}
	if p.RestoreTime != nil && !p.RestoreTime.Equal(*at) {
		return NativePoint{}, ErrConflict
	}
	p = cloneNativePoint(p)
	utc := at.UTC()
	p.RestoreTime = &utc
	return p, nil
}
func cloneNativePoint(p NativePoint) NativePoint {
	if p.ExpiresAt != nil {
		expires := *p.ExpiresAt
		p.ExpiresAt = &expires
	}
	if p.PITR != nil {
		bounds := *p.PITR
		p.PITR = &bounds
	}
	if p.RestoreTime != nil {
		at := *p.RestoreTime
		p.RestoreTime = &at
	}
	return p
}
func sameNativePointSelection(a, b NativePoint) bool {
	if !sameNativePointIdentity(a, b) {
		return false
	}
	if a.Kind != "pitr" {
		return a.RestoreTime == nil && b.RestoreTime == nil && a.PITR == nil && b.PITR == nil
	}
	return a.PITR != nil && b.PITR != nil && a.PITR.Protocol == 1 && b.PITR.Protocol == 1 && a.RestoreTime != nil && b.RestoreTime != nil && a.RestoreTime.Equal(*b.RestoreTime)
}
