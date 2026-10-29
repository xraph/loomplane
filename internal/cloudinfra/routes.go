package cloudinfra

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
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
	// BlockedPathPrefixes never reach the service on this hostname.
	BlockedPathPrefixes []string `json:"blocked_path_prefixes,omitempty"`
}

// Servers always belong to a Cloud. Only a Kubernetes binding on its
// environment's own connection routes without one.
const noCloudPlacement = "This service isn't placed through a Cloud, so Loomplane can't route to its server."

const blockedPathRule = "Blocked path prefixes must look like /admin: start with /, no trailing slash, no //, %, *, ?, # or dot segments, at most 8 of at most 128 bytes."

// ValidateBlockedPathPrefixes takes prefixes the caller already sorted and
// de-duplicated.
func ValidateBlockedPathPrefixes(prefixes []string) error {
	if !server.ValidBlockedPathPrefixes(prefixes) {
		return fmt.Errorf("%w: %s", ErrInvalid, blockedPathRule)
	}
	return nil
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
	// A Kubernetes binding names the namespace (RuntimeID is its UID), the
	// Service and the connection's ingress class and address. It has no
	// release or container, so it stays the same across releases.
	Namespace    string `json:"namespace,omitempty"`
	ServiceName  string `json:"service_name,omitempty"`
	IngressClass string `json:"ingress_class,omitempty"`
	// A platform binding names its provider and the app it runs as
	// (ExternalID), on the connection it was deployed with.
	Provider   string `json:"provider,omitempty"`
	ExternalID string `json:"external_id,omitempty"`
}

// routeLockKey serializes the routes that share one ingress: a server's
// Caddy config, a Kubernetes connection's namespace, or a platform app's
// spec.
func routeLockKey(route Route) string {
	switch route.Binding.BackendName() {
	case RouteBackendKubernetes:
		return "ingress/" + route.OrgID + "/kubernetes:" + route.Binding.ConnectionID
	case RouteBackendPlatform:
		return "ingress/" + route.OrgID + "/platform:" + route.Binding.ConnectionID + "/" + route.Binding.ExternalID
	}
	return "ingress/" + route.OrgID + "/" + route.Binding.ServerID
}

// noServerIngress reports a route with no server ingress config: one on a
// cluster, or on a platform that routes it itself.
func noServerIngress(route Route) bool {
	backend := route.Binding.BackendName()
	return backend == RouteBackendKubernetes || backend == RouteBackendPlatform
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
	// Receipts are a platform route's records. Other routes keep Receipt.
	Receipts []provider.DNSRecord `json:"receipts,omitempty"`
}

// RouteOperation keeps a platform route's records in DesiredRecords and
// Receipts, since one name can take several. Other routes keep the single
// DesiredRecord and Receipt.
type RouteOperation struct {
	CreatedAt          time.Time               `json:"created_at"`
	Source             *Route                  `json:"source,omitempty"`
	HandoffID          string                  `json:"handoff_id,omitempty"`
	NotBefore          time.Time               `json:"not_before"`
	CancelledIngress   *IngressTransition      `json:"-"`
	SourceTransition   *IngressTransition      `json:"-"`
	ID                 string                  `json:"id" forge:"id"`
	Route              Route                   `json:"route"`
	Kind               string                  `json:"kind"`
	Status             string                  `json:"status"`
	Message            string                  `json:"message"`
	Review             string                  `json:"review"`
	ExpiresAt          time.Time               `json:"expires_at"`
	Attempt            int                     `json:"attempt"`
	Revision           int64                   `json:"revision"`
	Generation         int64                   `json:"generation"`
	DNSAttempted       bool                    `json:"dns_attempted"`
	DesiredRecord      provider.DNSRecord      `json:"desired_record"`
	PreviousReceipt    *provider.DNSRecord     `json:"previous_receipt,omitempty"`
	Receipt            *provider.DNSRecord     `json:"receipt,omitempty"`
	DesiredRecords     []provider.DNSRecord    `json:"desired_records,omitempty"`
	Receipts           []provider.DNSRecord    `json:"receipts,omitempty"`
	Cost               *RouteCost              `json:"cost,omitempty"`
	Notices            []string                `json:"notices,omitempty"`
	DesiredConfig      *server.IngressConfig   `json:"-"`
	PreviousConfig     *server.IngressSnapshot `json:"-"`
	AppliedConfig      *server.IngressSnapshot `json:"-"`
	Principal          security.Principal      `json:"-"`
	ExecutionPrincipal *security.Principal     `json:"-"`
}

// executionPrincipal falls back for operations accepted before retry actors were stored.
func (op *RouteOperation) executionPrincipal() security.Principal {
	if op.ExecutionPrincipal != nil {
		return *op.ExecutionPrincipal
	}
	return op.Principal
}

type IngressState struct {
	Generation int64                   `json:"generation"`
	Revision   int64                   `json:"revision"`
	Config     server.IngressConfig    `json:"config"`
	Current    *server.IngressSnapshot `json:"current"`
	Previous   *server.IngressSnapshot `json:"previous"`
	// DNSTokens maps each zone whose token file the server holds to the
	// challenge connection generation that token came from.
	DNSTokens map[string]int64 `json:"dns_tokens,omitempty"`
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
	// ZoneProofsDue lists, across organizations, at most limit managed
	// zones that are unverified, with a proof unexpired at now and last
	// checked no later than checkedBefore, the longest unchecked first.
	ZoneProofsDue(ctx context.Context, now, checkedBefore, recheckBefore time.Time, limit int) ([]*Zone, error)
	ResolveRouteBinding(context.Context, string, string, RouteInput) (RouteBinding, error)
	// RouteBindingCommit is the commit the binding was last deployed from.
	RouteBindingCommit(ctx context.Context, org, bindingID string) (string, error)
	// RouteBindingCloud is the binding's Cloud placement, "" when it has none.
	RouteBindingCloud(ctx context.Context, org, project, environment, service string) (string, error)
	PreviewRoute(context.Context, *RouteOperation) error
	AcceptRoute(context.Context, *RouteOperation) error
	RouteOperation(context.Context, string, string) (*RouteOperation, error)
	RouteOperations(context.Context, string, string, string) ([]*RouteOperation, error)
	RouteHandoffReceipts(context.Context, string, string, string, string) ([]*RouteOperation, error)
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
	if in.Port < 1 || in.Port > 65535 || (in.ExternalTTLSeconds != 0 && (in.ExternalTTLSeconds < 60 || in.ExternalTTLSeconds > 86400)) {
		return nil, ErrInvalid
	}
	// Exact hostnames are refused before any lookup, as before wildcards.
	if !strings.Contains(in.Hostname, "*") {
		if _, err = NormalizeHostname(in.Hostname); err != nil {
			return nil, err
		}
	}
	in.BlockedPathPrefixes = slices.Compact(slices.Sorted(slices.Values(in.BlockedPathPrefixes)))
	if checkErr := ValidateBlockedPathPrefixes(in.BlockedPathPrefixes); checkErr != nil {
		return nil, checkErr
	}
	// A subdomain is verified through its domain, which the project need
	// not be granted.
	zone, zones, err := s.linkedZones(ctx, st, p.OrgID, in.ZoneID)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(zone.ProjectIDs, project) || !zone.verified() {
		return nil, ErrNotFound
	}
	hostname, err := ValidateRouteHostname(zone, in.Hostname)
	if err != nil {
		return nil, err
	}
	// A name under a subdomain is routed there, under its own grants.
	if checkErr := checkDeepestZone(zone, hostname.Hostname, zones); checkErr != nil {
		return nil, checkErr
	}
	in.Hostname = hostname.Hostname
	if checkErr := s.verifyZoneControl(ctx, zone); checkErr != nil {
		return nil, checkErr
	}
	// The stored intent records the Cloud the binding resolved under.
	if in.CloudID == "" {
		if in.CloudID, err = st.RouteBindingCloud(ctx, p.OrgID, project, in.EnvironmentID, in.Service); err != nil {
			return nil, err
		}
	}
	binding, err := st.ResolveRouteBinding(ctx, p.OrgID, project, in)
	if err != nil {
		return nil, err
	}
	switch backend := binding.BackendName(); {
	case backend == RouteBackendPlatform:
		// The platform routes the app itself, with or without a Cloud.
		if checkErr := checkPlatformRoute(in); checkErr != nil {
			return nil, checkErr
		}
	case in.CloudID == "" && backend != RouteBackendKubernetes:
		return nil, fmt.Errorf("%w: %s", ErrInvalid, noCloudPlacement)
	}
	id, err := randomID("route")
	if err != nil {
		return nil, err
	}
	route := Route{ID: id, OrgID: p.OrgID, ProjectID: project, Input: in, Binding: binding, Status: "pending", Revision: 1, UpdatedAt: s.now().UTC()}
	var notices []string
	if hostname.Wildcard {
		if notices, err = s.wildcardNotices(ctx, st, route); err != nil {
			return nil, err
		}
	}
	// A platform route writes the records its platform asks for, in
	// DesiredRecords. Other routes write one record to their address.
	var record provider.DNSRecord
	var platformRecords []provider.DNSRecord
	if binding.BackendName() == RouteBackendPlatform {
		platformRecords, err = s.platformDNSRecords(ctx, zone, route)
	} else {
		record, err = routeDNSRecord(zone, in.Hostname, binding.Address, id)
	}
	if err != nil {
		return nil, err
	}
	desired := platformRecords
	if desired == nil {
		desired = []provider.DNSRecord{record}
	}
	if checkErr := s.preflightRouteDNS(ctx, zone, desired...); checkErr != nil {
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
	route.OperationID = opID
	op := &RouteOperation{ID: opID, Kind: "apply", Status: "preview", Review: review, ExpiresAt: s.now().UTC().Add(15 * time.Minute), Revision: 1, Principal: p,
		Route: route, DesiredRecord: record, DesiredRecords: platformRecords, Notices: notices}
	if checkErr := st.PreviewRoute(ctx, op); checkErr != nil {
		return nil, checkErr
	}
	return op, nil
}

const errUnpublishedProxyImage = wildcardRefusalError("Wildcard routes on servers need Loomplane's DNS-capable proxy image, which isn't published yet.")

// wildcardNotices says what a wildcard route on a server costs: a proxy
// still on the stock image restarts on the DNS-capable one.
func (s *Service) wildcardNotices(ctx context.Context, st DomainStore, route Route) ([]string, error) {
	switch route.Binding.BackendName() {
	case RouteBackendKubernetes:
		// The cluster's DNS-01 Issuer is added on apply; nothing restarts.
		return nil, nil
	case RouteBackendPlatform:
		return nil, fmt.Errorf("%w: %s", ErrInvalid, platformWildcards)
	}
	if !s.ingressImagePinned() {
		return nil, errUnpublishedProxyImage
	}
	upgrade, err := s.ingressNeedsUpgrade(ctx, route)
	if err != nil || !upgrade {
		return nil, err
	}
	names, err := st.RuntimeNames(ctx, route.OrgID)
	if err != nil {
		return nil, err
	}
	name := names[route.Binding.ServerID]
	if name == "" {
		name = route.Binding.ServerID
	}
	return []string{"Every HTTPS hostname on " + name + " is unavailable for a few seconds while Loomplane restarts its proxy with DNS challenge support. Certificates and settings stay in their volumes."}, nil
}

func (s *Service) routeIngressNeedsUpgrade(ctx context.Context, route Route) (bool, error) {
	c, err := s.routeClient(ctx, route)
	if err != nil {
		return false, err
	}
	defer c.Close()
	return server.IngressNeedsUpgrade(ctx, c, route.OrgID, route.Binding.ServerID)
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
	zone, zones, err := s.linkedZones(ctx, st, p.OrgID, op.Route.Input.ZoneID)
	if err != nil {
		return nil, err
	}
	// A subdomain added since the preview takes its names.
	if checkErr := checkDeepestZone(zone, op.Route.Input.Hostname, zones); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := s.verifyZoneControl(ctx, zone); checkErr != nil {
		return nil, checkErr
	}
	op.Status, op.Attempt = "pending", 1
	op.ExecutionPrincipal = &p
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
			s.domainAudit(ctx, op.executionPrincipal(), "domain.route.enqueue_pending", op.ID, op.Route.ProjectID)
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
	op.ExecutionPrincipal = &p
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
	op := &RouteOperation{ID: opID, Route: *route, Kind: "remove", Status: "pending", Attempt: 1, Revision: 1, Receipt: route.Receipt, Receipts: route.Receipts, Principal: p}
	if checkErr := s.Store.WithOperationLock(ctx, routeLockKey(*route), func() error { return st.AcceptRoute(ctx, op) }); checkErr != nil {
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
