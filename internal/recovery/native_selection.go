package recovery

import (
	"context"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

// CheckPoint revalidates a retained native point before restore admission. A
// successful metadata check does not create a copy or establish SQL recovery.
func (s *NativeService) CheckPoint(ctx context.Context, p security.Principal, sourceID, pointID string) (*NativePoint, error) {
	return s.CheckPointAt(ctx, p, sourceID, pointID, nil)
}

// CheckPointAt requires an explicit UTC time for PITR and rechecks rolling bounds.
func (s *NativeService) CheckPointAt(ctx context.Context, p security.Principal, sourceID, pointID string, at *time.Time) (*NativePoint, error) {
	source, err := s.authorizedSource(ctx, p, sourceID, security.RestoreManage)
	if err != nil {
		return nil, err
	}
	if source.Provider != "rds" && source.Provider != "cloud-sql" {
		return nil, ErrInvalid
	}
	selected, err := s.Store.GetNativePoint(ctx, p.OrgID, sourceID, pointID)
	if err != nil {
		return nil, err
	}
	if selected == nil || selected.ID != pointID || selected.SourceID != sourceID {
		return nil, ErrNotFound
	}
	selection, err := selected.SelectTime(source.Provider, at)
	if err != nil {
		return nil, err
	}
	selected = &selection
	if s.Retirements != nil {
		retired, retirementErr := s.Retirements.NativePointRetired(ctx, p.OrgID, sourceID, pointID)
		if retirementErr != nil {
			return nil, retirementErr
		}
		if retired {
			return nil, ErrConflict
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	current, err := s.Backend.CheckNativeSnapshot(ctx, &source.Binding, *selected)
	if err != nil || current == nil {
		return nil, ErrNativeUnavailable
	}
	now := time.Now().UTC()
	if err := checkNativePoint(source.Provider, *selected, current, now); err != nil {
		return nil, err
	}
	fresh := cloneNativePoint(*current)
	current = &fresh
	current.RestoreTime = selected.RestoreTime
	current.ID, current.SourceID, current.Present, current.LastSeenAt = pointID, sourceID, true, now
	return current, nil
}

func checkNativePoint(provider string, selected NativePoint, current *NativePoint, now time.Time) error {
	if current == nil || !sameNativePointIdentity(selected, *current) {
		return ErrNativeUnavailable
	}
	if selected.Kind == "pitr" {
		if current.Status != "available" || current.ExpiresAt != nil {
			return ErrNativeUnavailable
		}
		if _, err := current.SelectTime(provider, selected.RestoreTime); err != nil {
			return ErrNativePITRUnavailable
		}
		return nil
	}
	if selected.PITR != nil || current.PITR != nil || selected.RestoreTime != nil || current.RestoreTime != nil {
		return ErrInvalid
	}
	status := "available"
	if provider == "cloud-sql" {
		status = "SUCCESSFUL"
	} else if provider != "rds" {
		return ErrInvalid
	}
	if current.Status != status || current.ExpiresAt != nil && (current.ExpiresAt.IsZero() || !current.ExpiresAt.After(now) || !current.ExpiresAt.After(current.CreatedAt)) {
		return ErrNativeUnavailable
	}
	return nil
}

func sameNativePointIdentity(a, b NativePoint) bool {
	return (a.Kind != "pitr" || a.PITR != nil && b.PITR != nil && a.PITR.Protocol == 1 && b.PITR.Protocol == 1) && a.ProviderID != "" && !a.CreatedAt.IsZero() && a.ProviderID == b.ProviderID && a.Name == b.Name && a.CreatedAt.Equal(b.CreatedAt) && a.Kind == b.Kind && a.Engine == b.Engine && a.Version == b.Version && a.Encrypted == b.Encrypted && a.KMSKey == b.KMSKey
}
