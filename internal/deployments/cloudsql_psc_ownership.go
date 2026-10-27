package deployments

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

const cloudSQLPSCOwnershipKey = "cloud_sql_psc"

var errCloudSQLPSCOwnership = errors.New("cloud sql owned endpoint does not match the saved source and provisioning review")

type cloudSQLPSCOwnership struct {
	Route     provider.CloudSQLSourceEndpoint `json:"route"`
	Version   int                             `json:"version"`
	Provision gcp.SQLPSCProvisionState        `json:"provision"`
	Endpoint  gcp.SQLPSCProvisionedEndpoint   `json:"endpoint"`
}

func ownedCloudSQLPSC(cfg *planning.ResourceConfig) bool {
	return cfg != nil && cfg.Managed != nil && cfg.Managed.CloudSQLPSC.ProvisionSubnetwork != ""
}

func (o *cloudSQLPSCOwnership) complete(project, physical string, route provider.CloudSQLSourceEndpoint) bool {
	return o != nil && o.Version == 1 && o.Route == route && (o.Provision.Version == 2 || o.Provision.Version == 3) && physical != "" &&
		o.Provision.SourceProject == project && o.Provision.SourcePhysicalID == physical &&
		o.Provision.Subnet.Route == (provider.CloudSQLApplicationNetwork{ConsumerProject: route.ConsumerProject, Network: route.Network, Subnetwork: route.ProvisionSubnetwork}) &&
		o.Provision.Address.Complete && o.Provision.ForwardingRule.Complete && o.Provision.DNS.Complete &&
		o.Endpoint.AddressID != "" && o.Endpoint.AddressID == o.Provision.Address.ResourceID &&
		o.Endpoint.ForwardingRuleID != "" && o.Endpoint.ForwardingRuleID == o.Provision.ForwardingRule.ResourceID &&
		o.Endpoint.DNSName != "" && o.Endpoint.Address != "" && o.Endpoint.ServiceAttachment != ""
}

func bindingCloudSQLPSC(cfg *planning.ResourceConfig, b *planning.Binding, project string) (*cloudSQLPSCOwnership, error) {
	var raw json.RawMessage
	if b != nil {
		raw = b.OwnedResources[cloudSQLPSCOwnershipKey]
	}
	if !ownedCloudSQLPSC(cfg) {
		if len(raw) != 0 {
			return nil, errCloudSQLPSCOwnership
		}
		return nil, nil
	}
	if b == nil {
		return nil, nil
	}
	var kept cloudSQLPSCOwnership
	if len(raw) == 0 || decodeDatabaseCheckpoint(raw, &kept) != nil || !kept.complete(project, b.PhysicalID, cfg.Managed.CloudSQLPSC) {
		return nil, errCloudSQLPSCOwnership
	}
	return &kept, nil
}

func (s *cloudSQLState) restorePSC(cfg *planning.ResourceConfig, b *planning.Binding, resumed bool) error {
	kept, err := bindingCloudSQLPSC(cfg, b, s.Scope.Project)
	if err != nil {
		return err
	}
	if !ownedCloudSQLPSC(cfg) {
		if s.PSC != nil || s.PSCCleanup != nil || s.Version == 4 {
			return errCloudSQLPSCOwnership
		}
		return nil
	}
	if !cfg.Managed.CloudSQLPSC.Valid(s.Scope.Region) || s.Scope.Mode == "remove" && kept == nil {
		return errCloudSQLPSCOwnership
	}
	if resumed {
		if s.Version != 4 || s.PSC == nil || s.PSC.Version != 1 || s.PSC.Route != cfg.Managed.CloudSQLPSC {
			return errCloudSQLPSCOwnership
		}
		if kept != nil && *kept != *s.PSC {
			return errCloudSQLPSCOwnership
		}
	} else {
		s.Version = 4
		s.PSC = &cloudSQLPSCOwnership{Version: 1, Route: cfg.Managed.CloudSQLPSC}
		if kept != nil {
			*s.PSC = *kept
		}
	}
	if s.Scope.Mode == "apply" && s.PSCCleanup != nil || s.Scope.Mode == "remove" && s.PSCCleanup != nil && s.Stage != "removed" {
		return errCloudSQLPSCOwnership
	}
	if s.PSC.Provision != (gcp.SQLPSCProvisionState{}) && (s.ResourceID == "" || s.PSC.Provision.SourceProject != s.Scope.Project || s.PSC.Provision.SourcePhysicalID != s.ResourceID || (s.Stage == "instance" && kept == nil)) {
		return errCloudSQLPSCOwnership
	}
	if s.Stage == "ready" || s.Scope.Mode == "remove" {
		if !s.PSC.complete(s.Scope.Project, s.ResourceID, cfg.Managed.CloudSQLPSC) {
			return errCloudSQLPSCOwnership
		}
	}
	return nil
}

func cloudSQLProvisionSpec(cfg *planning.ResourceConfig, instance, physical string) gcp.SQLPSCProvisionSpec {
	return gcp.SQLPSCProvisionSpec{Source: cloudSQLPITRSourceSpec(cfg, instance), PhysicalID: physical, Endpoint: cfg.Managed.CloudSQLPSC, Subnetwork: cfg.Managed.CloudSQLPSC.ProvisionSubnetwork}
}

// pscCheck refreshes the activity checkpoint before provider mutations. The
// checkpoint callback enforces the worker lease, even after SQL has been removed.
func (s *cloudSQLState) pscCheck(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.save()
}

func (s *cloudSQLState) ensurePSC(ctx context.Context, client *gcp.Session, cfg *planning.ResourceConfig) error {
	if s.PSC == nil {
		return nil
	}
	spec := cloudSQLProvisionSpec(cfg, s.Scope.Instance, s.ResourceID)
	owned := s.PSC.Provision
	endpoint, err := client.EnsureSQLPSCSourceEndpoint(ctx, spec, &owned, func(next gcp.SQLPSCProvisionState) error {
		updated := *s.PSC
		updated.Provision = next
		s.PSC = &updated
		return s.save()
	}, s.pscCheck)
	if err != nil {
		return err
	}
	if s.PSC.Endpoint != (gcp.SQLPSCProvisionedEndpoint{}) && s.PSC.Endpoint != *endpoint {
		return errCloudSQLPSCOwnership
	}
	s.PSC.Endpoint = *endpoint
	return s.save()
}

func (o *cloudSQLPSCOwnership) verify(ctx context.Context, client *gcp.Session, cfg *planning.ResourceConfig, instance, physical string, check func(context.Context) error) error {
	if o == nil {
		return nil
	}
	if !o.complete(client.Key().ProjectID, physical, cfg.Managed.CloudSQLPSC) {
		return errCloudSQLPSCOwnership
	}
	snapshot := o.Provision
	endpoint, err := client.EnsureSQLPSCSourceEndpoint(ctx, cloudSQLProvisionSpec(cfg, instance, physical), &snapshot, func(gcp.SQLPSCProvisionState) error { return errCloudSQLPSCOwnership }, check)
	if err != nil {
		return err
	}
	if *endpoint != o.Endpoint {
		return errCloudSQLPSCOwnership
	}
	return nil
}

func (s *cloudSQLState) ownership(b *planning.Binding) (string, error) {
	if s.PSC == nil {
		return "", nil
	}
	all := map[string]json.RawMessage{}
	if b != nil {
		for key, value := range b.OwnedResources {
			all[key] = value
		}
	}
	raw, err := json.Marshal(s.PSC)
	if err != nil {
		return "", err
	}
	all[cloudSQLPSCOwnershipKey] = raw
	raw, err = json.Marshal(all)
	return string(raw), err
}
