package deployments

import (
	"context"
	"errors"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
)

// NativePITRWindow reads the original source's current log window and isolation.
func (n *NativeDatabases) NativePITRWindow(ctx context.Context, b *planning.Binding) (*recovery.NativePoint, error) {
	cfg, conn, credential, err := n.nativeSourceAccess(ctx, b)
	if err != nil {
		return nil, err
	}
	if b.Provider == "cloud-sql" {
		return n.cloudSQLPITRWindow(ctx, b, cfg, conn, credential)
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

func cloudSQLPITRSourceSpec(cfg *planning.ResourceConfig, name string) gcp.SQLInstanceSpec {
	profile := cfg.Managed.Profile
	return gcp.SQLInstanceSpec{Name: name, Region: cfg.Region, DatabaseVersion: "POSTGRES_17", Tier: profile.ComputeClass,
		Storage: &gcp.SQLStorageSettings{SizeGB: profile.StorageGB, Type: profile.StorageType, RetainedBackups: profile.BackupRetentionValue, RecoveryDays: profile.PointInTimeRecoveryDays}}
}

func (n *NativeDatabases) cloudSQLPITRWindow(ctx context.Context, b *planning.Binding, cfg *planning.ResourceConfig, conn *connections.Connection, credential []byte) (*recovery.NativePoint, error) {
	profile, err := manageddb.CloudSQLPSCPostgres("17")
	if err != nil || cfg.Managed == nil || cfg.Managed.Profile != *profile || !cfg.Managed.CloudSQLPSC.Valid(cfg.Region) {
		return nil, recovery.ErrNativePITRUnavailable
	}
	key, err := gcp.ParseKey(credential)
	if err != nil {
		return nil, err
	}
	if key.ProjectID != conn.Details["project_id"] {
		return nil, recovery.ErrConflict
	}
	name, region, err := boundSQLInstance(b.ExternalID, key.ProjectID, b.Region)
	if err != nil {
		return nil, err
	}
	client, err := n.Google.Session(ctx, key)
	if err != nil {
		return nil, err
	}
	window, err := client.ReadSQLPITRWindow(ctx, region, name, b.PhysicalID, cloudSQLPITRSourceSpec(cfg, name))
	if errors.Is(err, gcp.ErrSQLPITRUnavailable) {
		return nil, recovery.ErrNativePITRUnavailable
	}
	if err != nil {
		return nil, err
	}
	return &recovery.NativePoint{ProviderID: window.PhysicalID, Name: name, Kind: "pitr", Status: "available", Engine: "postgres", Version: "17", CreatedAt: window.CreatedAt, Encrypted: true,
		PITR: &recovery.NativePITRBounds{Protocol: 1, Earliest: window.Earliest, Latest: window.Latest, SettingsDigest: window.SettingsDigest}}, nil
}
