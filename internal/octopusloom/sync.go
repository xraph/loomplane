package octopusloom

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

// What a sync left the gateway in: SyncResult.State.
const (
	// SyncApplied is Octopus holding everything Loomplane wants it to.
	SyncApplied = "applied"
	// SyncPending is a sync that didn't reach Octopus, or didn't run because
	// another one holds the gateway. Nothing is lost: the reconcile pushes it.
	SyncPending = "pending"
	// SyncFailed is a sync that reached Octopus and couldn't apply all of
	// it, such as a route Octopus already has its own route for. The rest
	// is applied.
	SyncFailed = "failed"
)

// The generated secrets of the Octopus kind a sync reads.
const (
	adminTokenSecret = "admin-api-token"
	farpTokenSecret  = "farp-token"
)

// Messages a SyncResult carries, for the person who changed a route.
const (
	msgUnavailable = "Octopus isn't answering on its private port. Loomplane keeps the route and pushes it within 30 seconds."
	msgNotRunning  = "Octopus isn't running in this environment yet. Loomplane pushes the route once it is."
	msgBusy        = "Loomplane is already pushing this gateway's routes. Your change goes out within 30 seconds."
	msgInternal    = "Loomplane couldn't work out what this gateway should serve. It tries again within 30 seconds."
	msgCredentials = "Loomplane couldn't read the gateway's admin credentials. It tries again within 30 seconds."
	msgProblem     = "Octopus didn't accept a change Loomplane pushed. Loomplane tries again within 30 seconds."
	msgRefused     = "Octopus refused Loomplane's admin requests. Loomplane tries again within 30 seconds."
)

// saveTimeout bounds recording what a sync did. The record is made apart
// from the sync's own context, so a sync that ran out of time still
// records how far it got.
const saveTimeout = 5 * time.Second

// errLoomplaneSide marks a sync that stopped on Loomplane's side rather
// than Octopus's: the store, the bindings, the hostnames or the secrets.
// Test with errors.Is.
var errLoomplaneSide = errors.New("loomplane couldn't sync the gateway")

// loomplaneError is err marked errLoomplaneSide. Its text is err's.
type loomplaneError struct{ err error }

func (e loomplaneError) Error() string        { return e.err.Error() }
func (e loomplaneError) Unwrap() error        { return e.err }
func (e loomplaneError) Is(target error) bool { return target == errLoomplaneSide }

func ours(err error) error { return loomplaneError{err: err} }

// Origins of the routes Loomplane put in Octopus: from the config file it
// rendered, or from the admin API. Octopus reports a config route replaced
// at runtime as admin until it restarts.
const (
	originConfig = "config"
	originAdmin  = "admin"
)

// ChannelOpener opens a channel to the gateway component's admin port.
type ChannelOpener func(ctx context.Context, ref GatewayRef) (*deployments.ComponentChannel, error)

// SecretReader reads one of the gateway's generated secrets by name.
type SecretReader func(ctx context.Context, ref GatewayRef, name string) ([]byte, error)

// BindingLister lists an environment's bindings.
type BindingLister func(ctx context.Context, orgID, environmentID string) ([]*planning.Binding, error)

// ServiceHostname is a hostname attached to a service, as the Domains
// routes list it.
type ServiceHostname = cloudinfra.ServiceHostname

// HostnameSource lists the hostnames attached to a service. Every one it
// returns counts as attached. *cloudinfra.Service is the real one.
type HostnameSource interface {
	ServiceHostnames(ctx context.Context, orgID, projectID, environmentID, service string) ([]ServiceHostname, error)
}

var _ HostnameSource = (*cloudinfra.Service)(nil)

// GatewayAction is the action name of a gateway's Octopus component, which
// its hostnames and its binding are recorded under.
func GatewayAction(resource string) string {
	return planning.ComponentActionName(resource, recipes.OctopusGateway)
}

// Syncer pushes what a gateway should hold to Octopus's admin API, so a
// route change needs no redeploy and an Octopus restart is put right by the
// next sync. Now defaults to time.Now and a nil Hostnames attaches nothing.
// Warn logs what a background sync couldn't do; it may be nil. What it is
// given never holds a token.
type Syncer struct {
	Store     Store
	Open      ChannelOpener
	Secret    SecretReader
	Bindings  BindingLister
	Hostnames HostnameSource
	Now       func() time.Time
	Warn      func(msg string, err error)
}

func (s *Syncer) warn(msg string, err error) {
	if s.Warn != nil {
		s.Warn(msg, err)
	}
}

// SyncResult is what one sync did. Restored counts the routes and upstreams
// it put back after it found Octopus had restarted.
type SyncResult struct {
	State      string `json:"state"` // "applied" | "pending" | "failed"
	Message    string `json:"message,omitempty"`
	Generation int64  `json:"generation"`
	Restarted  bool   `json:"restarted"`
	Restored   int    `json:"restored"`
}

// Sync pushes the gateway's routes, upstreams and FARP binding to Octopus
// over the Syncer's own channel and secrets. See SyncWith.
func (s *Syncer) Sync(ctx context.Context, ref GatewayRef) (SyncResult, error) {
	return s.SyncWith(ctx, ref, s.Open, s.Secret)
}

// SyncWith is Sync over the channel open opens and the secrets secret
// reads, such as a deploy's own. It holds the gateway's lock while it runs.
//
// The result is always filled in. The error is nil only when State is
// "applied". When another sync holds the lock it wraps ErrBusy and nothing
// is sent; when Octopus can't be reached it wraps ErrUnavailable and the
// applied generation stays where it was. Either way State is "pending".
// Otherwise State is "failed": what Octopus refused is in the gateway's
// RouteErrors and State.Error, and everything else is applied.
func (s *Syncer) SyncWith(ctx context.Context, ref GatewayRef, open ChannelOpener, secret SecretReader) (SyncResult, error) {
	var res SyncResult
	var syncErr error
	err := s.Store.WithGatewayLock(ctx, ref, func() error {
		res, syncErr = s.syncLocked(ctx, ref, open, secret)
		return nil
	})
	switch {
	case errors.Is(err, ErrBusy):
		res = SyncResult{State: SyncPending, Message: msgBusy}
		if g, gerr := s.Store.Gateway(ctx, ref); gerr == nil {
			res.Generation = g.Generation
		}
		return res, fmt.Errorf("sync Octopus gateway %s: %w", ref.Resource, err)
	case err != nil:
		return SyncResult{State: SyncFailed, Message: msgInternal}, fmt.Errorf("sync Octopus gateway %s: %w", ref.Resource, ours(err))
	}
	return res, syncErr
}

func (s *Syncer) syncLocked(ctx context.Context, ref GatewayRef, open ChannelOpener, secret SecretReader) (SyncResult, error) {
	g, err := s.Store.Gateway(ctx, ref)
	if err != nil {
		return SyncResult{State: SyncFailed, Message: msgInternal}, fmt.Errorf("sync Octopus gateway %s: %w", ref.Resource, ours(err))
	}
	d, routes, found, err := s.desired(ctx, g)
	if err != nil {
		return s.failed(ctx, g, msgInternal, ours(err))
	}
	if !found {
		return s.pending(ctx, g, nil, msgNotRunning, errors.New("the gateway has no binding"))
	}
	if open == nil || secret == nil {
		return s.failed(ctx, g, msgInternal, ours(errors.New("the syncer has no way to reach Octopus")))
	}
	adminToken, err := secret(ctx, ref, adminTokenSecret)
	if err != nil {
		return s.failed(ctx, g, msgCredentials, ours(err))
	}
	farpToken, err := secret(ctx, ref, farpTokenSecret)
	if err != nil {
		return s.failed(ctx, g, msgCredentials, ours(err))
	}
	ch, err := open(ctx, ref)
	if err != nil {
		return s.pending(ctx, g, nil, msgUnavailable, err)
	}
	defer func() {
		if ch.Close != nil {
			_ = ch.Close() //nolint:errcheck // nothing left to do with the channel
		}
	}()

	run := newSyncRun(NewAdmin(ch, adminToken, farpToken), g, d, routes)
	if err := run.read(ctx); err != nil {
		if errors.Is(err, ErrUnavailable) {
			return s.pending(ctx, g, run, msgUnavailable, err)
		}
		return s.failed(ctx, g, msgRefused, err)
	}
	// Upstreams go in before the routes that use them, and stale routes
	// come out before the upstreams they use.
	for _, step := range []func(context.Context) error{
		run.pushUpstreams,
		run.pushRoutes,
		run.removeStaleRoutes,
		run.removeStaleUpstreams,
		run.pushFarpBinding,
		func(ctx context.Context) error { return run.syncRegistrations(ctx, s.now()) },
	} {
		if err := step(ctx); err != nil {
			return s.pending(ctx, g, run, msgUnavailable, err)
		}
	}
	return s.finish(ctx, g, run)
}

// now is the Syncer's clock.
func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// desired builds what g should hold, from the environment's bindings, the
// gateway's manual routes and the hostnames attached to it: the one place
// both the rendered config file and the admin API push get it from. found
// is false when the gateway's own binding isn't there, as on a first
// deploy: d is then built without a connection, so no service is
// reachable, and a sync has no knowing which services it can reach.
func (s *Syncer) desired(ctx context.Context, g *Gateway) (d Desired, routes []*Route, found bool, err error) {
	if s.Bindings == nil {
		return Desired{}, nil, false, errors.New("the syncer can't list bindings")
	}
	bindings, err := s.Bindings(ctx, g.OrgID, g.EnvironmentID)
	if err != nil {
		return Desired{}, nil, false, fmt.Errorf("list the environment's bindings: %w", err)
	}
	connectionID, found := gatewayConnection(bindings, g.Resource)
	routes, err = s.Store.Routes(ctx, g.GatewayRef)
	if err != nil {
		return Desired{}, nil, false, fmt.Errorf("read the gateway's routes: %w", err)
	}
	var hostnames []string
	if s.Hostnames != nil {
		attached, err := s.Hostnames.ServiceHostnames(ctx, g.OrgID, g.ProjectID, g.EnvironmentID, GatewayAction(g.Resource))
		if err != nil {
			return Desired{}, nil, false, fmt.Errorf("read the gateway's hostnames: %w", err)
		}
		for _, h := range attached {
			hostnames = append(hostnames, h.Hostname)
		}
	}
	d = BuildDesired(DesiredInput{Gateway: *g, ConnectionID: connectionID, Bindings: bindings, Routes: routes, Hostnames: hostnames})
	return d, routes, found, nil
}

// gatewayConnection is the connection the gateway's Octopus component runs
// on, from its binding.
func gatewayConnection(bindings []*planning.Binding, resource string) (string, bool) {
	name := GatewayAction(resource)
	for _, b := range bindings {
		if b != nil && b.Subject == planning.ResourceSubject && b.Name == name {
			return b.ConnectionID, true
		}
	}
	return "", false
}

// pending records a sync that didn't get through. The applied generation,
// start time and sync time stay as they were, so the next sync sees a
// restart this one may have found. Upstreams put in before Octopus went
// away are kept as owned.
func (s *Syncer) pending(ctx context.Context, g *Gateway, run *syncRun, msg string, cause error) (SyncResult, error) {
	if !errors.Is(cause, ErrUnavailable) {
		cause = fmt.Errorf("%w: %w", ErrUnavailable, cause)
	}
	res := SyncResult{State: SyncPending, Message: msg, Generation: g.Generation}
	state := g.State
	state.Error = msg
	if run != nil {
		res.Restarted, res.Restored = run.restarted, run.restored
		state.OwnedUpstreams = sortedUnion(g.State.OwnedUpstreams, run.putUpstreams)
		if run.registrations != nil {
			state.Registrations = run.registrations
		}
	}
	err := fmt.Errorf("sync Octopus gateway %s: %w", g.Resource, cause)
	if saveErr := s.save(ctx, g.GatewayRef, g.AppliedGeneration, state); saveErr != nil {
		err = errors.Join(err, saveErr)
	}
	return res, err
}

// save records what a sync did, on a context of its own that the sync's
// deadline doesn't end. A failure is Loomplane's.
func (s *Syncer) save(ctx context.Context, ref GatewayRef, applied int64, state SyncState) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), saveTimeout)
	defer cancel()
	if err := s.Store.SaveSyncState(ctx, ref, applied, state); err != nil {
		return ours(fmt.Errorf("record the sync: %w", err))
	}
	return nil
}

// failed records a sync that couldn't run, keeping the rest of the state.
func (s *Syncer) failed(ctx context.Context, g *Gateway, msg string, cause error) (SyncResult, error) {
	state := g.State
	state.Error = msg
	err := fmt.Errorf("sync Octopus gateway %s: %w", g.Resource, cause)
	if saveErr := s.save(ctx, g.GatewayRef, g.AppliedGeneration, state); saveErr != nil {
		err = errors.Join(err, saveErr)
	}
	return SyncResult{State: SyncFailed, Message: msg, Generation: g.Generation}, err
}

// finish records a sync that reached Octopus throughout.
func (s *Syncer) finish(ctx context.Context, g *Gateway, run *syncRun) (SyncResult, error) {
	desiredUpstreams := make([]string, 0, len(run.desired.Upstreams))
	for _, u := range run.desired.Upstreams {
		desiredUpstreams = append(desiredUpstreams, u.Name)
	}
	routeErrors := maps.Clone(run.desired.Skipped)
	if routeErrors == nil {
		routeErrors = map[string]string{}
	}
	maps.Copy(routeErrors, run.routeErrors)
	if len(routeErrors) == 0 {
		routeErrors = nil
	}
	state := SyncState{
		StartTime:      run.info.StartTime,
		SyncedAt:       s.now(),
		OwnedUpstreams: sortedUnion(desiredUpstreams, run.keepOwned),
		RouteErrors:    routeErrors,
		Registrations:  g.State.Registrations,
	}
	if run.registrations != nil {
		state.Registrations = run.registrations
	}
	if len(state.Registrations) == 0 {
		state.Registrations = nil
	}
	res := SyncResult{State: SyncApplied, Generation: g.Generation, Restarted: run.restarted, Restored: run.restored}
	applied := g.Generation
	var err error
	switch {
	case run.rejected > 0 || len(run.problems) > 0:
		applied = g.AppliedGeneration
		res.State = SyncFailed
		res.Message = msgProblem
		switch {
		case run.rejected == 1:
			res.Message = "Octopus didn't accept one of this gateway's routes. The route says why."
		case run.rejected > 1:
			res.Message = fmt.Sprintf("Octopus didn't accept %d of this gateway's routes. Each one says why.", run.rejected)
		}
		state.Error = strings.Join(append([]string{res.Message}, run.problems...), " ")
		err = fmt.Errorf("sync Octopus gateway %s: %s", g.Resource, state.Error)
	case run.restored > 0:
		res.Message = fmt.Sprintf("Octopus restarted. Loomplane put back %d of its routes and upstreams.", run.restored)
	}
	if saveErr := s.save(ctx, g.GatewayRef, applied, state); saveErr != nil {
		res.State, res.Message = SyncFailed, msgInternal
		err = errors.Join(err, fmt.Errorf("sync Octopus gateway %s: %w", g.Resource, saveErr))
	}
	return res, err
}

// syncRun is one sync's view of Octopus and what it did there. Each step
// returns an error only when Octopus stops answering; anything Octopus
// refuses is recorded and the sync carries on.
type syncRun struct {
	admin   *Admin
	gateway *Gateway
	desired Desired
	// owner maps an Octopus route id to the manual route it comes from.
	owner map[string]*Route

	info          SystemInfo
	restarted     bool
	liveRoutes    map[string]*liveRoute
	liveUpstreams map[string]UpstreamInfo

	restored        int
	putUpstreams    []string
	failedUpstreams map[string]error
	keepOwned       []string
	// wanted are the desired Octopus route ids; replaced are the ones this
	// sync put and deleted the stale ones it removed. orphaned are the
	// upstreams the removed routes sent to.
	wanted, replaced, deleted map[string]bool
	orphaned                  map[string]bool
	// registrations are the gateway's FARP registrations as this sync leaves
	// them; nil until syncRegistrations runs, which a Kubernetes gateway
	// never does.
	registrations map[string]Registered
	routeErrors   map[string]string
	rejected      int
	problems      []string
}

func newSyncRun(admin *Admin, g *Gateway, d Desired, routes []*Route) *syncRun {
	run := &syncRun{
		admin: admin, gateway: g, desired: d,
		owner: map[string]*Route{}, failedUpstreams: map[string]error{}, routeErrors: map[string]string{},
		wanted: map[string]bool{}, replaced: map[string]bool{}, deleted: map[string]bool{}, orphaned: map[string]bool{},
	}
	for _, route := range d.Routes {
		run.wanted[route.ID] = true
	}
	for _, r := range routes {
		if r == nil {
			continue
		}
		for _, id := range OctopusRouteIDs(r.ID, r.Wildcard()) {
			run.owner[id] = r
		}
	}
	return run
}

// read takes in what Octopus holds now. A start time other than the one
// last seen means Octopus restarted and lost what was pushed at runtime.
func (r *syncRun) read(ctx context.Context) error {
	info, err := r.admin.SystemInfo(ctx)
	if err != nil {
		return err
	}
	r.info = info
	r.restarted = r.gateway.State.StartTime != "" && info.StartTime != r.gateway.State.StartTime
	routes, err := r.admin.Routes(ctx)
	if err != nil {
		return err
	}
	r.liveRoutes = groupRoutes(routes)
	upstreams, err := r.admin.Upstreams(ctx)
	if err != nil {
		return err
	}
	r.liveUpstreams = map[string]UpstreamInfo{}
	for _, u := range upstreams {
		r.liveUpstreams[u.Name] = u
	}
	return nil
}

// put counts a PUT that went through.
func (r *syncRun) put() {
	if r.restarted {
		r.restored++
	}
}

func (r *syncRun) problem(format string, args ...any) {
	r.problems = append(r.problems, fmt.Sprintf(format, args...))
}

// routeError records the first thing Octopus refused for a manual route.
func (r *syncRun) routeError(id, octopusID, msg string) {
	if id == "" {
		r.problem("Octopus didn't accept route %s: %s", octopusID, msg)
		return
	}
	if _, ok := r.routeErrors[id]; !ok {
		r.routeErrors[id] = msg
		r.rejected++
	}
}

// pushUpstreams puts every desired upstream Octopus doesn't hold as it
// should. An upstream with Loomplane's name is Loomplane's to set.
func (r *syncRun) pushUpstreams(ctx context.Context) error {
	for _, u := range r.desired.Upstreams {
		if live, ok := r.liveUpstreams[u.Name]; ok && sameInstances(live.Instances, u.Instances) {
			continue
		}
		body := UpstreamBody{Strategy: "round_robin", Instances: make([]UpstreamInstance, 0, len(u.Instances))}
		for _, i := range u.Instances {
			body.Instances = append(body.Instances, UpstreamInstance{ID: i.ID, Address: i.Host, Port: i.Port})
		}
		err := r.admin.PutUpstream(ctx, u.Name, body)
		switch {
		case errors.Is(err, ErrUnavailable):
			return err
		case err != nil:
			r.failedUpstreams[u.Name] = err
		default:
			r.putUpstreams = append(r.putUpstreams, u.Name)
			r.put()
		}
	}
	return nil
}

// pushRoutes puts every desired route Octopus doesn't hold as it should.
func (r *syncRun) pushRoutes(ctx context.Context) error {
	for _, d := range r.desired.Routes {
		manual := r.owner[d.ID]
		manualID := ""
		if manual != nil {
			manualID = manual.ID
		}
		if err := r.failedUpstreams[d.Upstream]; err != nil {
			r.routeError(manualID, d.ID, fmt.Sprintf("Octopus didn't accept the upstream this route sends to: %v", err))
			continue
		}
		if live := r.liveRoutes[d.ID]; live != nil && live.matches(d) {
			continue
		}
		err := r.putRoute(ctx, d)
		var conflict *RouteConflictError
		if errors.As(err, &conflict) && r.stale(conflict.Conflict) {
			// The route in the way is one of Loomplane's own that this sync
			// removes anyway, such as the base entry of a path that lost its
			// "/*". Remove it now and push again, once.
			err = r.deleteRoute(ctx, conflict.Conflict)
			if err == nil {
				err = r.putRoute(ctx, d)
			}
		}
		switch {
		case errors.Is(err, ErrUnavailable):
			return err
		case errors.As(err, &conflict) && manual != nil:
			r.routeError(manualID, d.ID, fmt.Sprintf("Octopus already has its own route for %s on %s (%s). Remove it in the Octopus admin, or change this route.",
				manual.Path, hostLabelFor(manual.Hostname), conflict.Conflict))
		case err != nil:
			r.routeError(manualID, d.ID, fmt.Sprintf("Octopus didn't accept this route: %v", err))
		default:
			r.replaced[d.ID] = true
			r.put()
		}
	}
	return nil
}

func (r *syncRun) putRoute(ctx context.Context, d OctopusRoute) error {
	return r.admin.PutRoute(ctx, d.ID, RouteBody{
		Path: d.Path, Methods: slices.Clone(d.Methods), Hosts: slices.Clone(d.Hosts),
		Upstream: d.Upstream, StripPrefix: d.StripPrefix, Priority: d.Priority,
	})
}

// stale reports whether Octopus's route id is one Loomplane put there, no
// longer wants and hasn't removed yet in this sync.
func (r *syncRun) stale(id string) bool {
	live := r.liveRoutes[id]
	return strings.HasPrefix(id, "lp-") && !r.wanted[id] && !r.deleted[id] && live != nil && live.loomplanes()
}

// deleteRoute removes a stale route. The upstreams it sent to and that no
// FARP or Kubernetes route uses are Loomplane's, even when no sync recorded
// them, as after a restart brings back an older config file's routes.
func (r *syncRun) deleteRoute(ctx context.Context, id string) error {
	if err := r.admin.DeleteRoute(ctx, id); err != nil {
		return err
	}
	r.deleted[id] = true
	for name := range r.liveRoutes[id].upstreams {
		r.orphaned[name] = true
	}
	return nil
}

// removeStaleRoutes deletes the routes Loomplane put in Octopus that it no
// longer wants: an "lp-" id from the config file or the admin API. FARP and
// Kubernetes routes, and routes made in Octopus itself, are never touched.
func (r *syncRun) removeStaleRoutes(ctx context.Context) error {
	for _, id := range slices.Sorted(maps.Keys(r.liveRoutes)) {
		if !r.stale(id) {
			continue
		}
		err := r.deleteRoute(ctx, id)
		switch {
		case errors.Is(err, ErrUnavailable):
			return err
		case err != nil:
			r.problem("Octopus didn't remove route %s: %v", id, err)
		}
	}
	return nil
}

// removeStaleUpstreams deletes the upstreams Loomplane created that it no
// longer wants: the names it recorded as its own, and the ones only its
// removed routes sent to. An upstream that a FARP or Kubernetes route sends
// to is never deleted, whatever its name, and neither is one a route still
// sends to, such as a route Octopus refused to change. Loomplane keeps
// owning that one, to remove once nothing uses it.
func (r *syncRun) removeStaleUpstreams(ctx context.Context) error {
	wanted := map[string]bool{}
	for _, u := range r.desired.Upstreams {
		wanted[u.Name] = true
	}
	inUse, discovered := map[string]bool{}, map[string]bool{}
	for id, live := range r.liveRoutes {
		if r.replaced[id] || r.deleted[id] {
			continue
		}
		for name := range live.upstreams {
			inUse[name] = true
			if !live.loomplanes() {
				discovered[name] = true
			}
		}
	}
	owned := map[string]bool{}
	for _, name := range r.gateway.State.OwnedUpstreams {
		owned[name] = true
	}
	for name := range r.orphaned {
		if !discovered[name] {
			owned[name] = true
		}
	}
	for _, name := range slices.Sorted(maps.Keys(owned)) {
		if _, live := r.liveUpstreams[name]; wanted[name] || !live {
			continue
		}
		if inUse[name] {
			r.keepOwned = append(r.keepOwned, name)
			continue
		}
		err := r.admin.DeleteUpstream(ctx, name)
		switch {
		case errors.Is(err, ErrUnavailable):
			return err
		case err != nil:
			r.keepOwned = append(r.keepOwned, name)
			r.problem("Octopus didn't remove upstream %s: %v", name, err)
		}
	}
	return nil
}

// pushFarpBinding sets the hostnames FARP routes answer on when Octopus has
// others.
func (r *syncRun) pushFarpBinding(ctx context.Context) error {
	binding, err := r.admin.FarpBinding(ctx)
	switch {
	case errors.Is(err, ErrUnavailable):
		return err
	case err != nil:
		r.problem("Octopus didn't report its FARP hostnames: %v", err)
		return nil
	}
	if sameSet(lowered(binding.Hostnames), lowered(r.desired.FarpHostnames)) {
		return nil
	}
	err = r.admin.PutFarpBinding(ctx, slices.Clone(r.desired.FarpHostnames))
	switch {
	case errors.Is(err, ErrUnavailable):
		return err
	case err != nil:
		r.problem("Octopus didn't take the FARP hostnames: %v", err)
	}
	return nil
}

// liveRoute is one route as Octopus reports it: the entries sharing a
// route id, one per method and host.
type liveRoute struct {
	paths, upstreams, strips, origins map[string]bool
	// entries are "METHOD host", host "" for any host.
	entries map[string]bool
}

// groupRoutes groups Octopus's route entries by route id, or by entry id
// for an entry without one.
func groupRoutes(infos []RouteInfo) map[string]*liveRoute {
	out := map[string]*liveRoute{}
	for _, info := range infos {
		key := info.ID
		if info.RouteID != nil && *info.RouteID != "" {
			key = *info.RouteID
		}
		l := out[key]
		if l == nil {
			l = &liveRoute{paths: map[string]bool{}, upstreams: map[string]bool{}, strips: map[string]bool{}, origins: map[string]bool{}, entries: map[string]bool{}}
			out[key] = l
		}
		host, strip := "", ""
		if info.Host != nil {
			host = strings.ToLower(*info.Host)
		}
		if info.StripPrefix != nil {
			strip = *info.StripPrefix
		}
		l.paths[info.Path] = true
		l.upstreams[info.Upstream] = true
		l.strips[strip] = true
		l.origins[info.Origin] = true
		l.entries[strings.ToUpper(info.Method)+" "+host] = true
	}
	return out
}

// matches reports whether Octopus holds d as Loomplane would put it. The
// origin doesn't count: a config route and the same route put at runtime
// are one route. No methods is every method and no hosts is any host.
func (l *liveRoute) matches(d OctopusRoute) bool {
	methods := d.Methods
	if len(methods) == 0 {
		methods = methodOrder
	}
	hosts := lowered(d.Hosts)
	if len(hosts) == 0 {
		hosts = []string{""}
	}
	want := map[string]bool{}
	for _, m := range methods {
		for _, h := range hosts {
			want[strings.ToUpper(m)+" "+h] = true
		}
	}
	return onlyKey(l.paths, d.Path) && onlyKey(l.upstreams, d.Upstream) && onlyKey(l.strips, d.StripPrefix) && maps.Equal(l.entries, want)
}

// loomplanes reports whether every entry came from Loomplane's config file
// or the admin API.
func (l *liveRoute) loomplanes() bool {
	for origin := range l.origins {
		if origin != originConfig && origin != originAdmin {
			return false
		}
	}
	return true
}

func onlyKey(set map[string]bool, key string) bool { return len(set) == 1 && set[key] }

func sameInstances(live []UpstreamInstance, want []Instance) bool {
	a := make([]string, 0, len(live))
	for _, i := range live {
		a = append(a, fmt.Sprintf("%s|%s|%d", i.ID, i.Address, i.Port))
	}
	b := make([]string, 0, len(want))
	for _, i := range want {
		b = append(b, fmt.Sprintf("%s|%s|%d", i.ID, i.Host, i.Port))
	}
	return sameSet(a, b)
}

// sameSet compares a and b as sets, so nil and empty are the same.
func sameSet(a, b []string) bool {
	as, bs := map[string]bool{}, map[string]bool{}
	for _, s := range a {
		as[s] = true
	}
	for _, s := range b {
		bs[s] = true
	}
	return maps.Equal(as, bs)
}

func lowered(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.ToLower(s))
	}
	return out
}

func sortedUnion(a, b []string) []string {
	out := slices.Concat(a, b)
	slices.Sort(out)
	out = slices.Compact(out)
	if len(out) == 0 {
		return nil
	}
	return out
}
