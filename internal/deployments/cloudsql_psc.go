package deployments

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/nativepg"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

type cloudSQLPSCCredentialError struct{ cause error }

func (cloudSQLPSCCredentialError) Error() string   { return "Cloud SQL source credential update failed" }
func (e cloudSQLPSCCredentialError) Unwrap() error { return e.cause }

func checkCloudRunSQLTransport(bindings []*planning.Binding) error {
	for _, binding := range bindings {
		if binding.Provider != "cloud-sql" {
			continue
		}
		var cfg planning.ResourceConfig
		if len(binding.Config) == 0 {
			continue // Legacy bindings predate saved resource configuration.
		}
		if err := json.Unmarshal(binding.Config, &cfg); err != nil {
			return errors.New("cloud sql attachment configuration cannot be read")
		}
		if cfg.Managed != nil && (cfg.Managed.CloudSQLPSC != (provider.CloudSQLSourceEndpoint{}) || cfg.Managed.Profile.Access == "cloud-sql-psc-connector") {
			return errors.New("cloud sql PSC application transport is not available yet")
		}
	}
	return nil
}

func (c *CloudSQL) verifyPSCSource(ctx context.Context, client *gcp.Session, job *Job, state *cloudSQLState, spec gcp.SQLInstanceSpec, expected *gcp.SQLSourceEndpoint, database, password string) error {
	guard := state.guarded(client, job.Binding == nil).Check
	check := func(ctx context.Context) error {
		observed, err := client.ReadSQLPITRSourceEndpoint(ctx, spec, state.ResourceID, job.Action.Resource.Managed.CloudSQLPSC, guard)
		if err != nil {
			return err
		}
		if expected == nil || *observed != *expected {
			return errors.New("cloud sql source endpoint changed during verification")
		}
		return nil
	}
	if err := check(ctx); err != nil {
		return err
	}
	factory := c.PSCConnector
	if factory == nil {
		factory = func(ctx context.Context, s *gcp.Session, region, instance string) (gcp.SQLConnector, error) {
			return s.PostgresPSCConnector(ctx, region, instance)
		}
	}
	connector, err := factory(ctx, client, spec.Region, spec.Name)
	if err != nil {
		return err
	}
	if connector == nil {
		return errors.New("cloud sql PSC transport is unavailable")
	}
	defer func() { _ = connector.Close() }()
	verifier := &nativepg.Verifier{Dial: connector.DialContext}
	_, err = verifier.Probe(ctx, nativepg.Spec{Database: database, AdminUser: sqlUser, AdminPassword: password, MajorVersion: 17}, check)
	return err
}
