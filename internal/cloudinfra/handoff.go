package cloudinfra

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// IngressTransition retains the complete source config before HTTP-01
// forwarding. Task 6 keeps the source workload until cutover and its TTL gate.
type IngressTransition struct {
	Desired  server.IngressConfig    `json:"desired"`
	Previous *server.IngressSnapshot `json:"previous"`
	Applied  *server.IngressSnapshot `json:"applied,omitempty"`
}

type HandoffInput struct {
	operationID string

	Revision           int64  `json:"revision"`
	DestinationCloudID string `json:"destination_cloud_id"`
}

// Kubernetes routes have no retained source to hand off from or to.
var errKubernetesHandoff = &provider.UnsupportedCapabilityError{Provider: "kubernetes", Capability: provider.IngressManagement}

// PrepareRouteHandoff runs after the destination binding is committed, while
// the deployment runner retains the previous instance in Replaced.
func (s *Service) PrepareRouteHandoff(ctx context.Context, p security.Principal, project, id string, in HandoffInput) (*RouteOperation, error) {
	if project == "" {
		return nil, ErrInvalid
	}
	if err := s.domainScope(ctx, p, project, true); err != nil {
		return nil, err
	}
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	source, err := st.Route(ctx, p.OrgID, project, id)
	if err != nil {
		return nil, err
	}
	if source.Binding.BackendName() == RouteBackendKubernetes {
		return nil, errKubernetesHandoff
	}
	if source.Revision != in.Revision || source.Status != "ready" {
		return nil, ErrConflict
	}
	input := source.Input
	input.CloudID = in.DestinationCloudID
	binding, err := st.ResolveRouteBinding(ctx, p.OrgID, project, input)
	if err != nil {
		return nil, err
	}
	if binding.BackendName() == RouteBackendKubernetes {
		return nil, errKubernetesHandoff
	}
	if reflect.DeepEqual(binding, source.Binding) {
		return nil, ErrInvalid
	}
	if checkErr := st.ValidateRetainedRoute(ctx, *source); checkErr != nil {
		return nil, checkErr
	}
	zone, err := st.Zone(ctx, p.OrgID, input.ZoneID)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(zone.ProjectIDs, project) {
		return nil, ErrNotFound
	}
	if checkErr := s.verifyZoneControl(ctx, zone); checkErr != nil {
		return nil, checkErr
	}
	desired, err := routeDNSRecord(zone, input.Hostname, binding.Address, source.ID)
	if err != nil {
		return nil, err
	}
	if source.Receipt != nil {
		if desired.Type != source.Receipt.Type {
			return nil, ErrInvalid
		}
		desired.ID, desired.Owner = source.Receipt.ID, source.Receipt.Owner
	}
	destination := *source
	destination.Input, destination.Binding = input, binding
	opID, err := randomID("routeop")
	if err != nil {
		return nil, err
	}
	if in.operationID != "" {
		opID = in.operationID
	}
	op := &RouteOperation{ID: opID, Kind: "prepare", Status: "pending", Attempt: 1, Revision: 1, Route: destination, Source: source, DesiredRecord: desired, PreviousReceipt: source.Receipt, Principal: p}
	if checkErr := st.AcceptRoute(ctx, op); checkErr != nil {
		return nil, checkErr
	}
	s.domainAudit(ctx, p, "domain.route."+op.Kind, op.ID, op.Route.ProjectID)
	s.enqueueRoute(ctx, op)
	return op, nil
}

// CutoverRoute uses the prepared destination and the source's exact DNS receipt.
// An unknown provider outcome remains the same operation and reconciles by owner.
func (s *Service) CutoverRoute(ctx context.Context, p security.Principal, project, preparedID string) (*RouteOperation, error) {
	st, prepared, err := s.routeOperation(ctx, p, project, preparedID, true)
	if err != nil {
		return nil, err
	}
	if prepared.Kind != "prepare" || prepared.Status != "succeeded" || prepared.Source == nil || prepared.AppliedConfig == nil {
		return nil, ErrConflict
	}
	current, err := st.Route(ctx, p.OrgID, project, prepared.Route.ID)
	if err != nil {
		return nil, err
	}
	if current.Revision != prepared.Source.Revision || !reflect.DeepEqual(current.Binding, prepared.Source.Binding) {
		return nil, ErrConflict
	}
	opID, err := randomID("routeop")
	if err != nil {
		return nil, err
	}
	op := &RouteOperation{ID: opID, Kind: "cutover", Status: "pending", Attempt: 1, Revision: 1, Route: prepared.Route, Source: prepared.Source, HandoffID: prepared.ID, DesiredRecord: prepared.DesiredRecord, PreviousReceipt: prepared.PreviousReceipt, DesiredConfig: prepared.DesiredConfig, AppliedConfig: prepared.AppliedConfig, Principal: p}
	if op.Source.Binding.ServerID == op.Route.Binding.ServerID {
		op.DesiredConfig, op.AppliedConfig = nil, nil
	}
	if checkErr := st.AcceptRoute(ctx, op); checkErr != nil {
		return nil, checkErr
	}
	s.domainAudit(ctx, p, "domain.route."+op.Kind, op.ID, op.Route.ProjectID)
	s.enqueueRoute(ctx, op)
	return op, nil
}

// RollbackRoute returns DNS to the retained source using the latest cutover
// receipt. It does not delete either workload or claim successful public DNS.
func (s *Service) RollbackRoute(ctx context.Context, p security.Principal, project, cutoverID string) (*RouteOperation, error) {
	st, cutover, err := s.routeOperation(ctx, p, project, cutoverID, true)
	if err != nil {
		return nil, err
	}
	if cutover.Kind != "cutover" || cutover.Source == nil || (cutover.Status != "succeeded" && cutover.Status != "waiting" && cutover.Status != "failed") {
		return nil, ErrConflict
	}
	if cutover.DNSAttempted && cutover.Receipt == nil {
		return nil, ErrConflict
	}
	source := *cutover.Source
	if checkErr := st.ValidateRetainedRoute(ctx, source); checkErr != nil {
		return nil, checkErr
	}
	current, err := st.Route(ctx, p.OrgID, project, source.ID)
	if err != nil {
		return nil, err
	}
	source.Revision = current.Revision
	zone, err := st.Zone(ctx, p.OrgID, source.Input.ZoneID)
	if err != nil {
		return nil, err
	}
	record, err := routeDNSRecord(zone, source.Input.Hostname, source.Binding.Address, source.ID)
	if err != nil {
		return nil, err
	}
	receipt := cutover.Receipt
	if !cutover.DNSAttempted && receipt == nil {
		receipt = cutover.PreviousReceipt
	}
	if receipt != nil {
		record.ID, record.Owner = receipt.ID, receipt.Owner
		// A failed pre-DNS cutover leaves the source record unchanged, including TTL.
		if !cutover.DNSAttempted && receipt.Value == source.Binding.Address {
			record = *receipt
		}
	}
	opID, err := randomID("routeop")
	if err != nil {
		return nil, err
	}
	op := &RouteOperation{ID: opID, Kind: "rollback", Status: "pending", Attempt: 1, Revision: 1, Route: source, Source: &cutover.Route, HandoffID: cutover.HandoffID, DesiredRecord: record, PreviousReceipt: receipt, Principal: p}
	err = s.Store.WithOperationLock(ctx, routeLockKey(cutover.Route), func() error { return st.AcceptRoute(ctx, op) })
	if err != nil {
		return nil, err
	}
	s.domainAudit(ctx, p, "domain.route."+op.Kind, op.ID, op.Route.ProjectID)
	s.enqueueRoute(ctx, op)
	return op, nil
}

func (s *Service) prepareSourceIngress(ctx context.Context, st DomainStore, op *RouteOperation) error {
	source := op.Source
	if source == nil {
		return ErrInvalid
	}
	if err := st.ValidateRetainedRoute(ctx, *source); err != nil {
		return err
	}
	state, err := st.BeginServerIngress(ctx, source.OrgID, source.Binding.ServerID)
	if err != nil {
		return err
	}
	c, err := s.routeClient(ctx, *source)
	if err != nil {
		return err
	}
	defer c.Close()
	f := server.EffectFence{Operation: source.OrgID + "/ingress/" + source.Binding.ServerID, Generation: state.Generation}
	ctx = server.WithEffectFence(ctx, f)
	if checkErr := c.ArmEffectFence(ctx, f); checkErr != nil {
		return checkErr
	}
	if state.Current == nil {
		return ErrConflict
	}
	if op.SourceTransition == nil {
		desired := state.Config
		desired.Routes = slices.Clone(state.Config.Routes)
		desired.Version = op.ID + "-source"
		index := slices.IndexFunc(desired.Routes, func(r server.IngressRoute) bool { return r.ID == source.ID })
		if index < 0 || desired.Routes[index].Backend.ContainerID != source.Binding.ContainerID || desired.Routes[index].Backend.ReleaseID != source.Binding.ReleaseID {
			return ErrConflict
		}
		// A wildcard's certificate comes through DNS-01 on the destination.
		if desired.Routes[index].DNSChallenge == nil {
			desired.Routes[index].ChallengeAddress = op.Route.Binding.Address
		}
		op.SourceTransition = &IngressTransition{Desired: desired, Previous: state.Current}
		if checkErr := st.SaveRouteOperation(ctx, op); checkErr != nil {
			return checkErr
		}
	}
	transition := op.SourceTransition
	expected := slices.IndexFunc(transition.Desired.Routes, func(r server.IngressRoute) bool { return r.ID == source.ID })
	current := slices.IndexFunc(state.Config.Routes, func(r server.IngressRoute) bool { return r.ID == source.ID })
	if expected >= 0 && current >= 0 && reflect.DeepEqual(transition.Desired.Routes[expected], state.Config.Routes[current]) {
		transition.Applied = state.Current
		if checkErr := st.SaveRouteOperation(ctx, op); checkErr != nil {
			return checkErr
		}
		return server.ProbeIngressBackend(ctx, c, state.Config, state.Current, source.ID)
	}
	desired, err := transition.Desired.JSON()
	if err != nil {
		return err
	}
	actual, err := server.ReadIngress(ctx, c, source.OrgID, source.Binding.ServerID)
	if err != nil {
		return err
	}
	if !server.SameIngressConfig(desired, actual.Config) {
		actual, err = server.ApplyIngress(ctx, c, transition.Previous, transition.Desired)
		if err != nil {
			return err
		}
	}
	state.Previous, state.Current, state.Config = transition.Previous, actual, transition.Desired
	if checkErr := st.SaveServerIngress(ctx, source.OrgID, source.Binding.ServerID, state); checkErr != nil {
		return checkErr
	}
	transition.Applied = actual
	if checkErr := st.SaveRouteOperation(ctx, op); checkErr != nil {
		return checkErr
	}
	return server.ProbeIngressBackend(ctx, c, state.Config, state.Current, source.ID)
}

// ReleaseRouteHandoff removes the inactive proxy route after the caller's
// reviewed DNS drain interval. The workload remains for Task 6 cleanup.
func (s *Service) ReleaseRouteHandoff(ctx context.Context, p security.Principal, project, preparedID string, drainSeconds int) (*RouteOperation, error) {
	if drainSeconds < 60 || drainSeconds > 86400 {
		return nil, ErrInvalid
	}
	st, prepared, err := s.routeOperation(ctx, p, project, preparedID, true)
	if err != nil {
		return nil, err
	}
	if prepared.Kind != "prepare" || prepared.Source == nil {
		return nil, ErrConflict
	}
	current, err := st.Route(ctx, p.OrgID, project, prepared.Route.ID)
	if err != nil {
		return nil, err
	}
	inactive := prepared.Source
	if reflect.DeepEqual(current.Binding, prepared.Source.Binding) {
		inactive = &prepared.Route
	} else if !reflect.DeepEqual(current.Binding, prepared.Route.Binding) {
		return nil, ErrConflict
	}
	if current.Status != "ready" {
		return nil, ErrConflict
	}
	drainSeconds = max(drainSeconds, prepared.Source.Input.ExternalTTLSeconds, prepared.Route.Input.ExternalTTLSeconds, current.Input.ExternalTTLSeconds)
	if current.Receipt != nil {
		drainSeconds = max(drainSeconds, current.Receipt.TTL)
	}
	if prepared.PreviousReceipt != nil {
		drainSeconds = max(drainSeconds, prepared.PreviousReceipt.TTL)
	}
	opID, err := randomID("routeop")
	if err != nil {
		return nil, err
	}
	op := &RouteOperation{ID: opID, Kind: "release", Status: "pending", Attempt: 1, Revision: 1, Route: *current, Source: inactive, HandoffID: prepared.ID, NotBefore: current.UpdatedAt.Add(time.Duration(drainSeconds) * time.Second), Receipt: current.Receipt, Principal: p}
	if checkErr := st.AcceptRoute(ctx, op); checkErr != nil {
		return nil, checkErr
	}
	s.domainAudit(ctx, p, "domain.route."+op.Kind, op.ID, op.Route.ProjectID)
	s.enqueueRoute(ctx, op)
	return op, nil
}

func (s *Service) releaseSourceIngress(ctx context.Context, st DomainStore, op *RouteOperation, current *IngressState) error {
	source := op.Source
	if source == nil {
		return ErrInvalid
	}
	if source.Binding.ServerID == op.Route.Binding.ServerID {
		if ingressReferences(current.Config, routeBackend(*source)) {
			return ErrConflict
		}
		c, err := s.routeCleanupClient(ctx, op.Route)
		if err != nil {
			return err
		}
		defer c.Close()
		f := server.EffectFence{Operation: op.Route.OrgID + "/ingress/" + op.Route.Binding.ServerID, Generation: op.Generation}
		if checkErr := server.DisconnectIngressBackend(server.WithEffectFence(ctx, f), c, current.Config, routeBackend(*source)); checkErr != nil {
			return checkErr
		}
		return st.CompleteHandoff(ctx, op)
	}
	state, err := st.BeginServerIngress(ctx, source.OrgID, source.Binding.ServerID)
	if err != nil {
		return err
	}
	c, err := s.routeCleanupClient(ctx, *source)
	if err != nil {
		return err
	}
	defer c.Close()
	fence := server.EffectFence{Operation: source.OrgID + "/ingress/" + source.Binding.ServerID, Generation: state.Generation}
	ctx = server.WithEffectFence(ctx, fence)
	if checkErr := c.ArmEffectFence(ctx, fence); checkErr != nil {
		return checkErr
	}
	if state.Current == nil {
		actual, readErr := server.ReadIngress(ctx, c, source.OrgID, source.Binding.ServerID)
		if errors.Is(readErr, server.ErrMissing) {
			return st.CompleteHandoff(ctx, op)
		}
		if readErr != nil {
			return readErr
		}
		initial := server.IngressConfig{OrgID: source.OrgID, ServerID: source.Binding.ServerID, Version: "initial"}
		raw, encodeErr := initial.JSON()
		if encodeErr != nil {
			return encodeErr
		}
		if server.SameIngressConfig(raw, actual.Config) {
			state.Config, state.Current = initial, actual
		} else {
			prepared, lookupErr := st.RouteOperation(ctx, source.OrgID, op.HandoffID)
			if lookupErr != nil {
				return lookupErr
			}
			if prepared.DesiredConfig == nil || !reflect.DeepEqual(prepared.Route.Binding, source.Binding) {
				return ErrConflict
			}
			raw, encodeErr = prepared.DesiredConfig.JSON()
			if encodeErr != nil {
				return encodeErr
			}
			if !server.SameIngressConfig(raw, actual.Config) {
				return ErrConflict
			}
			state.Config, state.Current = *prepared.DesiredConfig, actual
		}
		if checkErr := st.SaveServerIngress(ctx, source.OrgID, source.Binding.ServerID, state); checkErr != nil {
			return checkErr
		}
	}
	index := slices.IndexFunc(state.Config.Routes, func(r server.IngressRoute) bool { return r.ID == source.ID })
	if index >= 0 {
		if state.Config.Routes[index].Backend.ContainerID != source.Binding.ContainerID {
			return ErrConflict
		}
		if op.SourceTransition == nil {
			desired := state.Config
			desired.Routes = slices.Clone(state.Config.Routes)
			desired.Routes = slices.Delete(desired.Routes, index, index+1)
			desired.Version = op.ID + "-retired"
			op.SourceTransition = &IngressTransition{Desired: desired, Previous: state.Current}
			if checkErr := st.SaveRouteOperation(ctx, op); checkErr != nil {
				return checkErr
			}
		}
		transition := op.SourceTransition
		raw, encodeErr := transition.Desired.JSON()
		if encodeErr != nil {
			return encodeErr
		}
		actual, readErr := server.ReadIngress(ctx, c, source.OrgID, source.Binding.ServerID)
		if readErr != nil {
			return readErr
		}
		if !server.SameIngressConfig(raw, actual.Config) {
			actual, err = server.ApplyIngress(ctx, c, transition.Previous, transition.Desired)
			if err != nil {
				return err
			}
		}
		state.Previous, state.Current, state.Config = transition.Previous, actual, transition.Desired
		if checkErr := st.SaveServerIngress(ctx, source.OrgID, source.Binding.ServerID, state); checkErr != nil {
			return checkErr
		}
	}
	if ingressReferences(state.Config, routeBackend(*source)) {
		return ErrConflict
	}
	if checkErr := removeUnusedDNSToken(ctx, c, *source, state.Config); checkErr != nil {
		return checkErr
	}
	if checkErr := server.DisconnectIngressBackend(ctx, c, state.Config, routeBackend(*source)); checkErr != nil {
		return checkErr
	}
	return st.CompleteHandoff(ctx, op)
}

// recoverHandoffConfig adopts only an exact persisted handoff intent after a
// proxy response or database receipt was lost. Unrelated config remains a CAS conflict.
func (s *Service) recoverHandoffConfig(ctx context.Context, st DomainStore, c *server.Client, op *RouteOperation, state *IngressState) error {
	prepared, err := st.RouteOperation(ctx, op.Route.OrgID, op.HandoffID)
	if err != nil {
		return err
	}
	var desired *server.IngressConfig
	var previous *server.IngressSnapshot
	if prepared.SourceTransition != nil && prepared.SourceTransition.Desired.ServerID == op.Route.Binding.ServerID {
		desired, previous = &prepared.SourceTransition.Desired, prepared.SourceTransition.Previous
	} else if prepared.DesiredConfig != nil && prepared.DesiredConfig.ServerID == op.Route.Binding.ServerID {
		desired, previous = prepared.DesiredConfig, prepared.PreviousConfig
	}
	if desired == nil {
		return nil
	}
	actual, err := server.ReadIngress(ctx, c, op.Route.OrgID, op.Route.Binding.ServerID)
	if err != nil {
		return err
	}
	raw, err := desired.JSON()
	if err != nil {
		return err
	}
	if !server.SameIngressConfig(raw, actual.Config) {
		return nil
	}
	state.Previous, state.Current, state.Config = previous, actual, *desired
	return st.SaveIngress(ctx, op, state)
}

func ingressReferences(config server.IngressConfig, backend server.IngressBackend) bool {
	for _, r := range config.Routes {
		if server.IngressNetwork(r.Backend) == server.IngressNetwork(backend) || (r.PreparedBackend != nil && server.IngressNetwork(*r.PreparedBackend) == server.IngressNetwork(backend)) {
			return true
		}
	}
	return false
}

// clearInactivePreparation removes only the accepted candidate probe reference.
// It preserves the public upstream and unrelated routes even without a grant.
func (s *Service) clearInactivePreparation(ctx context.Context, st DomainStore, c *server.Client, op *RouteOperation, state *IngressState) error {
	if op.Source == nil || op.Source.Binding.ServerID != op.Route.Binding.ServerID {
		return nil
	}
	if state.Current == nil {
		return ErrConflict
	}
	actual, err := server.ReadIngress(ctx, c, op.Route.OrgID, op.Route.Binding.ServerID)
	if err != nil {
		return err
	}
	if !server.SameIngressConfig(state.Current.Config, actual.Config) {
		// A completed cleanup can lose its database receipt. Only its exact saved
		// intention and expected predecessor establish recovery authority.
		if op.DesiredConfig == nil || op.PreviousConfig == nil || !server.SameIngressConfig(state.Current.Config, op.PreviousConfig.Config) {
			return ErrConflict
		}
		raw, encodeErr := op.DesiredConfig.JSON()
		if encodeErr != nil || !server.SameIngressConfig(raw, actual.Config) {
			return ErrConflict
		}
		state.Config, state.Previous, state.Current = *op.DesiredConfig, op.PreviousConfig, actual
		return st.SaveIngress(ctx, op, state)
	}
	if !routeConfigMatches(op.Route, state.Config, false) {
		return ErrConflict
	}
	index := slices.IndexFunc(state.Config.Routes, func(r server.IngressRoute) bool { return r.ID == op.Route.ID })
	candidate := state.Config.Routes[index].PreparedBackend
	if candidate == nil {
		return nil
	}
	accepted := *candidate
	accepted.Address = ""
	if !reflect.DeepEqual(accepted, routeBackend(*op.Source)) {
		return ErrConflict
	}
	desired := state.Config
	desired.Routes = slices.Clone(state.Config.Routes)
	desired.Routes[index].PreparedBackend = nil
	desired.Version = op.ID + "-abandoned"
	op.DesiredConfig, op.PreviousConfig = &desired, actual
	if checkpointErr := st.SaveRouteOperation(ctx, op); checkpointErr != nil {
		return checkpointErr
	}
	applied, err := server.ApplyIngress(ctx, c, actual, desired)
	if err != nil {
		return err
	}
	state.Config, state.Previous, state.Current = desired, actual, applied
	if saveErr := st.SaveIngress(ctx, op, state); saveErr != nil {
		return saveErr
	}
	op.AppliedConfig = applied
	return st.SaveRouteOperation(ctx, op)
}
