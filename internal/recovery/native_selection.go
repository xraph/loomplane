package recovery

import (
	"context"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

// CheckPoint revalidates a retained native point before restore admission. A
// successful metadata check does not create a copy or establish SQL recovery.
func (s *NativeService) CheckPoint(ctx context.Context, p security.Principal, sourceID, pointID string) (*NativePoint, error) {
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
	current.ID, current.SourceID, current.Present, current.LastSeenAt = pointID, sourceID, true, now
	return current, nil
}

func checkNativePoint(provider string, selected NativePoint, current *NativePoint, now time.Time) error {
	if current == nil || !sameNativePointIdentity(selected, *current) {
		return ErrNativeUnavailable
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
	return a.ProviderID != "" && !a.CreatedAt.IsZero() && a.ProviderID == b.ProviderID && a.Name == b.Name && a.CreatedAt.Equal(b.CreatedAt) && a.Kind == b.Kind && a.Engine == b.Engine && a.Version == b.Version && a.Encrypted == b.Encrypted && a.KMSKey == b.KMSKey
}
