package providerresources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

const staleAfter = 30 * time.Minute

type Service struct {
	deps Deps
	now  func() time.Time
}

func NewService(d Deps) *Service { return &Service{deps: d, now: time.Now} }

func (s *Service) require(ctx context.Context, p security.Principal, perm security.Permission) error {
	if p.OrgID == "" || s.deps.Authorizer == nil {
		return security.ErrForbidden
	}
	return s.deps.Authorizer.Require(ctx, p, perm, "")
}
func (s *Service) decorate(r *ProviderResource) {
	r.Ownership = "external"
	r.OwnershipState = "observed"
	r.Consumers = []ProviderResourceConsumer{}
	if r.ObservationState != "stale" {
		r.ObservationState = "observed"
	}
	switch {
	case r.Status == "missing":
		r.ObservationState = "missing"
	case s.now().UTC().Sub(r.ObservedAt) > staleAfter:
		r.ObservationState = "stale"
	}
	r.CredentialMethods = []string{}
	if s.deps.Endpoints != nil && engine(r.Kind) != "" && r.Incarnation != "" && r.Status != "missing" {
		if r.Host != "" {
			r.CredentialMethods = append(r.CredentialMethods, "dsn")
		} else if s.supportsEndpointProof(r.Provider) {
			r.CredentialMethods = append(r.CredentialMethods, "dsn")
		}
	}
	r.ConnectionState = "credentials_needed"
	if r.EndpointID != "" {
		r.ConnectionState = "verification_needed"
	}
	if r.Incarnation == "" {
		r.ConnectionState = "identity_verification_needed"
	}
	if r.ObservationState == "missing" {
		r.ConnectionState = "unavailable"
	}
}
func (s *Service) List(ctx context.Context, p security.Principal) (*ProviderResourceList, error) {
	if err := s.require(ctx, p, security.InfrastructureRead); err != nil {
		return nil, err
	}
	out, err := s.deps.Store.List(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	out.SupportedProviders = []string{}
	candidates := s.deps.SupportedProviders
	if capable, ok := s.deps.Reader.(interface{ SupportedProviders() []string }); ok {
		candidates = capable.SupportedProviders()
	}
	for _, cloud := range candidates {
		if s.deps.Reader.Supports(cloud) {
			out.SupportedProviders = append(out.SupportedProviders, cloud)
		}
	}
	sort.Strings(out.SupportedProviders)
	for _, r := range out.Resources {
		s.decorate(r)
	}
	if err := s.decorateUsage(ctx, p, out.Resources); err != nil {
		return nil, err
	}
	sort.Slice(out.Resources, func(i, j int) bool { return out.Resources[i].Name < out.Resources[j].Name })
	sort.Slice(out.Syncs, func(i, j int) bool { return out.Syncs[i].StartedAt.After(out.Syncs[j].StartedAt) })
	return out, nil
}
func (s *Service) Get(ctx context.Context, p security.Principal, id string) (*ProviderResource, error) {
	if err := s.require(ctx, p, security.InfrastructureRead); err != nil {
		return nil, err
	}
	r, err := s.deps.Store.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	s.decorate(r)
	if err := s.decorateUsage(ctx, p, []*ProviderResource{r}); err != nil {
		return nil, err
	}
	return r, nil
}
func account(c *connections.Connection) string {
	if c == nil || connections.AccountIdentity(c.Cloud, c.Account, c.Details) == "" {
		return ""
	}
	switch c.Cloud {
	case connections.AWS:
		return connections.AccountIdentity(c.Cloud, c.Account, map[string]string{"region": "account-inventory"})
	case connections.Azure:
		return connections.AccountIdentity(c.Cloud, c.Account, map[string]string{"tenant_id": c.Details["tenant_id"], "subscription_id": c.Details["subscription_id"]})
	default:
		return connections.AccountIdentity(c.Cloud, c.Account, c.Details)
	}
}
func sameConnection(a, b *connections.Connection) bool {
	return a != nil && b != nil && a.ID == b.ID && a.OrgID == b.OrgID && a.Cloud == b.Cloud && a.Generation == b.Generation && account(a) != "" && connections.AccountIdentity(a.Cloud, a.Account, a.Details) == connections.AccountIdentity(b.Cloud, b.Account, b.Details) && b.Status == connections.StatusReady
}
func (s *Service) currentConnection(ctx context.Context, p security.Principal, c *connections.Connection) error {
	rows, err := s.deps.Connections.All(ctx, p.OrgID)
	if err != nil {
		return ErrChanged
	}
	for _, r := range rows {
		if r.ID == c.ID && sameConnection(c, r) {
			return nil
		}
	}
	return ErrChanged
}
func resourceID(org, cloud, identity, id, incarnation string) string {
	var raw []byte
	for _, part := range []string{org, cloud, identity, id, incarnation} {
		raw = strconv.AppendQuote(raw, part)
		raw = append(raw, 0)
	}
	sum := sha256.Sum256(raw)
	return "providerresource_" + hex.EncodeToString(sum[:])
}

// Sync authorizes before it asks Vault for provider credentials. Discovery only
// reads provider APIs; a failed or partial run never implies deletion.
func (s *Service) Sync(ctx context.Context, p security.Principal, connectionID string) (*ProviderResourceSync, error) {
	if err := s.require(ctx, p, security.CloudAccountManage); err != nil {
		return nil, err
	}
	return s.sync(ctx, p, connectionID, true)
}

// sync is reached after either the account-management check or a persisted
// project binding has authorized this exact resource's verification.
func (s *Service) sync(ctx context.Context, p security.Principal, connectionID string, refreshOptIn bool) (*ProviderResourceSync, error) {
	rows, err := s.deps.Connections.All(ctx, p.OrgID)
	if err != nil {
		return nil, ErrDiscovery
	}
	var c *connections.Connection
	for _, row := range rows {
		if row.ID == connectionID {
			c = row
			break
		}
	}
	if c == nil {
		return nil, ErrNotFound
	}
	identity := account(c)
	if c.OrgID != p.OrgID || identity == "" || c.Status != connections.StatusReady || !s.deps.Reader.Supports(c.Cloud) {
		return nil, ErrUnverified
	}
	id, err := typeid.Generate("resourcesync")
	if err != nil {
		return nil, err
	}
	run := &ProviderResourceSync{ID: id.String(), OrgID: p.OrgID, ConnectionID: c.ID, Provider: c.Cloud, AccountIdentity: identity, ConnectionGeneration: c.Generation, ConnectionIdentity: connections.AccountIdentity(c.Cloud, c.Account, c.Details), RefreshOptIn: refreshOptIn, RequestedBy: p.UserID, RequestedKeyID: p.KeyID, Status: "running", StartedAt: s.now().UTC()}
	err = s.deps.Store.Update(ctx, p.OrgID, c.Cloud, identity, c.ID, c.Generation, func(state *AccountState) error {
		state.Sequence++
		for _, previous := range state.Syncs {
			if previous.Status == "running" {
				previous.Status = "superseded"
				previous.Error = ErrChanged.Error()
				at := run.StartedAt
				previous.FinishedAt = &at
			}
		}
		run.Sequence = state.Sequence
		state.Syncs = append(state.Syncs, run)
		return nil
	})
	if err != nil {
		return nil, err
	}
	resolved, credential, resolveErr := s.deps.Connections.Credential(ctx, p.OrgID, connectionID)
	if resolveErr != nil {
		return s.finish(ctx, run, provider.ResourceInventory{}, "failed", ErrDiscovery)
	}
	defer clear(credential)
	if !sameConnection(c, resolved) {
		return s.finish(ctx, run, provider.ResourceInventory{}, "superseded", ErrChanged)
	}
	inv, discoverErr := s.deps.Reader.Inventory(ctx, resolved, credential)
	if ctx.Err() != nil {
		discoverErr = ErrDiscovery
	}
	if discoverErr == nil && !validInventory(inv) {
		discoverErr = ErrDiscovery
	}
	if err = s.currentConnection(ctx, p, c); err != nil {
		return s.finish(ctx, run, provider.ResourceInventory{}, "superseded", ErrChanged)
	}
	if discoverErr != nil {
		return s.finish(ctx, run, provider.ResourceInventory{}, "failed", ErrDiscovery)
	}
	status := "partial"
	if inv.Complete {
		status = "complete"
	}
	return s.finish(ctx, run, inv, status, nil)
}
func (s *Service) finish(ctx context.Context, run *ProviderResourceSync, inv provider.ResourceInventory, status string, resultErr error) (*ProviderResourceSync, error) {
	finished := s.now().UTC()
	run.FinishedAt = &finished
	run.Status = status
	if inv.Scope != "" {
		run.Scope = inv.Scope
	}
	if resultErr != nil {
		run.Error = resultErr.Error()
	}
	// Record cancellation as a durable safe outcome with a bounded independent
	// context, rather than leaving a run indefinitely marked running.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	connectionID, generation := "", int64(0)
	if resultErr == nil {
		connectionID, generation = run.ConnectionID, run.ConnectionGeneration
	}
	err := s.deps.Store.Update(writeCtx, run.OrgID, run.Provider, run.AccountIdentity, connectionID, generation, func(state *AccountState) error {
		if state.Sequence != run.Sequence && resultErr == nil {
			run.Status = "superseded"
			run.Error = ErrChanged.Error()
			resultErr = ErrChanged
		}
		for i, r := range state.Syncs {
			if r.ID == run.ID {
				state.Syncs[i] = run
				break
			}
		}
		if resultErr != nil {
			for _, r := range state.Resources {
				if state.Sequence == run.Sequence && r.Status != "missing" {
					r.ObservationState = "stale"
				}
			}
			return nil
		}
		// Check the generation once more inside the publishing transaction.
		return s.reconcile(state, run, inv, finished)
	})
	if errors.Is(err, ErrChanged) && resultErr == nil {
		return s.finish(writeCtx, run, provider.ResourceInventory{}, "superseded", ErrChanged)
	}
	if err != nil {
		return run, err
	}
	return run, resultErr
}
func (s *Service) reconcile(state *AccountState, run *ProviderResourceSync, inv provider.ResourceInventory, at time.Time) error {
	seen := map[string]bool{}
	for _, o := range inv.Resources {
		id := resourceID(run.OrgID, run.Provider, run.AccountIdentity, o.ID, o.Incarnation)
		seen[id] = true
		var r *ProviderResource
		for _, old := range state.Resources {
			if old.ID == id {
				r = old
				break
			}
		}
		if r != nil && o.Host == "" && r.EndpointProofRequired {
			o.Host, o.Port = r.Host, r.Port
		}
		if r == nil {
			r = &ProviderResource{ID: id, OrgID: run.OrgID, Provider: run.Provider, AccountIdentity: run.AccountIdentity, ExternalID: o.ID, Incarnation: o.Incarnation, Ownership: "external", Revision: 1}
			state.Resources = append(state.Resources, r)
		} else if r.Host != o.Host || r.Port != o.Port || r.Database != o.Database || r.Kind != o.Kind || r.Version != o.Version || r.Status == "missing" || (r.ConnectionID == run.ConnectionID && r.ConnectionGeneration != run.ConnectionGeneration) {
			r.Revision++
			if r.Host != o.Host || r.Port != o.Port || r.Database != o.Database || r.Kind != o.Kind || r.Status == "missing" {
				r.EndpointID = ""
				r.EndpointRevision = 0
			}
		}
		r.ConnectionID, r.ConnectionGeneration = run.ConnectionID, run.ConnectionGeneration
		r.ConnectionIdentity = run.ConnectionIdentity
		r.Scope, r.Name, r.Kind, r.Version, r.Region, r.Status = inv.Scope, o.Name, o.Kind, o.Version, o.Region, o.Status
		r.Host, r.Port, r.Database, r.ObservedAt = o.Host, o.Port, o.Database, at
		r.ObservationState = "observed"
		// A direct observation of a different creation marker proves replacement
		// even when another page or resource kind had incomplete coverage.
		for _, old := range state.Resources {
			if old.ID != r.ID && old.ExternalID == o.ID && o.Incarnation != "" && old.Incarnation != o.Incarnation && old.Status != "missing" {
				old.Status = "missing"
				old.EndpointID = ""
				old.EndpointRevision = 0
				old.Revision++
			}
		}
	}
	if !inv.Complete {
		for _, r := range state.Resources {
			if r.Scope == inv.Scope && !seen[r.ID] && r.Status != "missing" {
				r.ObservationState = "stale"
			}
		}
	}
	if inv.Complete {
		for _, r := range state.Resources {
			if r.Scope == inv.Scope && !seen[r.ID] && r.Status != "missing" {
				r.Status = "missing"
				r.Revision++
				r.EndpointID = ""
				r.EndpointRevision = 0
			}
		}
	}
	return nil
}
func safeText(v string) bool {
	if len(v) > 2048 || strings.Contains(v, "://") || strings.ContainsAny(v, "@\x00") {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	lower := strings.ToLower(v)
	for _, bad := range []string{"password=", "token=", "secret=", "authorization:", "bearer "} {
		if strings.Contains(lower, bad) {
			return false
		}
	}
	return true
}
func validInventory(inv provider.ResourceInventory) bool {
	if strings.TrimSpace(inv.Scope) == "" || !safeText(inv.Scope) {
		return false
	}
	seen := map[string]bool{}
	for _, r := range inv.Resources {
		if r.ID == "" || r.Kind == "" || r.Port < 0 || r.Port > 65535 || seen[r.ID] {
			return false
		}
		seen[r.ID] = true
		for _, v := range []string{r.ID, r.Incarnation, r.Name, r.Kind, r.Version, r.Region, r.Status, r.Host, r.Database} {
			if !safeText(v) {
				return false
			}
		}
		if strings.ContainsAny(r.Host, "/?# ") {
			return false
		}
	}
	return true
}

// Verified refreshes the selected resource through its current authorized
// account before a new attachment review can use it.
func (s *Service) Verified(ctx context.Context, p security.Principal, id string) (*ProviderResource, error) {
	r, err := s.Get(ctx, p, id)
	if err != nil {
		return nil, err
	}
	run, syncErr := s.Sync(ctx, p, r.ConnectionID)
	if syncErr != nil {
		err = syncErr
		return nil, err
	}
	current, err := s.Get(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if current.Status == "missing" || current.Incarnation == "" || current.ObservationState == "stale" {
		return nil, ErrUnverified
	}
	if run.FinishedAt == nil || !current.ObservedAt.Equal(*run.FinishedAt) {
		return nil, ErrUnverified
	}
	if current.EndpointProofRequired {
		return s.verifyEndpoint(ctx, p, current, current.Host, current.Port)
	}
	return current, nil
}
func engine(kind string) string {
	switch kind {
	case "postgres", "postgresql":
		return "postgres"
	case "mysql":
		return "mysql"
	case "redis", "valkey":
		return "redis"
	}
	return ""
}
func matchesDSN(r *ProviderResource, dsn string) bool {
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil || u.Opaque != "" || !strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), strings.TrimSuffix(r.Host, ".")) {
		return false
	}
	port := r.Port
	defaultPort := 0
	switch engine(r.Kind) {
	case "postgres":
		defaultPort = 5432
		if u.Scheme != "postgres" && u.Scheme != "postgresql" {
			return false
		}
		if port == 0 {
			port = 5432
		}
	case "mysql":
		defaultPort = 3306
		if u.Scheme != "mysql" {
			return false
		}
		if port == 0 {
			port = 3306
		}
	case "redis":
		defaultPort = 6379
		if u.Scheme != "redis" && u.Scheme != "rediss" {
			return false
		}
		if port == 0 {
			port = 6379
		}
	default:
		return false
	}
	supplied := defaultPort
	if u.Port() != "" {
		supplied, err = strconv.Atoi(u.Port())
		if err != nil {
			return false
		}
	}
	return supplied == port && (r.Database == "" || strings.TrimPrefix(u.Path, "/") == r.Database)
}

// Connect stores an application DSN through catalogconnect. Each replacement
// gets its own endpoint ID, so reviewed consumers retain their pinned secret.
func (s *Service) Connect(ctx context.Context, p security.Principal, id string, in ConnectInput) (*ProviderResource, error) {
	if err := s.require(ctx, p, security.SecretManage); err != nil {
		return nil, err
	}
	r, err := s.Verified(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if r.Host == "" {
		u, parseErr := url.Parse(strings.TrimSpace(in.DSN))
		if parseErr != nil || u.Hostname() == "" {
			return nil, ErrInvalid
		}
		port := 0
		switch engine(r.Kind) {
		case "postgres":
			port = 5432
		case "mysql":
			port = 3306
		case "redis":
			port = 6379
		default:
			return nil, ErrInvalid
		}
		if u.Port() != "" {
			port, parseErr = strconv.Atoi(u.Port())
			if parseErr != nil {
				return nil, ErrInvalid
			}
		}
		r, err = s.verifyEndpoint(ctx, p, r, u.Hostname(), port)
		if err != nil {
			return nil, err
		}
	}
	if s.deps.Endpoints == nil || r.Host == "" || !matchesDSN(r, in.DSN) {
		return nil, ErrInvalid
	}
	e, err := s.deps.Endpoints.Create(ctx, p, catalogconnect.CreateInput{Name: fmt.Sprintf("resource-%s-%d", strings.TrimPrefix(r.ID, "providerresource_"), r.Revision+1), Engine: engine(r.Kind), DSN: in.DSN})
	if err != nil {
		return nil, ErrInvalid
	}
	if !endpointMatches(r, e) {
		return nil, s.cleanupEndpoint(ctx, p, e.ID, ErrInvalid)
	}
	err = s.deps.Store.Update(ctx, p.OrgID, r.Provider, r.AccountIdentity, r.ConnectionID, r.ConnectionGeneration, func(state *AccountState) error {
		for _, current := range state.Resources {
			if current.ID != id {
				continue
			}
			if current.Revision != r.Revision || current.Status == "missing" {
				return ErrChanged
			}
			current.EndpointID = e.ID
			current.EndpointRevision = e.Revision
			current.Revision++
			return nil
		}
		return ErrNotFound
	})
	if err != nil {
		return nil, s.cleanupEndpoint(ctx, p, e.ID, err)
	}
	return s.Get(ctx, p, id)
}
func endpointMatches(r *ProviderResource, e *catalogconnect.Endpoint) bool {
	return e != nil && e.OrgID == r.OrgID && engine(r.Kind) == e.Engine && strings.EqualFold(strings.TrimSuffix(e.Host, "."), strings.TrimSuffix(r.Host, ".")) && (r.Port == 0 || e.Port == r.Port) && (r.Database == "" || e.Database == r.Database)
}

// Resolve is a secret-bearing boundary. Callers must also authorize the target
// project and deployment before using the returned DSN.
func (s *Service) Resolve(ctx context.Context, p security.Principal, id string, revision int64) (*ProviderResource, *catalogconnect.Endpoint, string, error) {
	if err := s.require(ctx, p, security.SecretManage); err != nil {
		return nil, nil, "", err
	}
	r, err := s.Verified(ctx, p, id)
	if err != nil {
		return nil, nil, "", err
	}
	if r.Revision != revision {
		return nil, nil, "", ErrChanged
	}
	if r.EndpointID == "" || s.deps.Endpoints == nil {
		return nil, nil, "", ErrUnverified
	}
	if accessErr := s.require(ctx, p, security.SecretManage); accessErr != nil {
		return nil, nil, "", accessErr
	}
	e, dsn, err := s.deps.Endpoints.Resolve(ctx, p.OrgID, r.EndpointID)
	if err != nil {
		return nil, nil, "", ErrUnverified
	}
	if !endpointMatches(r, e) || e.Revision != r.EndpointRevision || !matchesDSN(r, dsn) {
		return nil, nil, "", ErrChanged
	}
	current, err := s.Get(ctx, p, id)
	if err != nil || current.Revision != r.Revision || current.EndpointID != e.ID {
		return nil, nil, "", ErrChanged
	}
	return r, e, dsn, nil
}

// RefreshAll refreshes accounts whose latest sync was explicitly authorized.
// It rechecks the requesting member's permissions and account identity. API-key
// requests stay manual because key revocation is checked by the request layer.
func (s *Service) RefreshAll(ctx context.Context) error {
	if s.deps.Organizations == nil {
		return nil
	}
	orgs, err := s.deps.Organizations(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, org := range orgs {
		if err := ctx.Err(); err != nil {
			return err
		}
		list, listErr := s.deps.Store.List(ctx, org)
		if listErr != nil {
			failures = append(failures, listErr)
			continue
		}
		runs := list.Syncs
		if tracker, ok := s.deps.Store.(interface {
			Tracked(context.Context, string) ([]*ProviderResourceSync, error)
		}); ok {
			runs, listErr = tracker.Tracked(ctx, org)
			if listErr != nil {
				failures = append(failures, listErr)
				continue
			}
		}
		latest := map[string]*ProviderResourceSync{}
		for _, run := range runs {
			if !run.RefreshOptIn {
				continue
			}
			old := latest[run.ConnectionID]
			if old == nil || old.StartedAt.Before(run.StartedAt) {
				latest[run.ConnectionID] = run
			}
		}
		rows, listErr := s.deps.Connections.All(ctx, org)
		if listErr != nil {
			failures = append(failures, ErrDiscovery)
			continue
		}
		refreshed := map[string]bool{}
		for _, c := range rows {
			run := latest[c.ID]
			if run == nil || run.RequestedBy == "" || run.RequestedKeyID != "" || c.Status != connections.StatusReady || !s.deps.Reader.Supports(c.Cloud) || account(c) != run.AccountIdentity || refreshed[c.Cloud+run.AccountIdentity] {
				continue
			}
			p := security.Principal{OrgID: org, UserID: run.RequestedBy}
			if members, ok := s.deps.Authorizer.(interface {
				RequireCurrentMembership(context.Context, security.Principal) error
			}); ok {
				if checkErr := members.RequireCurrentMembership(ctx, p); checkErr != nil {
					failures = append(failures, security.ErrForbidden)
					continue
				}
			}
			if _, syncErr := s.Sync(ctx, p, c.ID); syncErr != nil {
				failures = append(failures, syncErr)
			} else {
				refreshed[c.Cloud+run.AccountIdentity] = true
			}
		}
	}
	return errors.Join(failures...)
}

// endpointVerifier supports providers whose metadata list omits the endpoint.
// Implementations return public identity facts and discard connection-info URIs.
type endpointVerifier interface {
	VerifyEndpoint(context.Context, *connections.Connection, []byte, provider.ResourceObservation, string, int) (provider.ResourceObservation, error)
}

func (s *Service) verifyEndpoint(ctx context.Context, p security.Principal, r *ProviderResource, host string, port int) (*ProviderResource, error) {
	return s.verifyEndpointAccess(ctx, p, r, host, port, true)
}
func (s *Service) verifyEndpointAccess(ctx context.Context, p security.Principal, r *ProviderResource, host string, port int, requireSecret bool) (*ProviderResource, error) {
	if requireSecret {
		if err := s.require(ctx, p, security.SecretManage); err != nil {
			return nil, err
		}
	}

	verifier, ok := s.deps.Reader.(endpointVerifier)
	if !ok {
		return nil, ErrInvalid
	}
	c, credential, err := s.deps.Connections.Credential(ctx, p.OrgID, r.ConnectionID)
	if err != nil {
		return nil, ErrUnverified
	}
	defer clear(credential)
	if c.OrgID != p.OrgID || c.Cloud != r.Provider || account(c) != r.AccountIdentity || c.Generation != r.ConnectionGeneration {
		return nil, ErrChanged
	}
	observation := provider.ResourceObservation{ID: r.ExternalID, Incarnation: r.Incarnation, Name: r.Name, Kind: r.Kind, Version: r.Version, Region: r.Region, Status: r.Status, Host: r.Host, Port: r.Port, Database: r.Database}
	verified, err := verifier.VerifyEndpoint(ctx, c, credential, observation, host, port)
	if err != nil || verified.ID != r.ExternalID || verified.Incarnation != r.Incarnation || verified.Kind != r.Kind || !strings.EqualFold(verified.Host, host) || verified.Port != port || !validInventory(provider.ResourceInventory{Scope: r.Scope, Resources: []provider.ResourceObservation{verified}}) {
		return nil, ErrInvalid
	}
	if currentErr := s.currentConnection(ctx, p, c); currentErr != nil {
		return nil, currentErr
	}
	if r.Host != "" {
		if !strings.EqualFold(r.Host, verified.Host) || r.Port != verified.Port {
			return nil, ErrChanged
		}
		return r, nil
	}
	err = s.deps.Store.Update(ctx, p.OrgID, r.Provider, r.AccountIdentity, r.ConnectionID, r.ConnectionGeneration, func(state *AccountState) error {
		for _, current := range state.Resources {
			if current.ID != r.ID {
				continue
			}
			if current.Revision != r.Revision || current.Status == "missing" {
				return ErrChanged
			}
			current.Host, current.Port = verified.Host, verified.Port
			current.EndpointProofRequired = true
			current.Revision++
			return nil
		}
		return ErrNotFound
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, p, r.ID)
}

func (s *Service) supportsEndpointProof(cloud string) bool {
	capable, ok := s.deps.Reader.(interface{ SupportsEndpointVerification(string) bool })
	return ok && capable.SupportsEndpointVerification(cloud)
}

func (s *Service) cleanupEndpoint(ctx context.Context, p security.Principal, id string, cause error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.deps.Endpoints.Delete(cleanupCtx, p, id); err != nil {
		return fmt.Errorf("%w: an unused endpoint needs credential cleanup", cause)
	}
	return cause
}

// AccountIdentity identifies the inventory account independently of the region
// or resource-group traversal scope. ConnectionIdentity retains that scope.
func AccountIdentity(c *connections.Connection) string { return account(c) }

func (s *Service) decorateUsage(ctx context.Context, p security.Principal, resources []*ProviderResource) error {
	if s.deps.Usage == nil {
		return nil
	}
	bindings, err := s.deps.Usage(ctx, p.OrgID)
	if err != nil {
		return err
	}
	rows, err := s.deps.Connections.All(ctx, p.OrgID)
	if err != nil {
		return ErrDiscovery
	}
	accounts := map[string]string{}
	for _, c := range rows {
		if c.OrgID == p.OrgID {
			accounts[c.ID] = account(c)
		}
	}
	for _, r := range resources {
		seen := map[string]bool{}
		for _, b := range bindings {
			if b.ID == "" || seen[b.ID] {
				continue
			}
			explicit := b.InventoryResourceID == r.ID
			externalIDMatches := b.ExternalID == r.ExternalID
			if r.Provider == connections.Azure {
				externalIDMatches = strings.EqualFold(b.ExternalID, r.ExternalID)
			}
			identityMatches := b.InventoryResourceID == "" && b.Provider == r.Provider && accounts[b.ConnectionID] != "" && accounts[b.ConnectionID] == r.AccountIdentity && (b.AccountIdentity == "" || b.AccountIdentity == r.AccountIdentity) && externalIDMatches
			if identityMatches && r.Ownership != "loomplane" && (b.Incarnation == "" || r.Incarnation == "" || (b.Incarnation == r.Incarnation && !b.OwnershipVerified)) {
				r.OwnershipState = "unverified"
			}
			native := identityMatches && b.Incarnation != "" && b.Incarnation == r.Incarnation
			if !explicit && !native {
				continue
			}
			for _, v := range []string{b.ID, b.ProjectID, b.EnvironmentID, b.Name} {
				if !safeText(v) {
					return ErrInvalid
				}
			}
			seen[b.ID] = true
			r.Consumers = append(r.Consumers, ProviderResourceConsumer{ID: b.ID, BindingID: b.ID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID, Name: b.Name})
			if native && b.OwnershipVerified {
				r.Ownership = "loomplane"
				r.OwnershipState = "verified"
			}
		}
		sort.Slice(r.Consumers, func(i, j int) bool { return r.Consumers[i].ID < r.Consumers[j].ID })
	}
	return nil
}
