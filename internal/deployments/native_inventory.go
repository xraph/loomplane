package deployments

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
)

// NativeDatabases reads provider metadata for the original reviewed source.
// It never substitutes Trove exports for provider-native recovery points.
type NativeDatabases struct {
	AWS         *aws.Client
	Credentials Credentials
}

func (n *NativeDatabases) NativeSnapshots(ctx context.Context, b *planning.Binding) ([]recovery.NativePoint, bool, error) {
	if b == nil || b.OrgID == "" || b.Subject != planning.ResourceSubject || b.Provider != "rds" || b.ConnectionID == "" || b.PhysicalID == "" || b.ExternalID == "" {
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
	if conn == nil || conn.OrgID != b.OrgID || conn.ID != b.ConnectionID || conn.Cloud != connections.AWS {
		return nil, false, fmt.Errorf("%w: native source account changed", recovery.ErrInvalid)
	}
	if _, reviewErr := managedDatabaseBindingReview(b, conn, "rds"); reviewErr != nil {
		return nil, false, reviewErr
	}
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
