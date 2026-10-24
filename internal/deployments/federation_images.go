package deployments

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/xraph/ctrlplane/instance"

	"github.com/xraph/loomplane/internal/clouds/registry"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/planning"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// LoadFederationImageProofs reads from the registry retained in the committed
// service build target and current instance. Callers must authorize/load these
// records, load the trusted controller profile and recheck live ownership before
// issuance. Reported image metadata selects content digests, never credentials.
func LoadFederationImageProofs(ctx context.Context, credentials Credentials, b *planning.Binding, inst *instance.Instance, reported, operatingSystem, architecture string) ([][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deployment, _, err := federationWorkloadInstance(b, inst)
	if err != nil {
		return nil, err
	}
	if _, ok := domain.ResolveImageSelector(deployment.Image, reported, operatingSystem, architecture, nil); ok {
		return nil, nil
	}
	if !domain.ValidPinnedImage(reported) {
		return nil, federation.ErrUnverified
	}
	var cfg planning.ServiceConfig
	var instanceSettings map[string]string
	if credentials == nil || json.Unmarshal(b.Config, &cfg) != nil || cfg.BuildTarget == nil || !domain.ValidIdentitySegment(cfg.BuildTarget.Registry) || cfg.BuildTarget.Registry != inst.Labels[labelRegistry] || json.Unmarshal([]byte(inst.Labels[labelRegistrySettings]), &instanceSettings) != nil || !reflect.DeepEqual(cfg.BuildTarget.RegistrySettings, instanceSettings) {
		return nil, federation.ErrUnverified
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	connection, raw, err := credentials.Credential(bounded, b.OrgID, cfg.BuildTarget.Registry)
	defer clear(raw)
	if err != nil {
		return nil, err
	}
	if connection == nil || connection.OrgID != b.OrgID || connection.ID != cfg.BuildTarget.Registry || connection.Cloud != connections.Registry || (connection.Status != connections.StatusReady && !connections.RegistryPullReady(connection)) {
		return nil, federation.ErrUnverified
	}
	registryConfig, err := registry.ParseConfig(raw, connection.Details)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(cfg.BuildTarget.RegistrySettings, planning.ReviewedConnectionSettings(connection.Cloud, registryConfig.Details)) {
		return nil, federation.ErrUnverified
	}
	client := registry.New(registryConfig)
	defer client.HTTP.CloseIdleConnections()
	return domain.CollectImageIndexes(bounded, deployment.Image, reported, operatingSystem, architecture, client.Manifest)
}
