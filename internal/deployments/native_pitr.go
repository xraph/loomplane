package deployments

import (
	"context"
	"errors"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
)

// NativePITRWindow reads the immutable source's actual log window. The current
// Cloud SQL copy profile cannot fence inherited clone networking at creation.
func (n *NativeDatabases) NativePITRWindow(ctx context.Context, b *planning.Binding) (*recovery.NativePoint, error) {
	cfg, conn, credential, err := n.nativeSourceAccess(ctx, b)
	if err != nil {
		return nil, err
	}
	if b.Provider != "rds" {
		return nil, recovery.ErrNativePITRUnavailable
	}
	key, err := aws.ParseKey(credential)
	if err != nil {
		return nil, err
	}
	window, err := n.AWS.Session(key, b.Region, conn.Account).ReadDBPITRWindow(ctx, b.PhysicalID)
	if errors.Is(err, aws.ErrDBPITRUnavailable) {
		return nil, recovery.ErrNativePITRUnavailable
	}
	if err != nil {
		return nil, err
	}
	if window.InstanceID != b.ExternalID || window.MasterUser != sqlUser || window.Engine != cfg.Kind || strings.Split(window.EngineVersion, ".")[0] != cfg.Version || cfg.Managed == nil || window.StorageGB <= 0 || window.StorageGB > cfg.Managed.Profile.StorageGB || !window.Encrypted || window.KMSKey == "" {
		return nil, recovery.ErrNativePITRUnavailable
	}
	return &recovery.NativePoint{ProviderID: window.ARN, Name: window.InstanceID, Kind: "pitr", Status: "available", Engine: window.Engine, Version: window.EngineVersion, CreatedAt: window.CreatedAt, Encrypted: window.Encrypted, KMSKey: window.KMSKey, PITR: &recovery.NativePITRBounds{Protocol: 1, Earliest: window.Earliest, Latest: window.Latest}}, nil
}
