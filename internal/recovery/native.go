package recovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// ErrNativeUnavailable preserves previous observations when a refresh fails.
var ErrNativeUnavailable = errors.New("native recovery inventory could not refresh; check the original cloud account and its snapshot permissions")

// NativeSource survives binding removal. Private configuration remains internal.
type NativeSource struct {
	ID            string           `json:"id" forge:"id"`
	OrgID         string           `json:"org_id"`
	ProjectID     string           `json:"project_id"`
	EnvironmentID string           `json:"environment_id"`
	Name          string           `json:"name"`
	Provider      string           `json:"provider"`
	ConnectionID  string           `json:"connection_id"`
	Region        string           `json:"region"`
	PhysicalID    string           `json:"physical_id"`
	CapturedAt    time.Time        `json:"captured_at"`
	State         string           `json:"state"`
	Complete      bool             `json:"complete"`
	CheckedAt     *time.Time       `json:"checked_at,omitempty"`
	ObservedAt    *time.Time       `json:"observed_at,omitempty"`
	Error         string           `json:"error,omitempty"`
	Binding       planning.Binding `json:"-"`
}

// NativePoint is provider metadata, not a logical backup object in Trove.
// Its identity includes the source, native reference and creation time.
type NativePoint struct {
	ID         string     `json:"id" forge:"id"`
	SourceID   string     `json:"source_id"`
	ProviderID string     `json:"provider_id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	Engine     string     `json:"engine"`
	Version    string     `json:"version"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at" nullable:"true" required:"true"`
	Encrypted  bool       `json:"encrypted"`
	KMSKey     string     `json:"kms_key,omitempty"`
	Present    bool       `json:"present"`
	LastSeenAt time.Time  `json:"last_seen_at"`
}

type NativePageInput struct {
	EnvironmentID string `json:"environment_id,omitempty"`
	Before        string `json:"before,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

func (p NativePageInput) pageSize() (int, error) {
	if p.Limit == 0 {
		return 50, nil
	}
	if p.Limit < 1 || p.Limit > 100 {
		return 0, fmt.Errorf("%w: native inventory limit must be from 1 to 100", ErrInvalid)
	}
	return p.Limit, nil
}

type NativeSourcesPage struct {
	Sources    []NativeSource `json:"sources"`
	NextBefore string         `json:"next_before,omitempty"`
}

type NativeInventory struct {
	Source     NativeSource  `json:"source"`
	Points     []NativePoint `json:"points"`
	NextBefore string        `json:"next_before,omitempty"`
}

type NativeFetch func(context.Context, *NativeSource) ([]NativePoint, bool, error)
type NativeStore interface {
	GetNativeSource(context.Context, string, string) (*NativeSource, error)
	GetNativePoint(context.Context, string, string, string) (*NativePoint, error)
	ListNativeSources(context.Context, string, string, NativePageInput) (*NativeSourcesPage, error)
	ReadNativeInventory(context.Context, string, string, NativePageInput) (*NativeInventory, error)
	RefreshNativeInventory(context.Context, string, string, NativeFetch) error
}
type NativeBackend interface {
	NativeSnapshots(context.Context, *planning.Binding) ([]NativePoint, bool, error)
	CheckNativeSnapshot(context.Context, *planning.Binding, NativePoint) (*NativePoint, error)
}
type NativeService struct {
	Store       NativeStore
	Authz       Authorizer
	Projects    Projects
	Backend     NativeBackend
	Copies      NativeCopyWorkStore
	CopyBackend NativeCopyBackend
	EnqueueCopy func(context.Context, string, string) error
}

func (s *NativeService) List(ctx context.Context, p security.Principal, project string, page NativePageInput) (*NativeSourcesPage, error) {
	if err := s.Authz.Require(ctx, p, security.BackupRead, project); err != nil {
		return nil, err
	}
	if _, err := s.Projects.GetProject(ctx, p.OrgID, project); err != nil {
		return nil, err
	}
	if _, err := page.pageSize(); err != nil {
		return nil, err
	}
	return s.Store.ListNativeSources(ctx, p.OrgID, project, page)
}
func (s *NativeService) authorizeSource(ctx context.Context, p security.Principal, id string) error {
	_, err := s.authorizedSource(ctx, p, id, security.BackupRead)
	return err
}

func (s *NativeService) authorizedSource(ctx context.Context, p security.Principal, id string, permission security.Permission) (*NativeSource, error) {
	source, err := s.Store.GetNativeSource(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if source == nil || source.ID != id || p.OrgID == "" || source.OrgID != p.OrgID || source.ProjectID == "" {
		return nil, ErrNotFound
	}
	if err := s.Authz.Require(ctx, p, permission, source.ProjectID); err != nil {
		return nil, err
	}
	if _, err := s.Projects.GetProject(ctx, p.OrgID, source.ProjectID); err != nil {
		return nil, err
	}
	return source, nil
}
func (s *NativeService) Inventory(ctx context.Context, p security.Principal, id string, page NativePageInput) (*NativeInventory, error) {
	if err := s.authorizeSource(ctx, p, id); err != nil {
		return nil, err
	}
	if _, err := page.pageSize(); err != nil {
		return nil, err
	}
	return s.Store.ReadNativeInventory(ctx, p.OrgID, id, page)
}
func (s *NativeService) Refresh(ctx context.Context, p security.Principal, id string) (*NativeInventory, error) {
	if err := s.authorizeSource(ctx, p, id); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	err := s.Store.RefreshNativeInventory(ctx, p.OrgID, id, func(fetchCtx context.Context, source *NativeSource) ([]NativePoint, bool, error) {
		return s.Backend.NativeSnapshots(fetchCtx, &source.Binding)
	})
	if err != nil {
		return nil, err
	}
	return s.Store.ReadNativeInventory(ctx, p.OrgID, id, NativePageInput{})
}
