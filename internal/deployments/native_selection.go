package deployments

import (
	"context"
	"fmt"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
)

// CheckNativeSnapshot shares inventory's original account review, then rereads
// the selected backup itself. No current source instance is needed.
func (n *NativeDatabases) CheckNativeSnapshot(ctx context.Context, b *planning.Binding, selected recovery.NativePoint) (*recovery.NativePoint, error) {
	if selected.Kind == "pitr" {
		return n.NativePITRWindow(ctx, b)
	}
	cfg, conn, credential, err := n.nativeSourceAccess(ctx, b)
	if err != nil {
		return nil, err
	}
	if selected.Engine != cfg.Kind || strings.Split(selected.Version, ".")[0] != cfg.Version {
		return nil, fmt.Errorf("%w: selected native point engine does not match the reviewed source", recovery.ErrInvalid)
	}
	if b.Provider == "rds" {
		key, parseErr := aws.ParseKey(credential)
		if parseErr != nil {
			return nil, parseErr
		}
		point, readErr := n.AWS.Session(key, b.Region, conn.Account).ReadDBSnapshot(ctx, b.PhysicalID, aws.DBSnapshot{ID: selected.Name, ARN: selected.ProviderID, InstanceID: b.ExternalID, ResourceID: b.PhysicalID, Type: selected.Kind, Engine: selected.Engine, EngineVersion: selected.Version, CreatedAt: selected.CreatedAt, Encrypted: selected.Encrypted, KMSKey: selected.KMSKey})
		if readErr != nil {
			return nil, readErr
		}
		fresh := nativeRDSPoint(*point)
		return &fresh, nil
	}
	key, err := gcp.ParseKey(credential)
	if err != nil {
		return nil, err
	}
	if key.ProjectID != conn.Details["project_id"] {
		return nil, fmt.Errorf("%w: native backup credential belongs to a different project", recovery.ErrInvalid)
	}
	instance, region, err := boundSQLInstance(b.ExternalID, key.ProjectID, b.Region)
	if err != nil {
		return nil, err
	}
	if !selected.Encrypted || selected.ProviderID != "projects/"+key.ProjectID+"/backups/"+selected.Name || selected.Version != cfg.Version {
		return nil, fmt.Errorf("%w: selected cloud sql point does not match its retained reference", recovery.ErrInvalid)
	}
	session, err := n.Google.Session(ctx, key)
	if err != nil {
		return nil, err
	}
	point, err := session.ReadSQLBackup(ctx, region, instance, b.PhysicalID, gcp.SQLBackup{Name: selected.ProviderID, Type: selected.Kind, DatabaseVersion: "POSTGRES_" + selected.Version, CreatedAt: selected.CreatedAt, KMSKey: selected.KMSKey})
	if err != nil {
		return nil, err
	}
	fresh := nativeSQLPoint(*point, key.ProjectID, cfg.Kind, selected.Version)
	return &fresh, nil
}
