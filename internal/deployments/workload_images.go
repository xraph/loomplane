package deployments

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/xraph/loomplane/internal/clouds/registry"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

func verifyPodGroupImages(ctx context.Context, main, architecture string, group *provider.PodGroup, verify func(context.Context, string, string, string) error) error {
	if group == nil {
		return nil
	}
	if verify == nil || (architecture != "arm64" && architecture != "amd64") {
		return provider.ErrWorkloadSpec
	}
	if err := provider.ValidatePodGroup(group); err != nil {
		return err
	}
	if err := provider.CheckPodGroupArchitecture(group, architecture); err != nil {
		return err
	}
	if err := verify(ctx, main, "linux", architecture); err != nil {
		return err
	}
	for _, sidecar := range group.Sidecars {
		if err := verify(ctx, sidecar.Image, "linux", architecture); err != nil {
			return err
		}
	}
	return nil
}
func (b *ImageBuilder) verifyPodGroup(ctx context.Context, job *Job, state *ImageArtifact) error {
	cfg := job.Action.Service
	if cfg.PodGroup == nil {
		return nil
	}
	if cfg.Provider != connections.Kubernetes || cfg.WorkloadCapabilityRevision != provider.PodGroupCapabilityRevision || cfg.BuildTarget == nil || cfg.BuildTarget.Registry == "" {
		return provider.ErrWorkloadSpec
	}
	architecture := cfg.TargetSettings["architecture"]
	if cfg.ClusterDemand != nil && cfg.ClusterDemand.Profile != nil {
		architecture = cfg.ClusterDemand.Profile.Architecture
	}
	if architecture == "" {
		architecture = state.Architecture
	}
	target := cfg.BuildTarget
	connection, raw, err := b.connection(ctx, job, target.Registry, connections.Registry, target.RegistrySettings, true)
	defer clear(raw)
	if err != nil {
		return err
	}
	config, err := registry.ParseConfig(raw, connection.Details)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(target.RegistrySettings, planning.ReviewedConnectionSettings(connection.Cloud, config.Details)) {
		return errors.New("registry trust changed since review")
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := registry.New(config)
	defer client.HTTP.CloseIdleConnections()
	if checkErr := verifyPodGroupImages(bounded, state.Reference, architecture, cfg.PodGroup, client.VerifyPlatform); checkErr != nil {
		return checkErr
	}
	state.Architecture = architecture
	return nil
}
