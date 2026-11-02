package cloudinfra

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
	fc "github.com/xraph/loomplane/internal/firecracker"
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
func (s *Service) executeFirecrackerRoute(ctx context.Context, st DomainStore, op *RouteOperation, zone *Zone) error {
	if op.Kind != "apply" && op.Kind != "verify" && op.Kind != "remove" {
		return &provider.UnsupportedCapabilityError{Provider: connections.Firecracker, Capability: provider.IngressManagement}
	}
	route := &op.Route
	if isWildcard(route.Input.Hostname) {
		return errors.New("firecracker managed TLS currently requires an exact hostname")
	}
	conn, raw, err := s.Connections.Credential(ctx, route.OrgID, route.Binding.ConnectionID)
	if err != nil {
		return err
	}
	if conn.Cloud != connections.Firecracker || conn.Status != connections.StatusReady || conn.Generation != route.Binding.ConnectionGeneration {
		return ErrConflict
	}
	client, err := fc.NewClient(raw, conn.Details)
	if err != nil {
		return err
	}
	defer client.Close()
	caps, err := client.Check(ctx)
	if err != nil {
		return err
	}
	if caps.RuntimeIdentity != route.Binding.RuntimeID || caps.IngressAddress != route.Binding.Address {
		return provider.ErrInfrastructureIdentity
	}
	q := fc.Request{Scope: fc.Scope{Org: route.OrgID, Connection: route.Binding.ConnectionID, Project: route.ProjectID, Environment: route.Input.EnvironmentID, Resource: route.Binding.InstanceID}, RuntimeIdentity: route.Binding.RuntimeID, Authority: fc.Authority{Generation: op.Generation, Operation: op.ID, Expires: s.now().Add(2 * time.Minute)}, Route: &fc.RouteSpec{ID: route.ID, Hostname: route.Input.Hostname, Port: route.Input.Port, BlockedPaths: route.Input.BlockedPathPrefixes, Remove: op.Kind == "remove"}}
	if _, err = client.Route(ctx, q); err != nil {
		return err
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
