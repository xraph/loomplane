package cloudinfra

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"reflect"
	"slices"
	"time"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/security"
)

type RouteVerification struct {
	DNS          bool      `json:"dns"`
	TLS          bool      `json:"tls"`
	Backend      bool      `json:"backend"`
	CheckedAt    time.Time `json:"checked_at"`
	CertExpires  time.Time `json:"cert_expires"`
	DNSError     string    `json:"dns_error,omitempty"`
	TLSError     string    `json:"tls_error,omitempty"`
	BackendError string    `json:"backend_error,omitempty"`
	CertState    string    `json:"cert_state,omitempty"` // issuing, ready or failed
}

// VerifyRoute queues a fresh durable check, including certificate renewal state.
func (s *Service) VerifyRoute(ctx context.Context, p security.Principal, project, id string) (*RouteOperation, error) {
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
	if route.Status == "removed" {
		return nil, ErrConflict
	}
	opID, err := randomID("routeop")
	if err != nil {
		return nil, err
	}
	op := &RouteOperation{ID: opID, Route: *route, Kind: "verify", Status: "pending", Attempt: 1, Revision: 1, Receipt: route.Receipt, Principal: p}
	if checkErr := st.AcceptRoute(ctx, op); checkErr != nil {
		return nil, checkErr
	}
	s.enqueueRoute(ctx, op)
	return op, nil
}

func (s *Service) ReconcileRoute(ctx context.Context, org, id string, heartbeat func([]byte) error) error {
	st, err := s.domainStore()
	if err != nil {
		return err
	}
	op, err := st.RouteOperation(ctx, org, id)
	if err != nil {
		return err
	}
	locks := []string{routeLockKey(op.Route)}
	if op.Source != nil && routeLockKey(*op.Source) != locks[0] {
		locks = append(locks, routeLockKey(*op.Source))
	}
	slices.Sort(locks)
	var locked func(int) error
	locked = func(index int) error {
		if index < len(locks) {
			return s.Store.WithOperationLock(ctx, locks[index], func() error { return locked(index + 1) })
		}
		return func() error {
			op, err = st.RouteOperation(ctx, org, id)
			if err != nil {
				return err
			}
			if op.Status == "preview" || op.Status == "succeeded" || op.Status == "failed" || op.Status == "cancelled" {
				return nil
			}
			if heartbeat != nil {
				if checkErr := heartbeat([]byte(`{"stage":"route_reconcile"}`)); checkErr != nil {
					return checkErr
				}
			}
			op.Status = "running"
			state, beginErr := st.BeginIngress(ctx, op)
			if beginErr != nil {
				return beginErr
			}
			err = s.executeRoute(ctx, st, op, state)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var notice *routeNoticeError
			switch {
			case errors.Is(err, ErrPending):
				op.Status, op.Message = "waiting", "DNS, certificate or backend verification is still pending."
			case err != nil:
				op.Status, op.Message = "failed", safeFailure(err)
			default:
				op.Status, op.Message = "succeeded", ""
			}
			if errors.As(err, &notice) {
				op.Message = notice.message
			}
			if saveErr := st.SaveRouteOperation(ctx, op); saveErr != nil {
				return saveErr
			}
			if errors.Is(err, ErrPending) {
				return ErrPending
			}
			return nil
		}()
	}
	return locked(0)
}

func (s *Service) executeRoute(ctx context.Context, st DomainStore, op *RouteOperation, state *IngressState) error {
	route := &op.Route
	// Accepted removal retains exact cleanup authority after a grant is revoked.
	if op.Kind != "remove" {
		if err := s.domainScope(ctx, op.Principal, route.ProjectID, true); err != nil {
			return err
		}
		switch op.Kind {
		case "release":
			current, err := st.Route(ctx, route.OrgID, route.ProjectID, route.ID)
			if err != nil {
				return err
			}
			if current.Revision != route.Revision || !reflect.DeepEqual(current.Binding, route.Binding) {
				return ErrConflict
			}
			if checkErr := st.ValidateRouteCleanup(ctx, *route); checkErr != nil {
				return checkErr
			}
		case "rollback":
			if err := st.ValidateRetainedRoute(ctx, *route); err != nil {
				return err
			}
		default:
			current, err := st.ResolveRouteBinding(ctx, route.OrgID, route.ProjectID, route.Input)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(current, route.Binding) {
				return ErrConflict
			}
		}
	}
	zone, err := st.Zone(ctx, route.OrgID, route.Input.ZoneID)
	if err != nil {
		return err
	}
	if op.Kind != "remove" && op.Kind != "release" {
		if !slices.Contains(zone.ProjectIDs, route.ProjectID) {
			return ErrNotFound
		}
		if checkErr := s.verifyZoneControl(ctx, zone); checkErr != nil {
			return checkErr
		}
	}
	if route.Binding.BackendName() == RouteBackendKubernetes {
		return s.executeKubernetesRoute(ctx, st, op, zone)
	}
	if op.Kind == "prepare" && op.Source != nil && op.Source.Binding.ServerID != op.Route.Binding.ServerID {
		if checkErr := s.prepareSourceIngress(ctx, st, op); checkErr != nil {
			return checkErr
		}
	}
	client := s.routeClient
	if op.Kind == "remove" || op.Kind == "release" {
		client = s.routeCleanupClient
	}
	c, err := client(ctx, *route)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx = server.WithEffectFence(ctx, server.EffectFence{Operation: route.OrgID + "/ingress/" + route.Binding.ServerID, Generation: op.Generation})
	if checkErr := c.ArmEffectFence(ctx, server.EffectFence{Operation: route.OrgID + "/ingress/" + route.Binding.ServerID, Generation: op.Generation}); checkErr != nil {
		return checkErr
	}
	if op.Kind == "release" {
		if checkErr := s.recoverHandoffConfig(ctx, st, c, op, state); checkErr != nil {
			return checkErr
		}
		if checkErr := s.clearInactivePreparation(ctx, st, c, op, state); checkErr != nil {
			return checkErr
		}
		// Cleanup may remove an accepted inactive probe reference, but cannot
		// replace the public backend or connect another backend.
		if state.Current == nil {
			return ErrConflict
		}
		route.Verification = s.verifyRouteDestination(ctx, c, *route, state, false)
		return s.releaseVerifiedSource(ctx, st, op, state)
	}
	if op.Kind == "remove" {
		if checkErr := s.recoverCancelledIngress(ctx, st, c, op, state); checkErr != nil {
			return checkErr
		}
	}
	if checkErr := s.reconcileRouteConfig(ctx, st, c, op, state, zone); checkErr != nil {
		return checkErr
	}
	if op.Kind != "verify" && op.Kind != "prepare" && op.Kind != "release" {
		if checkErr := s.applyRouteDNS(ctx, st, op, zone); checkErr != nil {
			return checkErr
		}
	}
	if op.Kind == "remove" {
		if checkErr := removeUnusedDNSToken(ctx, c, *route, state.Config); checkErr != nil {
			return checkErr
		}
		if checkErr := forgetDNSToken(ctx, st, op, state); checkErr != nil {
			return checkErr
		}
		if checkErr := server.DisconnectIngressBackend(ctx, c, state.Config, routeBackend(*route)); checkErr != nil {
			return checkErr
		}
		route.Status = "removed"
		route.UpdatedAt = s.now().UTC()
		return st.PublishRoute(ctx, op)
	}
	route.Verification = s.verifyRouteDestination(ctx, c, *route, state, op.Kind == "prepare")
	if op.Kind == "prepare" {
		if route.Verification.TLS && route.Verification.Backend {
			return nil
		}
		return ErrPending
	}
	route.Status = "waiting"
	if route.Verification.DNS && route.Verification.TLS && route.Verification.Backend {
		route.Status = "ready"
	}
	route.UpdatedAt = s.now().UTC()
	if checkErr := st.PublishRoute(ctx, op); checkErr != nil {
		return checkErr
	}
	if route.Status != "ready" {
		return ErrPending
	}
	return nil
}

func (s *Service) reconcileRouteConfig(ctx context.Context, st DomainStore, c *server.Client, op *RouteOperation, state *IngressState, zone *Zone) error {
	route := &op.Route
	if op.DesiredConfig != nil && op.AppliedConfig == nil && state.Current != nil {
		// Another hostname may have advanced the complete config after this
		// operation's apply was persisted but its receipt was lost.
		expected := slices.IndexFunc(op.DesiredConfig.Routes, func(r server.IngressRoute) bool { return r.ID == route.ID })
		current := slices.IndexFunc(state.Config.Routes, func(r server.IngressRoute) bool { return r.ID == route.ID })
		if (op.Kind == "remove" && current < 0) || (expected >= 0 && current >= 0 && reflect.DeepEqual(op.DesiredConfig.Routes[expected], state.Config.Routes[current])) {
			op.AppliedConfig = state.Current
			if err := st.SaveRouteOperation(ctx, op); err != nil {
				return err
			}
		}
	}
	if op.Kind == "verify" || op.AppliedConfig != nil {
		if state.Current == nil {
			return ErrConflict
		}
		if op.Kind == "verify" && isWildcard(route.Input.Hostname) {
			return s.refreshZoneToken(ctx, st, c, op, state, zone)
		}
		return nil
	}
	if op.DesiredConfig == nil {
		if state.Current == nil {
			snapshot, err := server.EnsureIngress(ctx, c, route.OrgID, route.Binding.ServerID)
			if err != nil {
				return err
			}
			state.Current = snapshot
			state.Config = server.IngressConfig{OrgID: route.OrgID, ServerID: route.Binding.ServerID, Version: "initial", Routes: []server.IngressRoute{}}
			initial, configErr := state.Config.JSON()
			if configErr != nil || !server.SameIngressConfig(initial, snapshot.Config) {
				return ErrConflict
			}
		}
		desired := state.Config
		desired.Routes = slices.Clone(state.Config.Routes)
		desired.Version = op.ID
		if op.Kind == "remove" {
			desired.Routes = slices.DeleteFunc(desired.Routes, func(r server.IngressRoute) bool { return r.ID == route.ID })
		} else {
			backend, err := server.ConnectIngressBackend(ctx, c, route.OrgID, route.Binding.ServerID, routeBackend(*route))
			if err != nil {
				return err
			}
			next := server.IngressRoute{ID: route.ID, Hostname: route.Input.Hostname, Backend: backend, BlockedPathPrefixes: slices.Clone(route.Input.BlockedPathPrefixes)}
			if isWildcard(route.Input.Hostname) {
				next.DNSChallenge = &server.IngressDNSChallenge{ZoneID: zone.ID, Zone: zone.Name}
			}
			if op.Kind == "prepare" && op.Source != nil && op.Source.Binding.ServerID == route.Binding.ServerID {
				index := slices.IndexFunc(desired.Routes, func(r server.IngressRoute) bool { return r.ID == route.ID })
				if index < 0 || desired.Routes[index].Backend.ContainerID != op.Source.Binding.ContainerID || desired.Routes[index].Backend.ReleaseID != op.Source.Binding.ReleaseID {
					return ErrConflict
				}
				next = desired.Routes[index]
				next.PreparedBackend = &backend
			}
			desired.Routes = slices.DeleteFunc(desired.Routes, func(r server.IngressRoute) bool { return r.ID == route.ID })
			desired.Routes = append(desired.Routes, next)
		}
		op.DesiredConfig, op.PreviousConfig = &desired, state.Current
		if err := st.SaveRouteOperation(ctx, op); err != nil {
			return err
		}
	}
	if op.Kind != "remove" && isWildcard(route.Input.Hostname) {
		if err := s.prepareWildcardIngress(ctx, st, c, op, state, zone); err != nil {
			return err
		}
	}
	actual, err := server.ReadIngress(ctx, c, route.OrgID, route.Binding.ServerID)
	if err != nil {
		return err
	}
	if err = adoptUpgradedIngress(ctx, st, c, op, state, actual); err != nil {
		return err
	}
	desired, err := op.DesiredConfig.JSON()
	if err != nil {
		return err
	}
	if !server.SameIngressConfig(desired, actual.Config) {
		actual, err = server.ApplyIngress(ctx, c, op.PreviousConfig, *op.DesiredConfig)
		if err != nil {
			return err
		}
	}
	state.Previous, state.Current, state.Config = op.PreviousConfig, actual, *op.DesiredConfig
	if checkErr := st.SaveIngress(ctx, op, state); checkErr != nil {
		return checkErr
	}
	op.AppliedConfig = actual
	return st.SaveRouteOperation(ctx, op)
}

// prepareWildcardIngress readies a server for a wildcard route's config:
// the DNS-capable proxy first, then the zone's token in its config volume.
func (s *Service) prepareWildcardIngress(ctx context.Context, st DomainStore, c *server.Client, op *RouteOperation, state *IngressState, zone *Zone) error {
	route := op.Route
	upgrade, err := server.IngressNeedsUpgrade(ctx, c, route.OrgID, route.Binding.ServerID)
	if err != nil {
		return err
	}
	if upgrade {
		if !server.IngressImagePinned() {
			return &routeNoticeError{err: unpublishedProxyImage, message: unpublishedProxyImage.Error()}
		}
		upgraded, upgradeErr := server.UpgradeIngress(ctx, c, route.OrgID, route.Binding.ServerID, op.PreviousConfig)
		if upgradeErr != nil {
			return upgradeErr
		}
		if checkErr := saveUpgradedIngress(ctx, st, op, state, upgraded); checkErr != nil {
			return checkErr
		}
	}
	// The config this op applies has Caddy load the file again.
	_, err = s.syncZoneToken(ctx, c, route, state, zone, true)
	return err
}

// syncZoneToken writes the zone's challenge token to the route's server
// after its connection passes the challenge checks again. Unless always,
// it writes only a token from a generation the server doesn't hold. It
// reports whether it wrote; the caller saves state.
func (s *Service) syncZoneToken(ctx context.Context, c *server.Client, route Route, state *IngressState, zone *Zone, always bool) (bool, error) {
	if !always {
		conn, _, err := s.Connections.Credential(ctx, zone.OrgID, zone.ChallengeConnectionID)
		if err != nil {
			return false, err
		}
		if held, ok := state.DNSTokens[zone.ID]; ok && held == conn.Generation {
			return false, nil
		}
	}
	token, generation, err := s.zoneChallengeCredential(ctx, zone)
	if err != nil {
		return false, &routeNoticeError{err: err, message: "The DNS challenge connection for " + zone.Name + " failed its checks, so Loomplane didn't put its token on the server: " + err.Error()}
	}
	if err = s.writeDNSToken(ctx, c, route.OrgID, route.Binding.ServerID, zone.ID, token); err != nil {
		return false, err
	}
	if state.DNSTokens == nil {
		state.DNSTokens = map[string]int64{}
	}
	state.DNSTokens[zone.ID] = generation
	return true, nil
}

// refreshZoneToken puts a replaced challenge token on the server during a
// wildcard route's check and has Caddy load its unchanged config again.
func (s *Service) refreshZoneToken(ctx context.Context, st DomainStore, c *server.Client, op *RouteOperation, state *IngressState, zone *Zone) error {
	wrote, err := s.syncZoneToken(ctx, c, op.Route, state, zone, false)
	if err != nil || !wrote {
		return err
	}
	reloaded, err := server.ReloadIngress(ctx, c, op.Route.OrgID, op.Route.Binding.ServerID, state.Current)
	if err != nil {
		return err
	}
	state.Current = reloaded
	return st.SaveIngress(ctx, op, state)
}

// adoptUpgradedIngress follows a proxy an upgrade replaced after its new
// snapshot was lost: the same config, now on the DNS-capable image.
func adoptUpgradedIngress(ctx context.Context, st DomainStore, c *server.Client, op *RouteOperation, state *IngressState, actual *server.IngressSnapshot) error {
	previous := op.PreviousConfig
	if previous == nil || actual.ContainerID == previous.ContainerID || !server.SameIngressConfig(actual.Config, previous.Config) {
		return nil
	}
	upgrade, err := server.IngressNeedsUpgrade(ctx, c, op.Route.OrgID, op.Route.Binding.ServerID)
	if err != nil {
		return err
	}
	if upgrade {
		if !server.IngressImagePinned() {
			return nil
		}
		// Only an upgrade replaces the proxy, and this one left its old
		// container behind. Finishing it removes that.
		if actual, err = server.UpgradeIngress(ctx, c, op.Route.OrgID, op.Route.Binding.ServerID, previous); err != nil {
			return err
		}
	}
	return saveUpgradedIngress(ctx, st, op, state, actual)
}

func saveUpgradedIngress(ctx context.Context, st DomainStore, op *RouteOperation, state *IngressState, upgraded *server.IngressSnapshot) error {
	op.PreviousConfig, state.Current = upgraded, upgraded
	if err := st.SaveIngress(ctx, op, state); err != nil {
		return err
	}
	return st.SaveRouteOperation(ctx, op)
}

// removeUnusedDNSToken deletes a removed wildcard's zone token once no
// route left on the server names that zone.
func removeUnusedDNSToken(ctx context.Context, c *server.Client, route Route, config server.IngressConfig) error {
	if !isWildcard(route.Input.Hostname) {
		return nil
	}
	for _, r := range config.Routes {
		if r.DNSChallenge != nil && r.DNSChallenge.ZoneID == route.Input.ZoneID {
			return nil
		}
	}
	return server.RemoveIngressDNSToken(ctx, c, route.OrgID, route.Binding.ServerID, route.Input.ZoneID)
}

// forgetDNSToken drops a removed token file's record.
func forgetDNSToken(ctx context.Context, st DomainStore, op *RouteOperation, state *IngressState) error {
	if _, ok := state.DNSTokens[op.Route.Input.ZoneID]; !ok || !isWildcard(op.Route.Input.Hostname) || slices.ContainsFunc(state.Config.Routes, func(r server.IngressRoute) bool {
		return r.DNSChallenge != nil && r.DNSChallenge.ZoneID == op.Route.Input.ZoneID
	}) {
		return nil
	}
	delete(state.DNSTokens, op.Route.Input.ZoneID)
	return st.SaveIngress(ctx, op, state)
}

func (s *Service) verifyRouteDestination(ctx context.Context, c *server.Client, route Route, state *IngressState, preparation bool) RouteVerification {
	out := RouteVerification{CheckedAt: s.now().UTC()}
	if !routeConfigMatches(route, state.Config, preparation) {
		out.BackendError = "The recorded proxy upstream does not match the accepted runtime."
		return out
	}
	// A wildcard is checked through one name under it.
	hostname := routeProbeHostname(route)
	addresses, err := s.domainResolver().LookupIPAddr(ctx, hostname)
	out.DNS = err == nil && len(addresses) > 0
	for _, address := range addresses {
		out.DNS = out.DNS && address.IP.Equal(net.ParseIP(route.Binding.Address))
	}
	if !out.DNS {
		out.DNSError = "DNS does not resolve exclusively to the accepted server address."
	}
	tlsCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 15 * time.Second}, Config: &tls.Config{ServerName: hostname, MinVersion: tls.VersionTLS12}}
	connection, err := dialer.DialContext(tlsCtx, "tcp", net.JoinHostPort(route.Binding.Address, "443"))
	if err == nil {
		if secured, ok := connection.(*tls.Conn); ok {
			certificates := secured.ConnectionState().PeerCertificates
			if len(certificates) > 0 {
				out.CertExpires = certificates[0].NotAfter
				out.TLS = out.CertExpires.After(s.now().Add(24 * time.Hour))
			}
		}
		_ = connection.Close()
	}
	if !out.TLS {
		out.TLSError = "A trusted certificate for this hostname with at least 24 hours remaining is not available."
	}
	probe := server.ProbeIngressBackend
	if preparation {
		probe = server.ProbeIngressPreparation
	}
	if err = probe(ctx, c, state.Config, state.Current, route.ID); err == nil {
		out.Backend = true
	} else {
		out.BackendError = "The accepted runtime instance and release did not serve a successful request through the recorded proxy configuration."
	}
	return out
}

// recoverCancelledIngress adopts only the cancelled operation's recorded remote
// effect. A newer persisted config wins; unknown remote edits remain conflicts.
func (s *Service) recoverCancelledIngress(ctx context.Context, st DomainStore, c *server.Client, op *RouteOperation, state *IngressState) error {
	intent := op.CancelledIngress
	if intent == nil || op.DesiredConfig != nil {
		return nil
	}
	actual, err := server.ReadIngress(ctx, c, op.Route.OrgID, op.Route.Binding.ServerID)
	if err != nil {
		return err
	}
	if state.Current != nil && server.SameIngressConfig(state.Current.Config, actual.Config) {
		return nil
	}
	desired, err := intent.Desired.JSON()
	if err != nil {
		return err
	}
	if !server.SameIngressConfig(desired, actual.Config) {
		initial := server.IngressConfig{OrgID: op.Route.OrgID, ServerID: op.Route.Binding.ServerID, Version: "initial"}
		raw, encodeErr := initial.JSON()
		if encodeErr == nil && state.Current == nil && server.SameIngressConfig(raw, actual.Config) {
			return nil
		}
		return ErrConflict
	}
	if state.Current != nil && (intent.Previous == nil || !server.SameIngressConfig(state.Current.Config, intent.Previous.Config)) {
		return ErrConflict
	}
	state.Config, state.Previous, state.Current = intent.Desired, intent.Previous, actual
	return st.SaveIngress(ctx, op, state)
}

func routeConfigMatches(route Route, config server.IngressConfig, preparation bool) bool {
	for _, r := range config.Routes {
		if r.ID != route.ID || r.Hostname != route.Input.Hostname {
			continue
		}
		// A hand-edited block list or challenge zone is drift like a changed upstream.
		if !slices.Equal(r.BlockedPathPrefixes, route.Input.BlockedPathPrefixes) || (r.DNSChallenge != nil) != isWildcard(route.Input.Hostname) || (r.DNSChallenge != nil && r.DNSChallenge.ZoneID != route.Input.ZoneID) {
			return false
		}
		backend := r.Backend
		if preparation && r.PreparedBackend != nil {
			backend = *r.PreparedBackend
		}
		backend.Address = ""
		return reflect.DeepEqual(backend, routeBackend(route))
	}
	return false
}

func (s *Service) releaseVerifiedSource(ctx context.Context, st DomainStore, op *RouteOperation, state *IngressState) error {
	v := op.Route.Verification
	if s.now().Before(op.NotBefore) || !v.DNS || !v.TLS || !v.Backend {
		return ErrPending
	}
	return s.releaseSourceIngress(ctx, st, op, state)
}
