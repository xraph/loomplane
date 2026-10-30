package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type EdgeState struct {
	RouteID           string              `json:"route_id"`
	OrgID             string              `json:"org_id"`
	ProjectID         string              `json:"project_id"`
	Revision          int64               `json:"revision"`
	Policy            provider.EdgePolicy `json:"policy"`
	Status            string              `json:"status"`
	Message           string              `json:"message"`
	Kind              string              `json:"kind"`
	URLs              []string            `json:"urls,omitempty"`
	RequestedAt       time.Time           `json:"requested_at"`
	AppliedAt         time.Time           `json:"applied_at"`
	LastPurgeAt       time.Time           `json:"last_purge_at"`
	LastDeploymentID  string              `json:"last_deployment_id,omitempty"`
	InvalidationError string              `json:"invalidation_error,omitempty"`
	Prior             *provider.DNSRecord `json:"prior,omitempty"`
	Desired           *provider.DNSRecord `json:"desired,omitempty"`
	Principal         security.Principal  `json:"-"`
}

type EdgeInput struct {
	Revision int64               `json:"revision"`
	Policy   provider.EdgePolicy `json:"policy"`
}

type EdgePurgeInput struct {
	Revision int64    `json:"revision"`
	URLs     []string `json:"urls"`
}

type EdgeInvalidation struct{ Org, Project, Route, Deployment string }

type EdgeStore interface {
	Edge(context.Context, string, string) (*EdgeState, error)
	AcceptEdge(context.Context, *Route, *EdgeState) error
	SaveEdge(context.Context, *EdgeState) error
	PublishEdge(context.Context, *Route, *EdgeState) error
	PendingEdges(context.Context) ([]*EdgeState, error)
	QueueEdgeInvalidations(context.Context) error
	EdgeInvalidations(context.Context) ([]EdgeInvalidation, error)
	FinishEdgeInvalidation(context.Context, EdgeInvalidation, string) error
}

func (s *Service) edgeStore() (EdgeStore, error) {
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	edge, ok := st.(EdgeStore)
	if !ok {
		return nil, ErrInvalid
	}
	return edge, nil
}

func (s *Service) GetEdge(ctx context.Context, p security.Principal, project, routeID string) (*EdgeState, error) {
	if project == "" {
		return nil, ErrInvalid
	}
	if checkErr := s.domainScope(ctx, p, project, false); checkErr != nil {
		return nil, checkErr
	}
	ds, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	if _, err = ds.Route(ctx, p.OrgID, project, routeID); err != nil {
		return nil, err
	}
	st, err := s.edgeStore()
	if err != nil {
		return nil, err
	}
	state, err := st.Edge(ctx, p.OrgID, routeID)
	if errors.Is(err, ErrNotFound) {
		return &EdgeState{RouteID: routeID, OrgID: p.OrgID, ProjectID: project, Status: "disabled", Policy: provider.EdgePolicy{Rules: []provider.EdgeRule{}}}, nil
	}
	return state, err
}

func (s *Service) UpdateEdge(ctx context.Context, p security.Principal, project, routeID string, in EdgeInput) (*EdgeState, error) {
	if in.Policy.Rules == nil {
		in.Policy.Rules = []provider.EdgeRule{}
	}
	if checkErr := in.Policy.Validate(); checkErr != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, checkErr)
	}
	return s.acceptEdge(ctx, p, project, routeID, in.Revision, "configure", in.Policy, nil)
}

func (s *Service) PurgeEdge(ctx context.Context, p security.Principal, project, routeID string, in EdgePurgeInput) (*EdgeState, error) {
	return s.acceptEdge(ctx, p, project, routeID, in.Revision, "purge", provider.EdgePolicy{}, in.URLs)
}

func (s *Service) RetryEdge(ctx context.Context, p security.Principal, project, routeID string, revision int64) (*EdgeState, error) {
	return s.acceptEdge(ctx, p, project, routeID, revision, "retry", provider.EdgePolicy{}, nil)
}

func (s *Service) acceptEdge(ctx context.Context, p security.Principal, project, routeID string, revision int64, kind string, policy provider.EdgePolicy, urls []string) (*EdgeState, error) {
	if project == "" {
		return nil, ErrInvalid
	}
	if checkErr := s.domainScope(ctx, p, project, true); checkErr != nil {
		return nil, checkErr
	}
	ds, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	route, err := ds.Route(ctx, p.OrgID, project, routeID)
	if err != nil {
		return nil, err
	}
	state, err := s.GetEdge(ctx, p, project, routeID)
	if err != nil {
		return nil, err
	}
	if state.Revision != revision || state.Status == "pending" {
		return nil, ErrConflict
	}
	recoverDisable := state.Status == "failed" && kind == "configure" && !policy.Enabled
	if state.Status == "failed" && kind != "retry" && !recoverDisable {
		return nil, fmt.Errorf("%w: retry the unfinished CDN operation first", ErrConflict)
	}
	zone, adapter, cred, err := s.edgeAuthority(ctx, p, *route)
	if err != nil {
		return nil, err
	}
	if route.Status != "ready" || route.Receipt == nil || isWildcard(route.Input.Hostname) {
		return nil, fmt.Errorf("%w: CDN needs a ready exact hostname on a managed Cloudflare zone", ErrInvalid)
	}
	switch kind {
	case "retry":
		if state.Status != "failed" {
			return nil, ErrConflict
		}
	case "purge":
		if !state.Policy.Enabled {
			return nil, ErrInvalid
		}
		if checkErr := provider.ValidateEdgePurge(route.Input.Hostname, urls); checkErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalid, checkErr)
		}
		state.Kind, state.URLs = kind, slices.Clone(urls)
	case "configure":
		// A failed attempt may have changed DNS without saving its receipt.
		// Recovery may disable it only after reading one of the two recorded
		// states back, never adopting an unrelated provider record.
		if recoverDisable {
			edge, ok := adapter.(provider.EdgeAdapter)
			if !ok {
				return nil, ErrInvalid
			}
			matched := false
			for _, candidate := range []*provider.DNSRecord{state.Desired, state.Prior} {
				if candidate != nil && edge.VerifyEdgeDNS(ctx, cred, zone.ProviderZone, *candidate) == nil {
					candidateReceipt := *candidate
					route.Receipt, matched = &candidateReceipt, true
					break
				}
			}
			if !matched {
				return nil, provider.ErrInfrastructureDrift
			}
		}
		state.Kind, state.Policy, state.URLs = kind, policy, nil
		prior, desired := *route.Receipt, *route.Receipt
		desired.Proxied = policy.Enabled
		desired.TTL = 60
		if policy.Enabled {
			desired.TTL = 300
		}
		state.Prior, state.Desired = &prior, &desired
	default:
		return nil, ErrInvalid
	}
	state.Status, state.Message, state.Principal, state.RequestedAt = "pending", "", p, s.now().UTC()
	st, err := s.edgeStore()
	if err != nil {
		return nil, err
	}
	err = s.Store.WithOperationLock(ctx, routeLockKey(*route), func() error { return st.AcceptEdge(ctx, route, state) })
	if err != nil {
		return nil, err
	}
	s.domainAudit(ctx, p, "domain.edge."+kind, routeID, project)
	return state, nil
}

func (s *Service) edgeAuthority(ctx context.Context, p security.Principal, route Route) (*Zone, provider.DNSAdapter, provider.InfrastructureCredential, error) {
	var empty provider.InfrastructureCredential
	if s.CapacityPrincipal != nil {
		if checkErr := s.CapacityPrincipal.ValidatePrincipal(ctx, p); checkErr != nil {
			return nil, nil, empty, checkErr
		}
	}
	if checkErr := s.domainScope(ctx, p, route.ProjectID, true); checkErr != nil {
		return nil, nil, empty, checkErr
	}
	ds, err := s.domainStore()
	if err != nil {
		return nil, nil, empty, err
	}
	zone, err := s.linkedZone(ctx, ds, route.OrgID, route.Input.ZoneID)
	if err != nil {
		return nil, nil, empty, err
	}
	if zone.Mode != "managed" || zone.Provider != "cloudflare" || !slices.Contains(zone.ProjectIDs, route.ProjectID) {
		return nil, nil, empty, fmt.Errorf("%w: use a managed Cloudflare domain granted to this project", ErrInvalid)
	}
	if checkErr := s.verifyZoneControl(ctx, zone); checkErr != nil {
		return nil, nil, empty, checkErr
	}
	adapter, cred, err := s.zoneCredentialFor(ctx, zone, true)
	if err != nil {
		return nil, nil, empty, err
	}
	if _, ok := adapter.(provider.EdgeAdapter); !ok {
		return nil, nil, empty, ErrInvalid
	}
	return zone, adapter, cred, nil
}

func (s *Service) ReconcileEdge(ctx context.Context, org, routeID string) error {
	st, err := s.edgeStore()
	if err != nil {
		return err
	}
	state, err := st.Edge(ctx, org, routeID)
	if err != nil {
		return err
	}
	ds, err := s.domainStore()
	if err != nil {
		return err
	}
	route, err := ds.Route(ctx, org, state.ProjectID, routeID)
	if err != nil {
		return err
	}
	return s.Store.WithOperationLock(ctx, routeLockKey(*route), func() error {
		state, err = st.Edge(ctx, org, routeID)
		if err != nil || state.Status != "pending" {
			return err
		}
		route, err = ds.Route(ctx, org, state.ProjectID, routeID)
		if err != nil {
			return err
		}
		applyErr := s.applyEdge(ctx, route, state)
		if applyErr != nil {
			state.Status, state.Message = "failed", truncate(applyErr.Error(), 500)
			return errors.Join(applyErr, st.SaveEdge(ctx, state))
		}
		state.Status, state.Message = "ready", ""
		if !state.Policy.Enabled {
			state.Status = "disabled"
		}
		state.LastPurgeAt = s.now().UTC()
		if state.Kind == "configure" {
			state.AppliedAt = s.now().UTC()
		}
		return st.PublishEdge(ctx, route, state)
	})
}

func (s *Service) applyEdge(ctx context.Context, route *Route, state *EdgeState) error {
	zone, dns, cred, err := s.edgeAuthority(ctx, state.Principal, *route)
	if err != nil {
		return err
	}
	edge, ok := dns.(provider.EdgeAdapter)
	if !ok {
		return ErrInvalid
	}
	// The provider zone lock also orders ruleset creation by different projects.
	return s.Store.WithOperationLock(ctx, "edge-zone/"+zone.AccountID+"/"+zone.ProviderZone.ID, func() error {
		if state.Kind == "purge" {
			return edge.PurgeEdge(ctx, cred, zone.ProviderZone, route.Input.Hostname, state.URLs)
		}
		if state.Prior == nil || state.Desired == nil || route.Status != "ready" {
			return ErrConflict
		}
		if route.Receipt == nil || (route.Receipt.ID != state.Prior.ID || route.Receipt.Owner != state.Prior.Owner || route.Receipt.Value != state.Prior.Value || route.Receipt.Name != state.Prior.Name) {
			return ErrConflict
		}
		writeDNS := func() error {
			receipt, writeErr := dns.UpsertRecord(ctx, cred, provider.DNSUpsertRequest{Zone: zone.ProviderZone, Record: *state.Desired, Expected: state.Prior})
			if writeErr == nil {
				route.Receipt = &receipt
			}
			return writeErr
		}
		if !state.Policy.Enabled {
			if checkErr := writeDNS(); checkErr != nil {
				return checkErr
			}
		}
		if checkErr := edge.ApplyEdge(ctx, cred, zone.ProviderZone, route.OrgID+"/"+route.ID, route.Input.Hostname, state.Policy, route.Input.BlockedPathPrefixes); checkErr != nil {
			return checkErr
		}
		if state.Policy.Enabled {
			if checkErr := writeDNS(); checkErr != nil {
				return checkErr
			}
		}
		return edge.PurgeEdge(ctx, cred, zone.ProviderZone, route.Input.Hostname, nil)
	})
}

// RecoverEdges resumes accepted configuration and purge work, then processes
// successful deployments from a durable, deduplicated invalidation outbox.
func (s *Service) RecoverEdges(ctx context.Context) error {
	st, err := s.edgeStore()
	if err != nil {
		return err
	}
	states, err := st.PendingEdges(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, state := range states {
		if checkErr := s.ReconcileEdge(ctx, state.OrgID, state.RouteID); checkErr != nil {
			failures = append(failures, checkErr)
		}
	}
	if checkErr := st.QueueEdgeInvalidations(ctx); checkErr != nil {
		return errors.Join(append(failures, checkErr)...)
	}
	jobs, err := st.EdgeInvalidations(ctx)
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	for _, job := range jobs {
		if checkErr := s.invalidateEdgeDeployment(ctx, st, job); checkErr != nil {
			failures = append(failures, checkErr)
		}
	}
	return errors.Join(failures...)
}

func (s *Service) invalidateEdgeDeployment(ctx context.Context, st EdgeStore, job EdgeInvalidation) error {
	ds, err := s.domainStore()
	if err != nil {
		return err
	}
	route, err := ds.Route(ctx, job.Org, job.Project, job.Route)
	if err != nil {
		return err
	}
	return s.Store.WithOperationLock(ctx, routeLockKey(*route), func() error {
		state, readErr := st.Edge(ctx, job.Org, job.Route)
		if readErr != nil {
			return readErr
		}
		route, readErr = ds.Route(ctx, job.Org, job.Project, job.Route)
		if readErr != nil {
			return readErr
		}
		if state.Status == "pending" || state.Status == "failed" {
			return nil
		}
		if !state.Policy.Enabled || !state.Policy.InvalidateOnDeploy || route.Status == "removed" {
			return st.FinishEdgeInvalidation(ctx, job, "")
		}
		zone, adapter, cred, purgeErr := s.edgeAuthority(ctx, state.Principal, *route)
		if purgeErr == nil {
			edge, ok := adapter.(provider.EdgeAdapter)
			if !ok {
				return ErrInvalid
			}
			purgeErr = edge.PurgeEdge(ctx, cred, zone.ProviderZone, route.Input.Hostname, nil)
		}
		state.InvalidationError = ""
		if purgeErr != nil {
			state.InvalidationError = truncate(purgeErr.Error(), 500)
		} else {
			state.LastPurgeAt, state.LastDeploymentID = s.now().UTC(), job.Deployment
		}
		return errors.Join(purgeErr, st.SaveEdge(ctx, state), st.FinishEdgeInvalidation(ctx, job, state.InvalidationError))
	})
}

func (s *Service) verifyProxiedRouteDNS(ctx context.Context, route Route) bool {
	if route.Receipt == nil || !route.Receipt.Proxied {
		return false
	}
	ds, err := s.domainStore()
	if err != nil {
		return false
	}
	zone, err := ds.Zone(ctx, route.OrgID, route.Input.ZoneID)
	if err != nil {
		return false
	}
	dns, cred, err := s.zoneCredential(ctx, zone)
	if err != nil {
		return false
	}
	edge, ok := dns.(provider.EdgeAdapter)
	return ok && edge.VerifyEdgeDNS(ctx, cred, zone.ProviderZone, *route.Receipt) == nil
}
