package cloudinfra

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/platformcloud"
	"github.com/xraph/loomplane/pkg/provider"
)

// The host switches a resource's current VM only after guest readiness. Routes
// retain that resource identity across same-host releases.
var firecrackerRouteBindingQuery = `SELECT b.id,c.id,c.generation,b.external_id,coalesce(c.details->>'runtime_identity',''),coalesce(c.details->>'ingress_address','')
 FROM loomplane_bindings b JOIN loomplane_cloud_connections c ON c.org_id=b.org_id AND c.id=b.connection_id AND c.cloud='firecracker' AND c.status='ready'
 JOIN cp_instances i ON i.tenant_id=b.org_id AND i.id=b.external_id` + routedService + `
 WHERE b.org_id=$1 AND b.project_id=$2 AND b.environment_id=$3 AND b.name=$4 AND b.provider='firecracker' AND b.subject='service' AND $5='' AND i.state='running'
 AND coalesce(b.config->'cloud_placement','null'::jsonb)='null'::jsonb`

func scanFirecrackerRouteBinding(row pgx.Row) (RouteBinding, error) {
	out := RouteBinding{Backend: RouteBackendFirecracker}
	err := row.Scan(&out.BindingID, &out.ConnectionID, &out.ConnectionGeneration, &out.InstanceID, &out.RuntimeID, &out.Address)
	if err != nil {
		return out, storeError(err)
	}
	out.ServerID = "firecracker:" + out.ConnectionID
	if len(out.RuntimeID) != 64 || !validRouteAddress(out.Address) {
		return out, ErrInvalid
	}
	return out, nil
}

// firecrackerRouteHost opens the host a Firecracker route's service runs on
// and names the scope its VM runs under there: the organization's connection
// to its own host, or, for a Loomplane Cloud service, the platform host its
// placement is on. The host must still be the one the route was reviewed
// with, except that a Loomplane Cloud service that has moved since has its
// route removed from the host it is on now.
func (s *Service) firecrackerRouteHost(ctx context.Context, route *Route, remove bool) (*fc.Client, fc.Scope, error) {
	if isPlatformCloud(route.Binding) {
		return s.platformRouteHost(ctx, route, remove)
	}
	conn, raw, err := s.Connections.Credential(ctx, route.OrgID, route.Binding.ConnectionID)
	if err != nil {
		return nil, fc.Scope{}, err
	}
	if conn.Cloud != connections.Firecracker || conn.Status != connections.StatusReady || conn.Generation != route.Binding.ConnectionGeneration {
		return nil, fc.Scope{}, ErrConflict
	}
	client, err := fc.NewClient(raw, conn.Details)
	if err != nil {
		return nil, fc.Scope{}, err
	}
	caps, err := client.Check(ctx)
	if err == nil && (caps.RuntimeIdentity != route.Binding.RuntimeID || caps.IngressAddress != route.Binding.Address) {
		err = provider.ErrInfrastructureIdentity
	}
	if err != nil {
		client.Close()
		return nil, fc.Scope{}, err
	}
	return client, fc.Scope{Org: route.OrgID, Connection: route.Binding.ConnectionID, Project: route.ProjectID, Environment: route.Input.EnvironmentID, Resource: route.Binding.InstanceID}, nil
}

func (s *Service) executeFirecrackerRoute(ctx context.Context, st DomainStore, op *RouteOperation, zone *Zone) error {
	if op.Kind != "apply" && op.Kind != "verify" && op.Kind != "remove" {
		return &provider.UnsupportedCapabilityError{Provider: connections.Firecracker, Capability: provider.IngressManagement}
	}
	route := &op.Route
	if isWildcard(route.Input.Hostname) {
		return errors.New("firecracker managed TLS currently requires an exact hostname")
	}
	if op.Kind != "remove" {
		if err := s.checkPlatformCloudZone(ctx, *route, zone); err != nil {
			return err
		}
	}
	client, scope, err := s.firecrackerRouteHost(ctx, route, op.Kind == "remove")
	switch {
	case op.Kind == "remove" && isPlatformCloud(route.Binding) && errors.Is(err, platformcloud.ErrNotFound):
		// The service is gone from Loomplane Cloud, and with it the VM the
		// host routed the name to. Only its DNS is left to remove.
	case err != nil:
		return err
	default:
		defer client.Close()
		q := fc.Request{Scope: scope, RuntimeIdentity: client.Details["runtime_identity"], Authority: fc.Authority{Generation: op.Generation, Operation: op.ID, Expires: s.now().Add(2 * time.Minute)}, Route: &fc.RouteSpec{ID: route.ID, Hostname: route.Input.Hostname, Port: route.Input.Port, BlockedPaths: route.Input.BlockedPathPrefixes, Remove: op.Kind == "remove"}}
		if _, err = client.Route(ctx, q); err != nil {
			return err
		}
	}
	if op.Kind == "remove" {
		if err := s.applyRouteDNS(ctx, st, op, zone); err != nil {
			return err
		}
		route.Status = "removed"
		route.UpdatedAt = s.now().UTC()
		return st.PublishRoute(ctx, op)
	}
	if op.Kind == "apply" && op.Receipt == nil {
		if err := s.applyRouteDNS(ctx, st, op, zone); err != nil {
			return err
		}
	}
	route.Verification = s.verifyKubernetesRoute(ctx, *route, kubernetes.RouteIngressStatus{Addresses: []string{route.Binding.Address}, BackendReady: true})
	route.Status = "waiting"
	if route.Verification.DNS && route.Verification.TLS && route.Verification.Backend {
		route.Status = "ready"
		route.Verification.CertState = "ready"
	}
	route.UpdatedAt = s.now().UTC()
	if err := st.PublishRoute(ctx, op); err != nil {
		return err
	}
	if route.Status != "ready" {
		return ErrPending
	}
	return nil
}
