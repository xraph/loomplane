package deployments

import (
	"context"
	"errors"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/nativepg"
)

type cloudSQLPSCCredentialError struct{ cause error }

func (cloudSQLPSCCredentialError) Error() string   { return "Cloud SQL source credential update failed" }
func (e cloudSQLPSCCredentialError) Unwrap() error { return e.cause }

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
