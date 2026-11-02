package deployments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

type cloudRunPSCState struct {
	Scope   string                           `json:"scope"`
	Subnet  gcp.SQLApplicationNetwork        `json:"subnet"`
	Sources map[string]gcp.SQLSourceEndpoint `json:"sources"`
}

type cloudRunPSCObserver interface {
	ReadSQLApplicationNetwork(context.Context, string, provider.CloudSQLApplicationNetwork, func(context.Context) error) (*gcp.SQLApplicationNetwork, error)
	ReadSQLPITRSourceEndpoint(context.Context, gcp.SQLInstanceSpec, string, provider.CloudSQLSourceEndpoint, func(context.Context) error) (*gcp.SQLSourceEndpoint, error)
}

func (p *cloudRunPSC) scope(job *Job) string {
	type sourceIdentity struct {
		Binding, Name, External, Physical, Connection, Region, Kind, Version string
		Managed                                                              planning.ManagedDatabaseConfig
	}
	sources := make([]sourceIdentity, 0, len(p.sources))
	for _, source := range p.sources {
		b, cfg := source.Binding, source.Config
		sources = append(sources, sourceIdentity{b.ID, b.Name, b.ExternalID, b.PhysicalID, b.ConnectionID, b.Region, cfg.Kind, cfg.Version, *cfg.Managed})
	}
	raw, _ := json.Marshal(struct { //nolint:errcheck // identity consists of JSON-safe scalar structs and slices
		Deployment, Org, Project, Environment, Plan, Revision, Action, Connection string
		Route                                                                     provider.CloudSQLApplicationNetwork
		Sources                                                                   []sourceIdentity
	}{job.Deployment.ID, job.Deployment.OrgID, job.Deployment.ProjectID, job.Deployment.EnvironmentID, job.Plan.ID, job.Plan.Revision, job.Action.ID, job.Connection.ID, p.transport.Network, sources})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// check fences a build or rollout with the saved route and source identities.
// Checkpoints carry observations, never the application's database credentials.
func (p *cloudRunPSC) check(ctx context.Context, observer cloudRunPSCObserver, job *Job, state *cloudRunState, save func() error) error {
	scope := p.scope(job)
	if state.PSC == nil && (state.BuildID != "" || state.Image != "" || state.LogAttempt != "") || state.PSC != nil && state.PSC.Scope != scope {
		return errors.New("cloud sql PSC application checkpoint does not match this deployment review")
	}
	guard := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if save == nil {
			return errors.New("cloud sql PSC application checkpoint is unavailable")
		}
		return save()
	}
	if err := guard(ctx); err != nil {
		return err
	}
	subnet, err := observer.ReadSQLApplicationNetwork(ctx, job.Action.Region, p.transport.Network, guard)
	if err != nil {
		return err
	}
	if subnet == nil {
		return errCloudRunPSCReview
	}
	observed := &cloudRunPSCState{Scope: scope, Subnet: *subnet, Sources: map[string]gcp.SQLSourceEndpoint{}}
	for _, source := range p.sources {
		name, _, bindingErr := boundSQLInstance(source.Binding.ExternalID, job.Connection.Details["project_id"], job.Action.Region)
		if bindingErr != nil {
			return bindingErr
		}
		endpoint, endpointErr := observer.ReadSQLPITRSourceEndpoint(ctx, cloudSQLPITRSourceSpec(&source.Config, name), source.Binding.PhysicalID, source.Config.Managed.CloudSQLPSC, guard)
		if endpointErr != nil {
			return endpointErr
		}
		if endpoint == nil {
			return errCloudRunPSCReview
		}
		observed.Sources[source.Binding.ExternalID] = *endpoint
	}
	fresh, err := observer.ReadSQLApplicationNetwork(ctx, job.Action.Region, p.transport.Network, guard)
	if err != nil {
		return err
	}
	if fresh == nil || *fresh != *subnet || state.PSC != nil && !reflect.DeepEqual(state.PSC, observed) {
		return errors.New("cloud sql PSC application network or source changed; review the deployment again")
	}
	state.PSC = observed
	return guard(ctx)
}
