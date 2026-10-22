package cloudinfra

import (
	"context"
	"net"
	"slices"
	"time"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type RouteInput struct {
	ExternalTTLSeconds int    `json:"external_ttl_seconds,omitempty"`
	ZoneID             string `json:"zone_id"`
	CloudID            string `json:"cloud_id"`
	EnvironmentID      string `json:"environment_id"`
	Service            string `json:"service"`
	Hostname           string `json:"hostname"`
	Port               int    `json:"port"`
}

type RouteBinding struct {
	RuntimeID            string `json:"runtime_id"`
	BindingID            string `json:"binding_id"`
	ConfigDigest         string `json:"config_digest"`
	ServerID             string `json:"server_id"`
	TargetID             string `json:"target_id"`
	ConnectionID         string `json:"connection_id"`
	ConnectionGeneration int64  `json:"connection_generation"`
	InstanceID           string `json:"instance_id"`
	ReleaseID            string `json:"release_id"`
	ContainerID          string `json:"container_id"`
	Address              string `json:"address"`
	Backend              string `json:"backend,omitempty"` // empty reads as server
}

type Route struct {
	ID           string              `json:"id" forge:"id"`
	OrgID        string              `json:"org_id"`
	ProjectID    string              `json:"project_id"`
	Input        RouteInput          `json:"input"`
	Binding      RouteBinding        `json:"binding"`
	Status       string              `json:"status"`
	OperationID  string              `json:"operation_id"`
	Receipt      *provider.DNSRecord `json:"receipt,omitempty"`
	Verification RouteVerification   `json:"verification"`
	Revision     int64               `json:"revision"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

type RouteOperation struct {
	CreatedAt        time.Time               `json:"created_at"`
	Source           *Route                  `json:"source,omitempty"`
	HandoffID        string                  `json:"handoff_id,omitempty"`
	NotBefore        time.Time               `json:"not_before"`
	CancelledIngress *IngressTransition      `json:"-"`
	SourceTransition *IngressTransition      `json:"-"`
	ID               string                  `json:"id" forge:"id"`
	Route            Route                   `json:"route"`
	Kind             string                  `json:"kind"`
	Status           string                  `json:"status"`
	Message          string                  `json:"message"`
	Review           string                  `json:"review"`
	ExpiresAt        time.Time               `json:"expires_at"`
	Attempt          int                     `json:"attempt"`
	Revision         int64                   `json:"revision"`
	Generation       int64                   `json:"generation"`
	DNSAttempted     bool                    `json:"dns_attempted"`
	DesiredRecord    provider.DNSRecord      `json:"desired_record"`
	PreviousReceipt  *provider.DNSRecord     `json:"previous_receipt,omitempty"`
	Receipt          *provider.DNSRecord     `json:"receipt,omitempty"`
	DesiredConfig    *server.IngressConfig   `json:"-"`
	PreviousConfig   *server.IngressSnapshot `json:"-"`
	AppliedConfig    *server.IngressSnapshot `json:"-"`
	Principal        security.Principal      `json:"-"`
}

type IngressState struct {
	Generation int64                   `json:"generation"`
	Revision   int64                   `json:"revision"`
	Config     server.IngressConfig    `json:"config"`
	Current    *server.IngressSnapshot `json:"current"`
	Previous   *server.IngressSnapshot `json:"previous"`
}

type RouteWorkflow interface {
	StartRoute(context.Context, string, string, int) error
}

type DomainStore interface {
	DomainProject(context.Context, string, string) error
	CreateZone(context.Context, *Zone) error
	Zone(context.Context, string, string) (*Zone, error)
	Zones(context.Context, string) ([]*Zone, error)
	SaveZone(context.Context, *Zone) error
	RemoveZone(context.Context, string, string, int64) error
	ResolveRouteBinding(context.Context, string, string, RouteInput) (RouteBinding, error)
	PreviewRoute(context.Context, *RouteOperation) error
	AcceptRoute(context.Context, *RouteOperation) error
	RouteOperation(context.Context, string, string) (*RouteOperation, error)
	RouteOperations(context.Context, string, string, string) ([]*RouteOperation, error)
	SaveRouteOperation(context.Context, *RouteOperation) error
	Route(context.Context, string, string, string) (*Route, error)
	Routes(context.Context, string, string) ([]*Route, error)
	PublishRoute(context.Context, *RouteOperation) error
	BeginIngress(context.Context, *RouteOperation) (*IngressState, error)
	BeginServerIngress(context.Context, string, string) (*IngressState, error)
	SaveServerIngress(context.Context, string, string, *IngressState) error
	ValidateRetainedRoute(context.Context, Route) error
	ValidateRouteCleanup(context.Context, Route) error
	CompleteHandoff(context.Context, *RouteOperation) error
	SaveIngress(context.Context, *RouteOperation, *IngressState) error
	PendingRoutes(context.Context) ([]*RouteOperation, error)
	// OrgRoutes lists every route of an organization that isn't removed.
	OrgRoutes(ctx context.Context, org string) ([]*Route, error)
	// ServiceRoutes lists one service's routes that aren't removed.
	ServiceRoutes(ctx context.Context, org, project, environment, service string) ([]*Route, error)
	// LatestRouteOperations maps each route ID to its newest operation.
	LatestRouteOperations(ctx context.Context, org string, routeIDs []string) (map[string]*RouteOperation, error)
	ProjectNames(ctx context.Context, org string) (map[string]string, error)
	// RuntimeNames maps server IDs and connection IDs to display names.
	RuntimeNames(ctx context.Context, org string) (map[string]string, error)
}

func (s *Service) PreviewRoute(ctx context.Context, p security.Principal, project string, in RouteInput) (*RouteOperation, error) {
	if err := s.domainScope(ctx, p, project, true); err != nil {
		return nil, err
	}
	if project == "" {
		return nil, ErrInvalid
	}
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	in.Hostname, err = NormalizeHostname(in.Hostname)
	if err != nil || in.Port < 1 || in.Port > 65535 || (in.ExternalTTLSeconds != 0 && (in.ExternalTTLSeconds < 60 || in.ExternalTTLSeconds > 86400)) {
		return nil, ErrInvalid
	}
	zone, err := st.Zone(ctx, p.OrgID, in.ZoneID)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(zone.ProjectIDs, project) || zone.VerifiedAt.IsZero() {
		return nil, ErrNotFound
	}
	if checkErr := s.verifyZoneControl(ctx, zone); checkErr != nil {
		return nil, checkErr
	}
	binding, err := st.ResolveRouteBinding(ctx, p.OrgID, project, in)
	if err != nil {
		return nil, err
	}
	id, err := randomID("route")
	if err != nil {
		return nil, err
	}
	record, err := routeDNSRecord(zone, in.Hostname, binding.Address, id)
	if err != nil {
		return nil, err
	}
	if checkErr := s.preflightRouteDNS(ctx, zone, record); checkErr != nil {
		return nil, checkErr
	}
	opID, err := randomID("routeop")
	if err != nil {
		return nil, err
	}
	review, err := reviewToken()
	if err != nil {
		return nil, err
	}
	op := &RouteOperation{ID: opID, Kind: "apply", Status: "preview", Review: review, ExpiresAt: s.now().UTC().Add(15 * time.Minute), Revision: 1, Principal: p,
		Route: Route{ID: id, OrgID: p.OrgID, ProjectID: project, Input: in, Binding: binding, Status: "pending", OperationID: opID, Revision: 1, UpdatedAt: s.now().UTC()}, DesiredRecord: record}
	if checkErr := st.PreviewRoute(ctx, op); checkErr != nil {
		return nil, checkErr
	}
	return op, nil
}

func (s *Service) routeOperation(ctx context.Context, p security.Principal, project, id string, write bool) (DomainStore, *RouteOperation, error) {
	if err := s.domainScope(ctx, p, project, write); err != nil {
		return nil, nil, err
	}
	if project == "" {
		return nil, nil, ErrInvalid
	}
	st, err := s.domainStore()
	if err != nil {
		return nil, nil, err
	}
	op, err := st.RouteOperation(ctx, p.OrgID, id)
	if err != nil {
		return nil, nil, err
	}
	if op.Route.ProjectID != project {
		return nil, nil, ErrNotFound
	}
	return st, op, nil
}

func (s *Service) ApplyRoute(ctx context.Context, p security.Principal, project, id string, in ApplyInput) (*RouteOperation, error) {
	st, op, err := s.routeOperation(ctx, p, project, id, true)
	if err != nil {
		return nil, err
	}
	if in.Review != op.Review {
		return nil, ErrConflict
	}
	if op.Status != "preview" {
		return op, nil
	}
	if !s.now().Before(op.ExpiresAt) {
		return nil, ErrConflict
	}
	zone, err := st.Zone(ctx, p.OrgID, op.Route.Input.ZoneID)
	if err != nil {
		return nil, err
	}
	if checkErr := s.verifyZoneControl(ctx, zone); checkErr != nil {
		return nil, checkErr
	}
	op.Status, op.Attempt = "pending", 1
	if checkErr := st.AcceptRoute(ctx, op); checkErr != nil {
		return nil, checkErr
	}
	s.domainAudit(ctx, p, "domain.route."+op.Kind, op.ID, op.Route.ProjectID)
	s.enqueueRoute(ctx, op)
	return op, nil
}

func (s *Service) enqueueRoute(ctx context.Context, op *RouteOperation) {
	if workflow, ok := s.Workflow.(RouteWorkflow); ok {
		if err := workflow.StartRoute(ctx, op.Route.OrgID, op.ID, op.Attempt); err != nil {
			s.domainAudit(ctx, op.Principal, "domain.route.enqueue_pending", op.ID, op.Route.ProjectID)
		}
	}
}

func (s *Service) RetryRoute(ctx context.Context, p security.Principal, project, id string) (*RouteOperation, error) {
	st, op, err := s.routeOperation(ctx, p, project, id, true)
	if err != nil {
		return nil, err
	}
	if op.Status != "failed" && op.Status != "waiting" {
		return nil, ErrConflict
	}
	op.Status, op.Message, op.Attempt = "pending", "", op.Attempt+1
	if checkErr := st.SaveRouteOperation(ctx, op); checkErr != nil {
		return nil, checkErr
	}
	s.domainAudit(ctx, p, "domain.route."+op.Kind, op.ID, op.Route.ProjectID)
	s.enqueueRoute(ctx, op)
	return op, nil
}

func (s *Service) RemoveRoute(ctx context.Context, p security.Principal, project, id string, revision int64) (*RouteOperation, error) {
	if err := s.domainScope(ctx, p, project, true); err != nil {
		return nil, err
	}
	if project == "" {
		return nil, ErrInvalid
	}
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	route, err := st.Route(ctx, p.OrgID, project, id)
	if err != nil {
		return nil, err
	}
	if route.Revision != revision || route.Status == "removed" {
		return nil, ErrConflict
	}
	opID, err := randomID("routeop")
	if err != nil {
		return nil, err
	}
	op := &RouteOperation{ID: opID, Route: *route, Kind: "remove", Status: "pending", Attempt: 1, Revision: 1, Receipt: route.Receipt, Principal: p}
	if checkErr := s.Store.WithOperationLock(ctx, "ingress/"+p.OrgID+"/"+route.Binding.ServerID, func() error { return st.AcceptRoute(ctx, op) }); checkErr != nil {
		return nil, checkErr
	}
	s.domainAudit(ctx, p, "domain.route."+op.Kind, op.ID, op.Route.ProjectID)
	s.enqueueRoute(ctx, op)
	return op, nil
}

func (s *Service) ListRoutes(ctx context.Context, p security.Principal, project string) ([]*Route, error) {
	if err := s.domainScope(ctx, p, project, false); err != nil {
		return nil, err
	}
	if project == "" {
		return nil, ErrInvalid
	}
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	return st.Routes(ctx, p.OrgID, project)
}

func (s *Service) GetRouteOperation(ctx context.Context, p security.Principal, project, id string) (*RouteOperation, error) {
	_, op, err := s.routeOperation(ctx, p, project, id, false)
	return op, err
}

func (s *Service) routeClient(ctx context.Context, route Route) (*server.Client, error) {
	return s.routeCredentialClient(ctx, route, false)
}
func (s *Service) routeCleanupClient(ctx context.Context, route Route) (*server.Client, error) {
	return s.routeCredentialClient(ctx, route, true)
}
func routeCredentialIdentity(route Route, conn *connections.Connection, cleanup bool) error {
	if conn.OrgID != route.OrgID || conn.Cloud != "server" || (!cleanup && conn.Generation != route.Binding.ConnectionGeneration) {
		return ErrConflict
	}
	if route.Binding.RuntimeID == "" || conn.Details["docker_id"] != route.Binding.RuntimeID {
		return provider.ErrInfrastructureIdentity
	}
	return nil
}
func (s *Service) routeCredentialClient(ctx context.Context, route Route, cleanup bool) (*server.Client, error) {
	conn, raw, err := s.Connections.Credential(ctx, route.OrgID, route.Binding.ConnectionID)
	if err != nil {
		return nil, err
	}
	if identityErr := routeCredentialIdentity(route, conn, cleanup); identityErr != nil {
		return nil, identityErr
	}
	cfg, err := server.ParseConfig(raw, conn.Details)
	if err != nil {
		return nil, err
	}
	cfg.Details["docker_id"] = route.Binding.RuntimeID
	c, err := server.Dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	c.RequireFence = true
	return c, nil
}

func (s *Service) RecoverRoutes(ctx context.Context) error {
	st, err := s.domainStore()
	if err != nil {
		return err
	}
	ops, err := st.PendingRoutes(ctx)
	if err != nil {
		return err
	}
	for _, op := range ops {
		s.enqueueRoute(ctx, op)
	}
	return nil
}

func routeBackend(route Route) server.IngressBackend {
	return server.IngressBackend{OrgID: route.OrgID, ProjectID: route.ProjectID, EnvironmentID: route.Input.EnvironmentID, Service: route.Input.Service,
		InstanceID: route.Binding.InstanceID, ReleaseID: route.Binding.ReleaseID, ContainerID: route.Binding.ContainerID, Port: route.Input.Port}
}

func validRouteAddress(address string) bool { return net.ParseIP(address) != nil }

// RouteHistory lets callers recover accepted handoff IDs after a lost response.
func (s *Service) RouteHistory(ctx context.Context, p security.Principal, project, id string) ([]*RouteOperation, error) {
	if project == "" {
		return nil, ErrInvalid
	}
	if err := s.domainScope(ctx, p, project, false); err != nil {
		return nil, err
	}
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	if _, err = st.Route(ctx, p.OrgID, project, id); err != nil {
		return nil, err
	}
	return st.RouteOperations(ctx, p.OrgID, project, id)
}
