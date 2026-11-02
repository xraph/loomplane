package octopusloom

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
)

const (
	// writeSyncTimeout bounds the sync a write runs.
	writeSyncTimeout = 10 * time.Second
	// busyEvery and busyFor are how often, and for how long, a write tries
	// again while another sync holds the gateway.
	busyEvery = 250 * time.Millisecond
	busyFor   = 5 * time.Second
	// octopusReadTimeout bounds one read of Octopus for a board, a status
	// or discovery: opening the channel and every call on it, together.
	// When it runs out the read is Octopus not answering.
	octopusReadTimeout = 5 * time.Second
)

// GatewaySyncer pushes a gateway to Octopus. *Syncer is the real one.
type GatewaySyncer interface {
	Sync(ctx context.Context, ref GatewayRef) (SyncResult, error)
}

var _ GatewaySyncer = (*Syncer)(nil)

// ReadAccess reaches a gateway's admin port and generated secrets as a
// principal, checking its permission on the project.
// *deployments.CatalogAccess is the real one.
type ReadAccess interface {
	Channel(ctx context.Context, p security.Principal, need security.Permission, ref deployments.ComponentRef) (*deployments.ComponentChannel, error)
	GeneratedSecret(ctx context.Context, p security.Principal, need security.Permission, ref deployments.ResourceRef, name string) ([]byte, error)
}

var _ ReadAccess = (*deployments.CatalogAccess)(nil)

// RouteStatuses derives a domain route's states as a principal.
// *cloudinfra.Service is the real one.
type RouteStatuses interface {
	RouteStatus(ctx context.Context, p security.Principal, project, id string) (cloudinfra.RouteStatus, error)
}

var _ RouteStatuses = (*cloudinfra.Service)(nil)

// ServiceDeps are what the Service reads and writes through. Warn logs
// what a write's sync couldn't do on Loomplane's side; it may be nil.
type ServiceDeps struct {
	Store     Store
	Syncer    GatewaySyncer
	Access    ReadAccess
	Hostnames HostnameSource
	Bindings  BindingLister
	Authz     deployments.Authorizer
	Domains   RouteStatuses
	Warn      func(msg string, err error)
}

// Service is the Octopus API: the Routes board and what a person changes
// on a gateway. Reads need security.InfrastructureRead on the project and
// reach Octopus as the caller; writes need security.InfrastructureModify,
// save the change and then push it. A push that doesn't get through never
// fails the write: the change is kept and the result says how far it got.
type Service struct {
	d                  ServiceDeps
	busyEvery, busyFor time.Duration
	syncTimeout        time.Duration
	readTimeout        time.Duration
}

// NewService returns the Service.
func NewService(d ServiceDeps) *Service {
	return &Service{d: d, busyEvery: busyEvery, busyFor: busyFor, syncTimeout: writeSyncTimeout, readTimeout: octopusReadTimeout}
}

func (s *Service) warn(msg string, err error) {
	if s.d.Warn != nil {
		s.d.Warn(msg, err)
	}
}

// gateway is ref's gateway once p may act on its project with need. A
// gateway of another project is not found.
func (s *Service) gateway(ctx context.Context, p security.Principal, need security.Permission, ref GatewayRef) (*Gateway, GatewayRef, error) {
	ref.OrgID = p.OrgID
	if err := s.d.Authz.Require(ctx, p, need, ref.ProjectID); err != nil {
		return nil, ref, err
	}
	g, err := s.d.Store.Gateway(ctx, ref)
	if err != nil {
		return nil, ref, err
	}
	if g.ProjectID != ref.ProjectID {
		return nil, ref, ErrNotFound
	}
	return g, ref, nil
}

// Board is the gateway's routes by hostname. When Octopus doesn't answer,
// the board still lists Loomplane's routes and says FARP's can't be shown.
func (s *Service) Board(ctx context.Context, p security.Principal, ref GatewayRef) (*Board, error) {
	in, err := s.boardInput(ctx, p, ref)
	if err != nil {
		return nil, err
	}
	return BuildBoard(*in), nil
}

// Status is the gateway's state and what attaching a domain to it takes.
func (s *Service) Status(ctx context.Context, p security.Principal, ref GatewayRef) (*OctopusStatus, error) {
	g, ref, err := s.gateway(ctx, p, security.InfrastructureRead, ref)
	if err != nil {
		return nil, err
	}
	bindings, err := s.bindings(ctx, g)
	if err != nil {
		return nil, err
	}
	st := octopusStatus(*g, bindings, s.systemInfo(ctx, p, ref))
	return &st, nil
}

// Discovery is what the gateway's FARP discovery found.
func (s *Service) Discovery(ctx context.Context, p security.Principal, ref GatewayRef) (*DiscoverySummary, error) {
	in, err := s.boardInput(ctx, p, ref)
	if err != nil {
		return nil, err
	}
	d := BuildBoard(*in).Discovery
	return &d, nil
}

func (s *Service) boardInput(ctx context.Context, p security.Principal, ref GatewayRef) (*BoardInput, error) {
	g, ref, err := s.gateway(ctx, p, security.InfrastructureRead, ref)
	if err != nil {
		return nil, err
	}
	routes, err := s.d.Store.Routes(ctx, ref)
	if err != nil {
		return nil, err
	}
	bindings, err := s.bindings(ctx, g)
	if err != nil {
		return nil, err
	}
	attached, err := s.attached(ctx, ref)
	if err != nil {
		return nil, err
	}
	hostnames := make([]BoardHostname, 0, len(attached))
	for _, h := range attached {
		bh := BoardHostname{ServiceHostname: h}
		if s.d.Domains != nil && h.RouteID != "" {
			st, stErr := s.d.Domains.RouteStatus(ctx, p, ref.ProjectID, h.RouteID)
			switch {
			case stErr == nil:
				bh.Status = &st
			case !errors.Is(stErr, cloudinfra.ErrNotFound):
				return nil, stErr
			}
		}
		hostnames = append(hostnames, bh)
	}
	return &BoardInput{Gateway: *g, Bindings: bindings, Routes: routes, Hostnames: hostnames, Live: s.live(ctx, p, ref)}, nil
}

// systemInfo is what Octopus says about itself, read as p within
// readTimeout, or nil when it doesn't answer in time.
func (s *Service) systemInfo(ctx context.Context, p security.Principal, ref GatewayRef) *SystemInfo {
	ctx, cancel := context.WithTimeout(ctx, s.readTimeout)
	defer cancel()
	admin, closeCh := s.openAdmin(ctx, p, ref)
	if admin == nil {
		return nil
	}
	defer closeCh()
	info, err := admin.SystemInfo(ctx)
	if err != nil {
		return nil
	}
	return &info
}

// live is what Octopus reports, read as p, or nil when it doesn't answer.
// Opening the channel and the four reads share readTimeout, so a slow or
// hung Octopus costs a board at most that.
func (s *Service) live(ctx context.Context, p security.Principal, ref GatewayRef) *LiveState {
	ctx, cancel := context.WithTimeout(ctx, s.readTimeout)
	defer cancel()
	admin, closeCh := s.openAdmin(ctx, p, ref)
	if admin == nil {
		return nil
	}
	defer closeCh()
	var live LiveState
	var err error
	if live.Info, err = admin.SystemInfo(ctx); err != nil {
		return nil
	}
	if live.Routes, err = admin.Routes(ctx); err != nil {
		return nil
	}
	if live.Upstreams, err = admin.Upstreams(ctx); err != nil {
		return nil
	}
	if live.FarpServices, err = admin.FarpServices(ctx); err != nil {
		return nil
	}
	return &live
}

// openAdmin opens the gateway's admin API as p, which reads need. It is
// nil when Octopus can't be reached; the caller calls closeCh otherwise.
func (s *Service) openAdmin(ctx context.Context, p security.Principal, ref GatewayRef) (admin *Admin, closeCh func()) {
	if s.d.Access == nil {
		return nil, nil
	}
	token, err := s.d.Access.GeneratedSecret(ctx, p, security.InfrastructureRead, resourceRef(ref), adminTokenSecret)
	if err != nil || len(token) == 0 {
		return nil, nil
	}
	ch, err := s.d.Access.Channel(ctx, p, security.InfrastructureRead, deployments.ComponentRef{
		ResourceRef: resourceRef(ref), Component: recipes.OctopusGateway, Port: adminPort,
	})
	if err != nil {
		return nil, nil
	}
	return NewAdmin(ch, token, nil), func() {
		if ch.Close != nil {
			_ = ch.Close() //nolint:errcheck // nothing left to do with the channel
		}
	}
}

func (s *Service) bindings(ctx context.Context, g *Gateway) ([]*planning.Binding, error) {
	if s.d.Bindings == nil {
		return nil, nil
	}
	bindings, err := s.d.Bindings(ctx, g.OrgID, g.EnvironmentID)
	if err != nil {
		return nil, fmt.Errorf("list the environment's bindings: %w", err)
	}
	return bindings, nil
}

// attached is the hostnames attached to the gateway.
func (s *Service) attached(ctx context.Context, ref GatewayRef) ([]ServiceHostname, error) {
	if s.d.Hostnames == nil {
		return nil, nil
	}
	out, err := s.d.Hostnames.ServiceHostnames(ctx, ref.OrgID, ref.ProjectID, ref.EnvironmentID, GatewayAction(ref.Resource))
	if err != nil {
		return nil, fmt.Errorf("read the gateway's hostnames: %w", err)
	}
	return out, nil
}

// checkAttached is ErrInvalid unless hostname is attached to the gateway.
func checkAttached(attached []ServiceHostname, resource, hostname string) error {
	if slices.ContainsFunc(attached, func(h ServiceHostname) bool { return strings.EqualFold(h.Hostname, hostname) }) {
		return nil
	}
	return invalidf("Attach %s to %s first.", hostname, resource)
}

// checkTarget is ErrInvalid unless the gateway can send in to its service
// port: one the board calls routable, on the gateway's server or cluster.
func checkTarget(bindings []*planning.Binding, resource string, in RouteInput) error {
	env := newBoardEnv(BoardInput{Bindings: bindings})
	svc := env.services[in.Service]
	if svc == nil {
		return invalidf("%s isn't a service in this environment.", in.Service)
	}
	if svc.cfg.Port <= 0 || !httpWorkload(svc.cfg.Workload) {
		return invalidf("%s has no HTTP port, so it can't take a route.", in.Service)
	}
	if connection, found := gatewayConnection(bindings, resource); found && svc.binding.ConnectionID != connection {
		return invalidf("%s runs on a different server or cluster, so Octopus can't reach it.", in.Service)
	}
	if svc.cfg.Port != in.Port {
		return invalidf("%s listens on %d.", in.Service, svc.cfg.Port)
	}
	return nil
}

// checkRoute validates in for the route id ("" for a new one) whose
// stored hostname is was: the model's rules, no clash with the gateway's
// other routes, an attached hostname and a reachable target. A route keeps
// a hostname that was detached after it was made.
func (s *Service) checkRoute(ctx context.Context, g *Gateway, id, was string, in RouteInput) (RouteInput, error) {
	in, err := NormalizeRouteInput(in)
	if err != nil {
		return RouteInput{}, err
	}
	existing, err := s.d.Store.Routes(ctx, g.GatewayRef)
	if err != nil {
		return RouteInput{}, err
	}
	if conflict := CheckRouteConflicts(existing, id, in); conflict != nil {
		return RouteInput{}, conflict
	}
	kept := id != "" && strings.EqualFold(in.Hostname, was)
	if in.Hostname != "" && !kept {
		attached, attachedErr := s.attached(ctx, g.GatewayRef)
		if attachedErr != nil {
			return RouteInput{}, attachedErr
		}
		if invalid := checkAttached(attached, g.Resource, in.Hostname); invalid != nil {
			return RouteInput{}, invalid
		}
	}
	bindings, err := s.bindings(ctx, g)
	if err != nil {
		return RouteInput{}, err
	}
	if invalid := checkTarget(bindings, g.Resource, in); invalid != nil {
		return RouteInput{}, invalid
	}
	return in, nil
}

// CreateRoute adds a manual route and pushes it.
func (s *Service) CreateRoute(ctx context.Context, p security.Principal, ref GatewayRef, in RouteInput) (*RouteResult, error) {
	g, ref, err := s.gateway(ctx, p, security.InfrastructureModify, ref)
	if err != nil {
		return nil, err
	}
	if in, err = s.checkRoute(ctx, g, "", "", in); err != nil {
		return nil, err
	}
	r := routeFrom(ref, in)
	r.CreatedBy = p.UserID
	if createErr := s.d.Store.CreateRoute(ctx, r); createErr != nil {
		return nil, createErr
	}
	return &RouteResult{Route: r, Sync: s.sync(ctx, ref)}, nil
}

// UpdateRoute changes a manual route, at the revision in.Revision, and
// pushes it.
func (s *Service) UpdateRoute(ctx context.Context, p security.Principal, ref GatewayRef, id string, in RouteInput) (*RouteResult, error) {
	g, ref, err := s.gateway(ctx, p, security.InfrastructureModify, ref)
	if err != nil {
		return nil, err
	}
	routes, err := s.d.Store.Routes(ctx, ref)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(routes, func(r *Route) bool { return r != nil && r.ID == id })
	if i < 0 {
		return nil, ErrNotFound
	}
	if in, err = s.checkRoute(ctx, g, id, routes[i].Hostname, in); err != nil {
		return nil, err
	}
	r := routeFrom(ref, in)
	r.ID, r.Revision = id, in.Revision
	if updateErr := s.d.Store.UpdateRoute(ctx, r); updateErr != nil {
		return nil, updateErr
	}
	return &RouteResult{Route: r, Sync: s.sync(ctx, ref)}, nil
}

func routeFrom(ref GatewayRef, in RouteInput) *Route {
	return &Route{
		GatewayRef: ref, Hostname: in.Hostname, Path: in.Path, Methods: in.Methods, Service: in.Service,
		Port: in.Port, StripPrefix: in.StripPrefix, Note: in.Note,
	}
}

// RemoveRoute deletes a manual route, at revision, and pushes the gateway
// without it. A FARP route it hid comes back.
func (s *Service) RemoveRoute(ctx context.Context, p security.Principal, ref GatewayRef, id string, revision int64) (*SyncResult, error) {
	_, ref, err := s.gateway(ctx, p, security.InfrastructureModify, ref)
	if err != nil {
		return nil, err
	}
	if deleteErr := s.d.Store.DeleteRoute(ctx, ref, id, revision); deleteErr != nil {
		return nil, deleteErr
	}
	res := s.sync(ctx, ref)
	return &res, nil
}

// SetFarpHostnames sets the hostnames FARP routes answer on, at the
// gateway's revision, and pushes them. Each one must be attached; none is
// any host.
func (s *Service) SetFarpHostnames(ctx context.Context, p security.Principal, ref GatewayRef, hostnames []string, revision int64) (*OctopusStatus, error) {
	g, ref, err := s.gateway(ctx, p, security.InfrastructureModify, ref)
	if err != nil {
		return nil, err
	}
	clean := []string{}
	for _, h := range hostnames {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" && !slices.Contains(clean, h) {
			clean = append(clean, h)
		}
	}
	slices.Sort(clean)
	if len(clean) > 0 {
		attached, attachedErr := s.attached(ctx, ref)
		if attachedErr != nil {
			return nil, attachedErr
		}
		for _, h := range clean {
			if invalid := checkAttached(attached, g.Resource, h); invalid != nil {
				return nil, invalid
			}
		}
	}
	if _, err = s.d.Store.SetFarpHostnames(ctx, ref, clean, revision); err != nil {
		return nil, err
	}
	s.sync(ctx, ref)
	if g, err = s.d.Store.Gateway(ctx, ref); err != nil {
		return nil, err
	}
	bindings, err := s.bindings(ctx, g)
	if err != nil {
		return nil, err
	}
	st := octopusStatus(*g, bindings, nil)
	return &st, nil
}

// Sync pushes the gateway now: what the reconcile does every 30 seconds,
// and on a server, registering its services with FARP again.
func (s *Service) Sync(ctx context.Context, p security.Principal, ref GatewayRef) (*SyncResult, error) {
	_, ref, err := s.gateway(ctx, p, security.InfrastructureModify, ref)
	if err != nil {
		return nil, err
	}
	res := s.sync(ctx, ref)
	return &res, nil
}

// sync pushes the gateway within writeSyncTimeout. While another sync
// holds it, it tries again every busyEvery for up to busyFor, then
// reports the change as pending. What doesn't get through is in the
// result, never an error: the change is saved and the reconcile pushes
// it. A failure on Loomplane's side is logged as well.
func (s *Service) sync(ctx context.Context, ref GatewayRef) SyncResult {
	ctx, cancel := context.WithTimeout(ctx, s.syncTimeout)
	defer cancel()
	deadline := time.Now().Add(s.busyFor)
	for {
		res, err := s.d.Syncer.Sync(ctx, ref)
		switch {
		case err == nil:
			return res
		case errors.Is(err, ErrBusy) && time.Now().Before(deadline):
			select {
			case <-ctx.Done():
				return res
			case <-time.After(s.busyEvery):
			}
			continue
		case errors.Is(err, errLoomplaneSide):
			s.warn(fmt.Sprintf("Octopus gateway %s in environment %s didn't sync after a change (%s)", ref.Resource, ref.EnvironmentID, res.State), err)
		}
		return res
	}
}
