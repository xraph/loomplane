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
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	current, err := s.Backend.CheckNativeSnapshot(ctx, &source.Binding, *selected)
	if err != nil || current == nil {
		return nil, ErrNativeUnavailable
	}
	if selected.ProviderID == "" || selected.CreatedAt.IsZero() || current.ProviderID != selected.ProviderID || current.Name != selected.Name || !current.CreatedAt.Equal(selected.CreatedAt) || current.Kind != selected.Kind || current.Engine != selected.Engine || current.Version != selected.Version || current.Encrypted != selected.Encrypted || current.KMSKey != selected.KMSKey {
		return nil, ErrNativeUnavailable
	}
	status := "available"
	if source.Provider == "cloud-sql" {
		status = "SUCCESSFUL"
	}
	now := time.Now().UTC()
	if current.Status != status || current.ExpiresAt != nil && (current.ExpiresAt.IsZero() || !current.ExpiresAt.After(now) || !current.ExpiresAt.After(current.CreatedAt)) {
		return nil, ErrNativeUnavailable
	}
	current.ID, current.SourceID, current.Present, current.LastSeenAt = pointID, sourceID, true, now
	return current, nil
}
