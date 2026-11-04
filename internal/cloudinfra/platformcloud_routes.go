package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/platformcloud"
	"github.com/xraph/loomplane/pkg/provider"
)

// PlatformRouteHosts opens the Loomplane Cloud host a platform service runs
// on, with the scope its VM runs under there. *platformcloud.Service is one.
type PlatformRouteHosts interface {
	// Apex is the domain Loomplane Cloud names services under, or "".
	Apex(ctx context.Context) (string, error)
	Attach(ctx context.Context, orgID, projectID, envID, resource, name string) (*fc.Client, fc.Scope, *platformcloud.Placement, error)
}

var _ PlatformRouteHosts = (*platformcloud.Service)(nil)

// platformCloudService is the provider of a service Loomplane Cloud runs.
const platformCloudService = platformcloud.RuntimePlatform

// isPlatformCloud reports whether a route reaches a service Loomplane Cloud
// runs: through the ingress of the platform host its VM is on, with the
// platform's connection instead of one of the organization's.
func isPlatformCloud(b RouteBinding) bool {
	return b.BackendName() == RouteBackendFirecracker && b.ConnectionID == fc.PlatformConnection
}

// errUnverifiedPlatformDomain refuses a custom domain on a Loomplane Cloud
// service from a zone its organization hasn't verified. A platform host
// serves every workspace, so its ingress takes a name only on that proof.
var errUnverifiedPlatformDomain = platformRefusal(ErrConflict, "Add and verify this domain first.")

// errPlatformApexDomain refuses a custom domain under the platform apex,
// where Loomplane Cloud names every workspace's services itself.
var errPlatformApexDomain = platformRefusal(ErrInvalid, "That name is under Loomplane Cloud's own domain, which names services itself. Use a domain of your own.")

// checkPlatformCloudZone refuses a Loomplane Cloud route whose hostname
// isn't in a zone the route's own organization has verified, or is under
// the platform apex, whatever zone it came from. Generated hostnames never
// come here: platformcloud publishes those itself.
func (s *Service) checkPlatformCloudZone(ctx context.Context, route Route, zone *Zone) error {
	if !isPlatformCloud(route.Binding) {
		return nil
	}
	if zone == nil || zone.OrgID != route.OrgID || !zone.Verified() {
		return errUnverifiedPlatformDomain
	}
	if s.PlatformRoutes == nil {
		return errors.New("Loomplane Cloud routes aren't available here")
	}
	apex, err := s.PlatformRoutes.Apex(ctx)
	if err != nil {
		return err
	}
	apex = strings.TrimSuffix(strings.ToLower(apex), ".")
	name := strings.TrimSuffix(strings.ToLower(route.Input.Hostname), ".")
	if apex != "" && (name == apex || strings.HasSuffix(name, "."+apex)) {
		return errPlatformApexDomain
	}
	return nil
}

// A Loomplane Cloud binding is a running service on the platform Cloud with
// a running placement on a ready host. The binding records the host's public
// ingress address, which its DNS record points at anyway, and nothing else
// about the host.
var platformCloudRouteBindingQuery = `SELECT b.id,b.external_id,coalesce(ph.evidence->>'ingress_address','')
 FROM loomplane_bindings b
 JOIN cp_instances i ON i.tenant_id=b.org_id AND i.id=b.external_id` + routedService + `
 JOIN loomplane_platform_placements pl ON pl.org_id=b.org_id AND pl.environment_id=b.environment_id AND pl.resource=b.name AND pl.kind='service' AND pl.state='running' AND pl.instance_id=b.external_id
 JOIN loomplane_platform_hosts ph ON ph.id=pl.host_id AND ph.status='ready'
 WHERE b.org_id=$1 AND b.project_id=$2 AND b.environment_id=$3 AND b.name=$4 AND b.provider='` + platformCloudService + `' AND b.subject='service' AND i.state='running'
 AND coalesce(b.config->'cloud_placement'->'platform','null'::jsonb)<>'null'::jsonb AND ($5='' OR b.config->'cloud_placement'->>'cloud_id'=$5)`

func scanPlatformCloudRouteBinding(row pgx.Row) (RouteBinding, error) {
	out := RouteBinding{Backend: RouteBackendFirecracker, ConnectionID: fc.PlatformConnection, ServerID: "firecracker:" + fc.PlatformConnection}
	if err := row.Scan(&out.BindingID, &out.InstanceID, &out.Address); err != nil {
		return out, storeError(err)
	}
	if !validRouteAddress(out.Address) {
		return out, ErrInvalid
	}
	return out, nil
}

// platformRouteHost opens the platform host a Loomplane Cloud route's
// service runs on and checks it still answers on the address the route was
// reviewed with, unless the route is being removed. A service Loomplane
// Cloud no longer runs is platformcloud.ErrNotFound.
func (s *Service) platformRouteHost(ctx context.Context, route *Route, remove bool) (*fc.Client, fc.Scope, error) {
	if s.PlatformRoutes == nil {
		return nil, fc.Scope{}, errors.New("Loomplane Cloud routes aren't available here")
	}
	client, scope, _, err := s.PlatformRoutes.Attach(ctx, route.OrgID, route.ProjectID, route.Input.EnvironmentID, route.Binding.InstanceID, route.Input.Service)
	if err != nil {
		return nil, fc.Scope{}, err
	}
	if client == nil {
		return nil, fc.Scope{}, fmt.Errorf("%w: the service has no host on Loomplane Cloud", platformcloud.ErrNotFound)
	}
	if scope.Org != route.OrgID || scope.Project != route.ProjectID || scope.Environment != route.Input.EnvironmentID || scope.Resource != route.Binding.InstanceID {
		client.Close()
		return nil, fc.Scope{}, ErrConflict
	}
	caps, err := client.Check(ctx)
	if err == nil && !remove && caps.IngressAddress != route.Binding.Address {
		err = provider.ErrInfrastructureIdentity // It moved since the route was reviewed.
	}
	if err != nil {
		client.Close()
		return nil, fc.Scope{}, err
	}
	return client, scope, nil
}
