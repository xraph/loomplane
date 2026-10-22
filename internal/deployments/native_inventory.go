package deployments

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
)

// NativeDatabases reads provider metadata for the original reviewed source.
// It never substitutes Trove exports for provider-native recovery points.
type NativeDatabases struct {
	AWS         *aws.Client
	Google      *gcp.Client
	Credentials Credentials
}

func (n *NativeDatabases) NativeSnapshots(ctx context.Context, b *planning.Binding) ([]recovery.NativePoint, bool, error) {
	if b == nil || b.OrgID == "" || b.Subject != planning.ResourceSubject || (b.Provider != "rds" && b.Provider != "cloud-sql") || b.ConnectionID == "" || b.PhysicalID == "" || b.ExternalID == "" {
		return nil, false, fmt.Errorf("%w: native recovery requires a pinned supported database source", recovery.ErrInvalid)
	}
	var cfg planning.ResourceConfig
	if json.Unmarshal(b.Config, &cfg) != nil || cfg.Managed == nil {
		return nil, false, fmt.Errorf("%w: native inventory requires the source's saved managed account review", recovery.ErrInvalid)
	}
	conn, credential, err := n.Credentials.Credential(ctx, b.OrgID, b.ConnectionID)
	if err != nil {
		return nil, false, err
	}
	cloud := connections.AWS
	if b.Provider == "cloud-sql" {
		cloud = connections.GCP
	}
	if conn == nil || conn.OrgID != b.OrgID || conn.ID != b.ConnectionID || conn.Cloud != cloud {
		return nil, false, fmt.Errorf("%w: native source account changed", recovery.ErrInvalid)
	}
	if _, reviewErr := managedDatabaseBindingReview(b, conn, b.Provider); reviewErr != nil {
		return nil, false, reviewErr
	}
	if b.Provider == "cloud-sql" {
		return n.cloudSQLSnapshots(ctx, b, conn, credential, &cfg)
	}
	return n.rdsSnapshots(ctx, b, conn, credential, &cfg)
}

func (n *NativeDatabases) rdsSnapshots(ctx context.Context, b *planning.Binding, conn *connections.Connection, credential []byte, cfg *planning.ResourceConfig) ([]recovery.NativePoint, bool, error) {
	key, err := aws.ParseKey(credential)
	if err != nil {
		return nil, false, err
	}
	snapshots, complete, err := n.AWS.Session(key, b.Region, conn.Account).ListDBSnapshots(ctx, b.PhysicalID)
	if err != nil {
		return nil, false, err
	}
	points := make([]recovery.NativePoint, 0, len(snapshots))
	for _, point := range snapshots {
		if point.Engine != cfg.Kind {
			return nil, false, fmt.Errorf("%w: native snapshot engine does not match the source", recovery.ErrInvalid)
		}
		points = append(points, recovery.NativePoint{ProviderID: point.ARN, Name: point.ID, Kind: point.Type, Status: point.Status, Engine: point.Engine, Version: point.EngineVersion, CreatedAt: point.CreatedAt, Encrypted: point.Encrypted, KMSKey: point.KMSKey})
	}
	return points, complete, nil
}

func (n *NativeDatabases) cloudSQLSnapshots(ctx context.Context, b *planning.Binding, conn *connections.Connection, credential []byte, cfg *planning.ResourceConfig) ([]recovery.NativePoint, bool, error) {
	key, err := gcp.ParseKey(credential)
	if err != nil {
		return nil, false, err
	}
	if key.ProjectID != conn.Details["project_id"] {
		return nil, false, fmt.Errorf("%w: native backup credential belongs to a different project", recovery.ErrInvalid)
	}
	instance, region, err := boundSQLInstance(b.ExternalID, key.ProjectID, b.Region)
	if err != nil {
		return nil, false, err
	}
	session, err := n.Google.Session(ctx, key)
	if err != nil {
		return nil, false, err
	}
	backups, complete, err := session.ListSQLBackups(ctx, region, instance, b.PhysicalID)
	if err != nil {
		return nil, false, err
	}
	points := make([]recovery.NativePoint, 0, len(backups))
	for _, point := range backups {
		version := strings.TrimPrefix(point.DatabaseVersion, "POSTGRES_")
		if cfg.Kind != "postgres" || version == point.DatabaseVersion || version != cfg.Version {
			return nil, false, fmt.Errorf("%w: native backup engine does not match the reviewed source", recovery.ErrInvalid)
		}
		points = append(points, recovery.NativePoint{ProviderID: point.Name, Name: strings.TrimPrefix(point.Name, "projects/"+key.ProjectID+"/backups/"), Kind: point.Type, Status: point.State, Engine: cfg.Kind, Version: version, CreatedAt: point.CreatedAt, Encrypted: true, KMSKey: point.KMSKey})
	}
	return points, complete, nil
}
