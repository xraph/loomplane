package providerresources

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type testAuth struct{ denied bool }

func (a *testAuth) Require(context.Context, security.Principal, security.Permission, string) error {
	if a.denied {
		return security.ErrForbidden
	}
	return nil
}

type testConnections struct {
	mu       sync.Mutex
	rows     map[string]*connections.Connection
	revealed int
}

func (c *testConnections) Credential(_ context.Context, org, id string) (*connections.Connection, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revealed++
	r := c.rows[id]
	if r == nil || r.OrgID != org {
		return nil, nil, connections.ErrNotFound
	}
	v := *r
	return &v, []byte("TOPSECRET"), nil
}
func (c *testConnections) All(_ context.Context, org string) ([]*connections.Connection, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*connections.Connection
	for _, r := range c.rows {
		if r.OrgID == org {
			v := *r
			out = append(out, &v)
		}
	}
	return out, nil
}

type testReader struct {
	fn func(*connections.Connection) (provider.ResourceInventory, error)
}

func (r *testReader) Supports(string) bool { return true }
func (r *testReader) Inventory(_ context.Context, c *connections.Connection, _ []byte) (provider.ResourceInventory, error) {
	return r.fn(c)
}

type memoryStore struct {
	mu       sync.Mutex
	accounts map[string]*AccountState
}

func (m *memoryStore) Update(_ context.Context, org, cloud, account string, _ string, _ int64, fn func(*AccountState) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := org + cloud + account
	state := m.accounts[key]
	if state == nil {
		state = &AccountState{Resources: []*ProviderResource{}, Syncs: []*ProviderResourceSync{}}
	}
	raw, _ := json.Marshal(state)
	var snapshot AccountState
	_ = json.Unmarshal(raw, &snapshot)
	if err := fn(&snapshot); err != nil {
		return err
	}
	m.accounts[key] = &snapshot
	return nil
}
func (m *memoryStore) List(_ context.Context, org string) (*ProviderResourceList, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := &ProviderResourceList{Resources: []*ProviderResource{}, Syncs: []*ProviderResourceSync{}}
	for _, s := range m.accounts {
		for _, r := range s.Resources {
			if r.OrgID == org {
				v := *r
				out.Resources = append(out.Resources, &v)
			}
		}
		for _, r := range s.Syncs {
			if r.OrgID == org {
				v := *r
				out.Syncs = append(out.Syncs, &v)
			}
		}
	}
	return out, nil
}
func (m *memoryStore) Get(ctx context.Context, org, id string) (*ProviderResource, error) {
	list, _ := m.List(ctx, org)
	for _, r := range list.Resources {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, ErrNotFound
}
func fixture(t *testing.T) (*Service, *testConnections, *testReader, *testAuth) {
	t.Helper()
	conn := &testConnections{rows: map[string]*connections.Connection{}}
	for _, id := range []string{"one", "two"} {
		conn.rows[id] = &connections.Connection{ID: id, OrgID: "org", Cloud: "digitalocean", Generation: 1, Status: connections.StatusReady, Details: map[string]string{"account_uuid": "account"}}
	}
	reader := &testReader{fn: func(*connections.Connection) (provider.ResourceInventory, error) {
		return provider.ResourceInventory{Scope: "account", Complete: true, Resources: []provider.ResourceObservation{{ID: "native", Incarnation: "created-1", Kind: "postgres", Name: "db", Host: "db.example", Port: 5432, Status: "online"}}}, nil
	}}
	auth := &testAuth{}
	svc := NewService(Deps{Store: &memoryStore{accounts: map[string]*AccountState{}}, Connections: conn, Reader: reader, Authorizer: auth})
	return svc, conn, reader, auth
}

var principal = security.Principal{OrgID: "org", UserID: "user"}

func TestDeniedBeforeCredentialResolution(t *testing.T) {
	s, c, _, a := fixture(t)
	a.denied = true
	if _, err := s.Sync(t.Context(), principal, "one"); !errors.Is(err, security.ErrForbidden) {
		t.Fatal(err)
	}
	if c.revealed != 0 {
		t.Fatal("unauthorized credential access")
	}
}
func TestStableIdentityAndReplacement(t *testing.T) {
	s, _, r, _ := fixture(t)
	for _, id := range []string{"one", "two"} {
		if _, err := s.Sync(t.Context(), principal, id); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := s.List(t.Context(), principal)
	if len(list.Resources) != 1 {
		t.Fatalf("duplicate connection produced %d resources", len(list.Resources))
	}
	old := list.Resources[0]
	r.fn = func(*connections.Connection) (provider.ResourceInventory, error) {
		return provider.ResourceInventory{Scope: "account", Complete: true, Resources: []provider.ResourceObservation{{ID: "native", Incarnation: "created-2", Kind: "postgres", Host: "db.example", Port: 5432}}}, nil
	}
	if _, err := s.Sync(t.Context(), principal, "two"); err != nil {
		t.Fatal(err)
	}
	list, _ = s.List(t.Context(), principal)
	if len(list.Resources) != 2 {
		t.Fatal("replacement reused identity")
	}
	got, _ := s.Get(t.Context(), principal, old.ID)
	if got.Status != "missing" || got.EndpointID != "" {
		t.Fatalf("old incarnation: %+v", got)
	}
}
func TestIncompleteAndFailedDiscoveryPreserveObservationsAndRedact(t *testing.T) {
	s, _, r, _ := fixture(t)
	_, _ = s.Sync(t.Context(), principal, "one")
	list, _ := s.List(t.Context(), principal)
	id := list.Resources[0].ID
	r.fn = func(*connections.Connection) (provider.ResourceInventory, error) {
		return provider.ResourceInventory{Scope: "account", Complete: false}, nil
	}
	run, err := s.Sync(t.Context(), principal, "one")
	if err != nil || run.Status != "partial" {
		t.Fatalf("partial: %+v %v", run, err)
	}
	got, _ := s.Get(t.Context(), principal, id)
	if got.Status == "missing" {
		t.Fatal("partial traversal marked missing")
	}
	r.fn = func(*connections.Connection) (provider.ResourceInventory, error) {
		return provider.ResourceInventory{}, errors.New("postgres://user:TOPSECRET@db.example?token=TOPSECRET")
	}
	run, err = s.Sync(t.Context(), principal, "one")
	if err == nil || run.Status != "failed" {
		t.Fatalf("failure: %+v %v", run, err)
	}
	list, _ = s.List(t.Context(), principal)
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "TOPSECRET") || strings.Contains(err.Error(), "TOPSECRET") {
		t.Fatal("secret leaked")
	}
	got, _ = s.Get(t.Context(), principal, id)
	if got.Status == "missing" {
		t.Fatal("failed traversal marked missing")
	}
}
func TestCompleteScopeMarksMissingAndStaleness(t *testing.T) {
	s, _, r, _ := fixture(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	_, _ = s.Sync(t.Context(), principal, "one")
	list, _ := s.List(t.Context(), principal)
	id := list.Resources[0].ID
	now = now.Add(31 * time.Minute)
	got, _ := s.Get(t.Context(), principal, id)
	if got.ObservationState != "stale" {
		t.Fatalf("state: %s", got.ObservationState)
	}
	r.fn = func(*connections.Connection) (provider.ResourceInventory, error) {
		return provider.ResourceInventory{Scope: "another-scope", Complete: true}, nil
	}
	_, _ = s.Sync(t.Context(), principal, "one")
	got, _ = s.Get(t.Context(), principal, id)
	if got.Status == "missing" {
		t.Fatal("another scope marked missing")
	}
	r.fn = func(*connections.Connection) (provider.ResourceInventory, error) {
		return provider.ResourceInventory{Scope: "account", Complete: true}, nil
	}
	_, _ = s.Sync(t.Context(), principal, "one")
	got, _ = s.Get(t.Context(), principal, id)
	if got.Status != "missing" {
		t.Fatal("complete traversal did not mark missing")
	}
}
func TestOverlappingSyncIsFenced(t *testing.T) {
	s, _, r, _ := fixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	r.fn = func(c *connections.Connection) (provider.ResourceInventory, error) {
		if c.ID == "one" {
			close(entered)
			<-release
			return provider.ResourceInventory{Scope: "account", Complete: true}, nil
		}
		return provider.ResourceInventory{Scope: "account", Complete: true, Resources: []provider.ResourceObservation{{ID: "native", Incarnation: "created-1", Kind: "postgres", Name: "new"}}}, nil
	}
	done := make(chan error, 1)
	go func() { _, err := s.Sync(context.Background(), principal, "one"); done <- err }()
	<-entered
	if _, err := s.Sync(t.Context(), principal, "two"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrChanged) {
		t.Fatalf("older sync: %v", err)
	}
	list, _ := s.List(t.Context(), principal)
	if len(list.Resources) != 1 || list.Resources[0].Name != "new" || list.Resources[0].Status == "missing" || list.Resources[0].ObservationState != "observed" {
		t.Fatalf("older run overwrote observations: %+v", list.Resources)
	}
}
func TestConnectionGenerationAndUnknownIncarnationBlockUse(t *testing.T) {
	s, c, r, _ := fixture(t)
	r.fn = func(*connections.Connection) (provider.ResourceInventory, error) {
		c.mu.Lock()
		c.rows["one"].Generation++
		c.mu.Unlock()
		return provider.ResourceInventory{Scope: "account", Complete: true, Resources: []provider.ResourceObservation{{ID: "native", Kind: "postgres"}}}, nil
	}
	if _, err := s.Sync(t.Context(), principal, "one"); !errors.Is(err, ErrChanged) {
		t.Fatalf("rotated credential: %v", err)
	}
	list, _ := s.List(t.Context(), principal)
	if len(list.Resources) != 0 {
		t.Fatal("rotation published unverified inventory")
	}
}

func TestFailedDiscoveryMarksStaleAndKeepsHistory(t *testing.T) {
	s, _, r, _ := fixture(t)
	_, _ = s.Sync(t.Context(), principal, "one")
	r.fn = func(*connections.Connection) (provider.ResourceInventory, error) {
		return provider.ResourceInventory{}, errors.New("secret")
	}
	_, _ = s.Sync(t.Context(), principal, "one")
	list, _ := s.List(t.Context(), principal)
	if len(list.Syncs) != 2 || list.Resources[0].ObservationState != "stale" {
		t.Fatalf("failure facts: %+v", list)
	}
}

type endpointRows struct {
	rows map[string]*catalogconnect.Endpoint
}

func (e *endpointRows) Create(_ context.Context, r *catalogconnect.Endpoint) error {
	v := *r
	e.rows[r.ID] = &v
	return nil
}
func (e *endpointRows) Get(_ context.Context, org, id string) (*catalogconnect.Endpoint, error) {
	r := e.rows[id]
	if r == nil || r.OrgID != org {
		return nil, catalogconnect.ErrNotFound
	}
	v := *r
	return &v, nil
}
func (e *endpointRows) List(_ context.Context, org string) ([]*catalogconnect.Endpoint, error) {
	var out []*catalogconnect.Endpoint
	for _, r := range e.rows {
		if r.OrgID == org {
			v := *r
			out = append(out, &v)
		}
	}
	return out, nil
}
func (e *endpointRows) Delete(_ context.Context, org, id string) error {
	if r := e.rows[id]; r != nil && r.OrgID == org {
		delete(e.rows, id)
		return nil
	}
	return catalogconnect.ErrNotFound
}

type endpointSecrets map[string][]byte

func (e endpointSecrets) Put(_ context.Context, key string, b []byte) error {
	e[key] = append([]byte(nil), b...)
	return nil
}
func (e endpointSecrets) Get(_ context.Context, key string) ([]byte, error) {
	return append([]byte(nil), e[key]...), nil
}
func (e endpointSecrets) Delete(_ context.Context, key string) error { delete(e, key); return nil }
func TestConnectUsesVaultAndPinsSecretAndPhysicalIdentity(t *testing.T) {
	s, _, reader, a := fixture(t)
	rows := &endpointRows{rows: map[string]*catalogconnect.Endpoint{}}
	secrets := endpointSecrets{}
	s.deps.Endpoints = catalogconnect.NewService(rows, secrets, a)
	_, _ = s.Sync(t.Context(), principal, "one")
	list, _ := s.List(t.Context(), principal)
	id := list.Resources[0].ID
	if _, err := s.Connect(t.Context(), principal, id, ConnectInput{DSN: "postgres://u:TOPSECRET@other.example/app?sslmode=require"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("endpoint substitution: %v", err)
	}
	if len(secrets) != 0 {
		t.Fatal("mismatch reached vault")
	}
	r, err := s.Connect(t.Context(), principal, id, ConnectInput{DSN: "postgres://u:TOPSECRET@db.example:5432/app?sslmode=require"})
	if err != nil {
		t.Fatal(err)
	}
	oldRevision, oldEndpoint := r.Revision, r.EndpointID
	got, e, dsn, err := s.Resolve(t.Context(), principal, id, r.Revision)
	if err != nil || got.ID != id || e.ID != r.EndpointID || !strings.Contains(dsn, "TOPSECRET") {
		t.Fatalf("resolve: %+v %+v %v", got, e, err)
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "TOPSECRET") {
		t.Fatal("dsn returned in resource")
	}
	r, err = s.Connect(t.Context(), principal, id, ConnectInput{DSN: "postgres://u:NEWSECRET@db.example:5432/app?sslmode=require"})
	if err != nil {
		t.Fatal(err)
	}
	if r.EndpointID == oldEndpoint || r.Revision <= oldRevision || len(secrets) != 2 {
		t.Fatal("rotation changed a consumer's old secret")
	}
	if _, _, _, err = s.Resolve(t.Context(), principal, id, oldRevision); !errors.Is(err, ErrChanged) {
		t.Fatalf("old review survived rotation: %v", err)
	}
	reader.fn = func(*connections.Connection) (provider.ResourceInventory, error) {
		return provider.ResourceInventory{Scope: "account", Complete: true, Resources: []provider.ResourceObservation{{ID: "native", Incarnation: "created-1", Kind: "postgres", Host: "new.example", Port: 5432}}}, nil
	}
	if _, _, _, err = s.Resolve(t.Context(), principal, id, r.Revision); !errors.Is(err, ErrChanged) {
		t.Fatalf("endpoint change survived review: %v", err)
	}
	current, _ := s.Get(t.Context(), principal, id)
	if current.EndpointID != "" {
		t.Fatal("endpoint changed but access survived")
	}
}
func TestUnknownIncarnationAndOtherOrganizationCannotConnect(t *testing.T) {
	s, _, reader, _ := fixture(t)
	reader.fn = func(*connections.Connection) (provider.ResourceInventory, error) {
		return provider.ResourceInventory{Scope: "account", Complete: true, Resources: []provider.ResourceObservation{{ID: "native", Kind: "postgres", Host: "db.example", Port: 5432}}}, nil
	}
	_, _ = s.Sync(t.Context(), principal, "one")
	list, _ := s.List(t.Context(), principal)
	id := list.Resources[0].ID
	if _, err := s.Verified(t.Context(), principal, id); !errors.Is(err, ErrUnverified) {
		t.Fatalf("ambiguous incarnation: %v", err)
	}
	if _, err := s.Get(t.Context(), security.Principal{OrgID: "other", UserID: "user"}, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross org: %v", err)
	}
}

func TestBackgroundRefreshUsesTrackedAuthorizedIdentity(t *testing.T) {
	s, c, _, a := fixture(t)
	s.deps.Organizations = func(context.Context) ([]string, error) { return []string{"org"}, nil }
	if err := s.RefreshAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c.revealed != 0 {
		t.Fatal("untracked account refreshed")
	}
	_, _ = s.Sync(t.Context(), principal, "one")
	if err := s.RefreshAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c.revealed != 2 {
		t.Fatalf("authorized refresh count %d", c.revealed)
	}
	a.denied = true
	if err := s.RefreshAll(t.Context()); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("denied refresh: %v", err)
	}
	if c.revealed != 2 {
		t.Fatal("refresh bypassed current role")
	}
	a.denied = false
	c.mu.Lock()
	c.rows["one"].Details = map[string]string{"account_uuid": "substitute"}
	c.mu.Unlock()
	if err := s.RefreshAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c.revealed != 2 {
		t.Fatal("refresh followed substituted account")
	}
}
func TestBackgroundRefreshDoesNotReuseAPIKeyAuthority(t *testing.T) {
	s, c, _, _ := fixture(t)
	s.deps.Organizations = func(context.Context) ([]string, error) { return []string{"org"}, nil }
	p := principal
	p.KeyID = "key"
	_, _ = s.Sync(t.Context(), p, "one")
	if err := s.RefreshAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c.revealed != 1 {
		t.Fatal("background reused key without request-layer revocation check")
	}
}
func TestSupportedProvidersOnlyReportsInstalledReaders(t *testing.T) {
	s, _, _, _ := fixture(t)
	s.deps.SupportedProviders = []string{"digitalocean", "azure"}
	got, err := s.List(t.Context(), principal)
	if err != nil || len(got.SupportedProviders) != 2 {
		t.Fatalf("capabilities: %+v %v", got, err)
	}
}
func TestDSNPortFollowsProtocolDefault(t *testing.T) {
	r := &ProviderResource{Kind: "postgres", Host: "db.example", Port: 25060}
	if matchesDSN(r, "postgres://u:p@db.example/app?sslmode=require") {
		t.Fatal("missing nondefault port accepted")
	}
	if !matchesDSN(r, "postgres://u:p@db.example:25060/app?sslmode=require") {
		t.Fatal("explicit discovered port rejected")
	}
}

type verifyingReader struct {
	*testReader
	verify func(provider.ResourceObservation, string, int) (provider.ResourceObservation, error)
}

func (r *verifyingReader) SupportsEndpointVerification(string) bool { return true }
func (r *verifyingReader) VerifyEndpoint(_ context.Context, _ *connections.Connection, _ []byte, o provider.ResourceObservation, host string, port int) (provider.ResourceObservation, error) {
	return r.verify(o, host, port)
}
func TestConnectVerifiesProviderHostWhenInventoryOmitsEndpoint(t *testing.T) {
	s, _, reader, a := fixture(t)
	reader.fn = func(*connections.Connection) (provider.ResourceInventory, error) {
		return provider.ResourceInventory{Scope: "account", Complete: true, Resources: []provider.ResourceObservation{{ID: "native", Incarnation: "created-1", Kind: "postgres"}}}, nil
	}
	calls := 0
	s.deps.Reader = &verifyingReader{testReader: reader, verify: func(o provider.ResourceObservation, host string, port int) (provider.ResourceObservation, error) {
		calls++
		if host != "db.example" || port != 5432 {
			return o, ErrInvalid
		}
		o.Host, o.Port = host, port
		return o, nil
	}}
	rows := &endpointRows{rows: map[string]*catalogconnect.Endpoint{}}
	secrets := endpointSecrets{}
	s.deps.Endpoints = catalogconnect.NewService(rows, secrets, a)
	_, _ = s.Sync(t.Context(), principal, "one")
	list, _ := s.List(t.Context(), principal)
	id := list.Resources[0].ID
	if _, err := s.Connect(t.Context(), principal, id, ConnectInput{DSN: "postgres://u:p@unrelated.example/app?sslmode=require"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unverified candidate: %v", err)
	}
	r, err := s.Connect(t.Context(), principal, id, ConnectInput{DSN: "postgres://u:p@db.example/app?sslmode=require"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = s.Resolve(t.Context(), principal, id, r.Revision)
	if err != nil {
		t.Fatalf("verified endpoint lost during metadata refresh: %v", err)
	}
	if calls < 3 {
		t.Fatalf("host not reverified: %d", calls)
	}
}

func TestCancelledDiscoveryCannotMarkMissing(t *testing.T) {
	s, _, reader, _ := fixture(t)
	_, _ = s.Sync(t.Context(), principal, "one")
	ctx, cancel := context.WithCancel(t.Context())
	reader.fn = func(*connections.Connection) (provider.ResourceInventory, error) {
		cancel()
		return provider.ResourceInventory{Scope: "account", Complete: true}, nil
	}
	run, err := s.Sync(ctx, principal, "one")
	if err == nil || run == nil || run.Status != "failed" {
		t.Fatalf("cancelled run published complete: %+v %v", run, err)
	}
	list, _ := s.List(t.Context(), principal)
	if list.Resources[0].Status == "missing" {
		t.Fatal("cancelled run marked missing")
	}
}
func TestDuplicateConnectionGenerationAndRenamePreserveAccessRevision(t *testing.T) {
	s, c, reader, _ := fixture(t)
	_, _ = s.Sync(t.Context(), principal, "one")
	list, _ := s.List(t.Context(), principal)
	before := list.Resources[0]
	c.mu.Lock()
	c.rows["two"].Generation = 5
	c.mu.Unlock()
	original := reader.fn
	reader.fn = func(c *connections.Connection) (provider.ResourceInventory, error) {
		out, err := original(c)
		out.Resources[0].Name = "renamed"
		return out, err
	}
	_, _ = s.Sync(t.Context(), principal, "two")
	after, _ := s.Get(t.Context(), principal, before.ID)
	if before.Revision != after.Revision || after.Name != "renamed" {
		t.Fatalf("display/access-path update invalidated review: %+v -> %+v", before, after)
	}
}

func TestGlobalResourceIdentityAcrossConnectionScopes(t *testing.T) {
	for _, cloud := range []string{"aws", "azure"} {
		t.Run(cloud, func(t *testing.T) {
			s, c, reader, _ := fixture(t)
			for id, row := range c.rows {
				row.Cloud = cloud
				row.Account = "123456789012"
				if cloud == "aws" {
					row.Details = map[string]string{"region": "us-east-1"}
					if id == "two" {
						row.Details["region"] = "us-west-2"
					}
				} else {
					row.Details = map[string]string{"tenant_id": "tenant", "subscription_id": "sub", "resource_group": "one"}
					if id == "two" {
						row.Details["resource_group"] = "two"
					}
				}
			}
			reader.fn = func(c *connections.Connection) (provider.ResourceInventory, error) {
				return provider.ResourceInventory{Scope: c.Details["region"] + c.Details["resource_group"], Complete: true, Resources: []provider.ResourceObservation{{ID: "native-global", Incarnation: "creation", Kind: "object-storage", Name: "bucket"}}}, nil
			}
			for _, id := range []string{"one", "two"} {
				if _, err := s.Sync(t.Context(), principal, id); err != nil {
					t.Fatal(err)
				}
			}
			list, _ := s.List(t.Context(), principal)
			if len(list.Resources) != 1 {
				t.Fatalf("one account split by placement scope: %d", len(list.Resources))
			}
		})
	}
}

type revocableSecretsAuth struct{ revoked bool }

func (a *revocableSecretsAuth) Require(_ context.Context, _ security.Principal, perm security.Permission, _ string) error {
	if a.revoked && perm == security.SecretManage {
		return security.ErrForbidden
	}
	return nil
}
func TestResolveReauthorizesSecretsAfterProviderCheck(t *testing.T) {
	s, _, reader, _ := fixture(t)
	a := &revocableSecretsAuth{}
	s.deps.Authorizer = a
	s.deps.Endpoints = catalogconnect.NewService(&endpointRows{rows: map[string]*catalogconnect.Endpoint{}}, endpointSecrets{}, a)
	_, _ = s.Sync(t.Context(), principal, "one")
	list, _ := s.List(t.Context(), principal)
	r, err := s.Connect(t.Context(), principal, list.Resources[0].ID, ConnectInput{DSN: "postgres://u:TOPSECRET@db.example/app?sslmode=require"})
	if err != nil {
		t.Fatal(err)
	}
	original := reader.fn
	reader.fn = func(c *connections.Connection) (provider.ResourceInventory, error) {
		a.revoked = true
		return original(c)
	}
	_, _, dsn, err := s.Resolve(t.Context(), principal, r.ID, r.Revision)
	if !errors.Is(err, security.ErrForbidden) || dsn != "" {
		t.Fatalf("secret permission revoked during provider request: %v", err)
	}
}

func TestUsageRequiresAccountIncarnationAndOwnershipReceipt(t *testing.T) {
	s, _, _, _ := fixture(t)
	_, _ = s.Sync(t.Context(), principal, "one")
	list, _ := s.List(t.Context(), principal)
	r := list.Resources[0]
	bindings := []ResourceBinding{{ID: "native", ProjectID: "project", EnvironmentID: "production", Name: "database", Provider: r.Provider, ConnectionID: "one", AccountIdentity: r.AccountIdentity, ExternalID: r.ExternalID, Incarnation: r.Incarnation, OwnershipVerified: true}, {ID: "attached", ProjectID: "another", EnvironmentID: "staging", Name: "pg", InventoryResourceID: r.ID}, {ID: "replacement", Provider: r.Provider, ConnectionID: "one", ExternalID: r.ExternalID, Incarnation: "another-creation", OwnershipVerified: true}, {ID: "unknown", Provider: r.Provider, ConnectionID: "one", ExternalID: r.ExternalID, OwnershipVerified: true}, {ID: "wrong-account", Provider: r.Provider, ConnectionID: "one", AccountIdentity: "substitute", ExternalID: r.ExternalID, Incarnation: r.Incarnation, OwnershipVerified: true}}
	s.deps.Usage = func(_ context.Context, org string) ([]ResourceBinding, error) {
		if org != "org" {
			t.Fatalf("wrong usage organization %s", org)
		}
		return bindings, nil
	}
	got, err := s.Get(t.Context(), principal, r.ID)
	if err != nil || got.Ownership != "loomplane" || len(got.Consumers) != 2 {
		t.Fatalf("verified usage: %+v %v", got, err)
	}
	bindings[0].OwnershipVerified = false
	got, err = s.Get(t.Context(), principal, r.ID)
	if err != nil || got.Ownership != "external" || got.OwnershipState != "unverified" || len(got.Consumers) != 2 {
		t.Fatalf("receiptless usage conferred lifecycle ownership: %+v %v", got, err)
	}
	bindings = bindings[1:]
	got, err = s.Get(t.Context(), principal, r.ID)
	if err != nil || got.Ownership != "external" || len(got.Consumers) != 1 || got.Consumers[0].ID != "attached" {
		t.Fatalf("external association: %+v %v", got, err)
	}
	s.deps.Usage = func(context.Context, string) ([]ResourceBinding, error) { return nil, errors.New("usage unavailable") }
	if _, err = s.List(t.Context(), principal); err == nil {
		t.Fatal("usage failure masqueraded as unused inventory")
	}
}

type projectOnlyAuth struct{}

func (projectOnlyAuth) Require(_ context.Context, p security.Principal, perm security.Permission, id string) error {
	if p.OrgID == "org" && ((id == "project" && (perm == security.ProjectRead || perm == security.DeploymentCreate)) || ((id == "production" || id == "staging") && perm == security.EnvironmentRead)) {
		return nil
	}
	return security.ErrForbidden
}
func TestProjectAccessRequiresPersistedPinnedBindingBeforeCredentials(t *testing.T) {
	s, c, _, a := fixture(t)
	s.deps.Endpoints = catalogconnect.NewService(&endpointRows{rows: map[string]*catalogconnect.Endpoint{}}, endpointSecrets{}, a)
	_, _ = s.Sync(t.Context(), principal, "one")
	list, _ := s.List(t.Context(), principal)
	r, err := s.Connect(t.Context(), principal, list.Resources[0].ID, ConnectInput{DSN: "postgres://u:PROJECTSECRET@db.example/app?sslmode=require"})
	if err != nil {
		t.Fatal(err)
	}
	bindings := []ResourceBinding{{ID: "attachment", ProjectID: "project", EnvironmentID: "production", Name: "db", Provider: r.Provider, ConnectionID: r.ConnectionID, AccountIdentity: r.AccountIdentity, ExternalID: r.ExternalID, Incarnation: r.Incarnation, InventoryResourceID: r.ID, Revision: r.Revision, EndpointID: r.EndpointID, EndpointRevision: r.EndpointRevision}}
	s.deps.Usage = func(context.Context, string) ([]ResourceBinding, error) { return bindings, nil }
	s.deps.Authorizer = projectOnlyAuth{}
	developer := security.Principal{OrgID: "org", UserID: "developer"}
	before := c.revealed
	if _, _, _, err = s.ResolveProject(t.Context(), developer, "other-project", "production", r.ID, r.Revision); !errors.Is(err, security.ErrForbidden) || c.revealed != before {
		t.Fatalf("ungranted project reached credentials: %v", err)
	}
	if _, _, _, err = s.ResolveProject(t.Context(), developer, "project", "staging", r.ID, r.Revision); !errors.Is(err, security.ErrForbidden) || c.revealed != before {
		t.Fatalf("cross-environment binding reached credentials: %v", err)
	}
	if _, _, _, err = s.Resolve(t.Context(), developer, r.ID, r.Revision); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("public secret boundary weakened: %v", err)
	}
	got, endpoint, dsn, err := s.ResolveProject(t.Context(), developer, "project", "production", r.ID, r.Revision)
	if err != nil || got.ID != r.ID || endpoint.ID != r.EndpointID || !strings.Contains(dsn, "PROJECTSECRET") {
		t.Fatalf("approved project access: %+v %+v %v", got, endpoint, err)
	}
	if len(got.Consumers) != 1 || got.Consumers[0].ProjectID != "project" {
		t.Fatal("project access disclosed unrelated consumers")
	}
	history, _ := s.deps.Store.List(t.Context(), "org")
	for _, run := range history.Syncs {
		if run.RequestedBy == developer.UserID && run.RefreshOptIn {
			t.Fatal("project actor took over account auto-refresh")
		}
	}
	before = c.revealed
	bindings = nil
	if _, _, _, err = s.ResolveProject(t.Context(), developer, "project", "production", r.ID, r.Revision); !errors.Is(err, security.ErrForbidden) || c.revealed != before {
		t.Fatalf("removed persisted binding reached credentials: %v", err)
	}
}

type revocableProjectAuth struct{ denied bool }

func (a *revocableProjectAuth) Require(ctx context.Context, p security.Principal, perm security.Permission, id string) error {
	if a.denied {
		return security.ErrForbidden
	}
	return (projectOnlyAuth{}).Require(ctx, p, perm, id)
}

type countingEndpoints struct {
	Endpoints
	resolves int
}

func (e *countingEndpoints) Resolve(ctx context.Context, org, id string) (*catalogconnect.Endpoint, string, error) {
	e.resolves++
	return e.Endpoints.Resolve(ctx, org, id)
}
func TestProjectGrantRevokedDuringProviderCheckBlocksVault(t *testing.T) {
	s, _, reader, admin := fixture(t)
	endpoints := &countingEndpoints{Endpoints: catalogconnect.NewService(&endpointRows{rows: map[string]*catalogconnect.Endpoint{}}, endpointSecrets{}, admin)}
	s.deps.Endpoints = endpoints
	_, _ = s.Sync(t.Context(), principal, "one")
	list, _ := s.List(t.Context(), principal)
	r, err := s.Connect(t.Context(), principal, list.Resources[0].ID, ConnectInput{DSN: "postgres://u:SECRET@db.example/app?sslmode=require"})
	if err != nil {
		t.Fatal(err)
	}
	binding := ResourceBinding{ID: "attachment", ProjectID: "project", EnvironmentID: "production", Provider: r.Provider, AccountIdentity: r.AccountIdentity, ExternalID: r.ExternalID, Incarnation: r.Incarnation, InventoryResourceID: r.ID, Revision: r.Revision, EndpointID: r.EndpointID, EndpointRevision: r.EndpointRevision}
	s.deps.Usage = func(context.Context, string) ([]ResourceBinding, error) { return []ResourceBinding{binding}, nil }
	auth := &revocableProjectAuth{}
	s.deps.Authorizer = auth
	original := reader.fn
	reader.fn = func(c *connections.Connection) (provider.ResourceInventory, error) {
		auth.denied = true
		return original(c)
	}
	_, _, dsn, err := s.ResolveProject(t.Context(), security.Principal{OrgID: "org", UserID: "developer"}, "project", "production", r.ID, r.Revision)
	if !errors.Is(err, security.ErrForbidden) || dsn != "" || endpoints.resolves != 0 {
		t.Fatalf("revoked project authority reached Vault: err=%v resolves=%d", err, endpoints.resolves)
	}
}
