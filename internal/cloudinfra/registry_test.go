package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type registryTestStore struct {
	Store
	RegistryStore
	DomainStore
	state       *RegistryState
	zones       []*Zone
	failReceipt bool
	candidates  []*connections.Connection
}

func (s *registryTestStore) Zones(context.Context, string) ([]*Zone, error) { return s.zones, nil }
func (s *registryTestStore) RegistryState(context.Context, string, string) (*RegistryState, error) {
	if s.state == nil {
		return nil, ErrNotFound
	}
	raw, _ := json.Marshal(s.state)
	var out RegistryState
	err := json.Unmarshal(raw, &out)
	return &out, err
}
func (s *registryTestStore) SaveRegistryState(_ context.Context, state *RegistryState) error {
	if s.failReceipt && state.DNS != nil && state.DNS.Receipt != nil {
		s.failReceipt = false
		return errors.New("receipt save lost")
	}
	raw, _ := json.Marshal(state)
	return json.Unmarshal(raw, &s.state)
}
func (s *registryTestStore) WithOperationLock(_ context.Context, _ string, run func() error) error {
	return run()
}

type registryConnectionStore struct {
	connections.Store
	connection *connections.Connection
}

func (s registryConnectionStore) GetConnection(context.Context, string, string) (*connections.Connection, error) {
	return s.connection, nil
}

func TestRegistryDNSRecoversUncertainWriteAndRefusesDrift(t *testing.T) {
	adapter := &routeDNSAdapter{ownership: true}
	adapters := NewAdapters()
	if err := adapters.RegisterDNS("vercel", adapter); err != nil {
		t.Fatal(err)
	}
	zone := &Zone{ID: "zone", OrgID: "org", Name: "example.com", Mode: "managed", ConnectionID: "dns", ConnectionGeneration: 1, Provider: "vercel", AccountID: "account", RecordOwnership: true, ProviderZone: provider.DNSZone{ID: "zone", Name: "example.com", AccountID: "account"}, Revision: 1, VerifiedAt: time.Now(), ProofName: "_loomplane.example.com", ProofValue: "proof", ProofExpiresAt: time.Now().Add(time.Hour)}
	st := &registryTestStore{zones: []*Zone{zone}, failReceipt: true}
	svc := NewService(Deps{Store: st, Domains: st, Adapters: adapters, Connections: routeDNSCredentials{"vercel"}, Resolver: proofResolver{[]string{"proof"}}})
	state := &RegistryState{OrgID: "org", ConnectionID: "conn", Hostname: "images.example.com"}
	connection := &connections.Connection{Details: map[string]string{"ingress_address": "192.0.2.10"}}
	if err := svc.reconcileRegistryDNS(t.Context(), st, state, connection, false); err == nil {
		t.Fatal("receipt save should fail")
	} else {
		t.Log(err)
	}
	if st.state == nil || st.state.DNS == nil || !st.state.DNS.Attempted || adapter.writes != 1 {
		t.Fatal("write not checkpointed", adapter.writes)
	}
	state, err := st.RegistryState(t.Context(), "org", "conn")
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.reconcileRegistryDNS(t.Context(), st, state, connection, false); err != nil {
		t.Fatal(err)
	}
	if adapter.writes != 1 || !adapter.last.ReconcileOnly || state.DNS.Receipt == nil {
		t.Fatal("uncertain write was repeated")
	}
	adapter.records[0].Value = "192.0.2.99"
	if err = svc.reconcileRegistryDNS(t.Context(), st, state, connection, false); !errors.Is(err, provider.ErrInfrastructureDrift) {
		t.Fatal("foreign drift overwritten", err)
	}
	if err = svc.reconcileRegistryDNS(t.Context(), st, state, connection, true); !errors.Is(err, provider.ErrInfrastructureDrift) {
		t.Fatal("foreign drift deleted", err)
	}
	adapter.records[0] = *state.DNS.Receipt
	if err = svc.reconcileRegistryDNS(t.Context(), st, state, connection, true); err != nil || state.DNS != nil {
		t.Fatal("exact owned record not removed", err)
	}
}

func TestRegistryDNSRequiresOrganizationZoneAndLiveProof(t *testing.T) {
	st := &registryTestStore{zones: []*Zone{{ID: "project-zone", Name: "example.com", Mode: "managed", ProjectIDs: []string{"project"}}}}
	svc := NewService(Deps{Domains: st})
	state := &RegistryState{OrgID: "org", Hostname: "images.example.com"}
	if err := svc.reconcileRegistryDNS(t.Context(), st, state, &connections.Connection{}, false); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	st.zones[0].ProjectIDs = nil
	st.zones[0].Mode = "managed"
	adapter := &routeDNSAdapter{ownership: true}
	adapters := NewAdapters()
	if err := adapters.RegisterDNS("vercel", adapter); err != nil {
		t.Fatal(err)
	}
	svc.Adapters = adapters
	svc.Connections = routeDNSCredentials{"vercel"}
	st.zones[0].OrgID = "org"
	st.zones[0].ConnectionID = "dns"
	st.zones[0].ConnectionGeneration = 1
	st.zones[0].Provider = "vercel"
	st.zones[0].AccountID = "account"
	st.zones[0].ProviderZone = provider.DNSZone{ID: "zone", Name: "example.com", AccountID: "account"}
	st.zones[0].VerifiedAt = time.Now()
	st.zones[0].ProofName = "_loomplane.example.com"
	st.zones[0].ProofValue = "proof"
	state.DNS = &RegistryDNS{ZoneID: "project-zone"}
	svc.Resolver = proofResolver{[]string{"different"}}
	if err := svc.reconcileRegistryDNS(t.Context(), st, state, &connections.Connection{}, false); !errors.Is(err, ErrPending) {
		t.Fatal("missing proof authorized write", err)
	}
}

func TestRegistryRetryPreservesCheckpointAndRequiresMatchingIdentity(t *testing.T) {
	c := &connections.Connection{ID: "conn", OrgID: "org", Cloud: connections.Kubernetes, Generation: 2, Details: map[string]string{"registry_hostname": "images.example.com", "namespace_uid": "ns", "endpoint": "https://cluster", "cluster_ca": "ca"}}
	state := newRegistryState(c, time.Now())
	state.Generation = 1
	state.Action = &RegistryAction{ID: "operation", Kind: "restore", Phase: "backed_up", BackupID: "source"}
	st := &registryTestStore{state: state}
	svc := NewService(Deps{Store: st, RuntimeConnections: registryConnectionStore{connection: c}, Authz: cleanupTestAuth{}})
	p := security.Principal{OrgID: "org", UserID: "manager"}
	input := RegistryActionInput{Kind: "retry", Generation: 2, Hostname: state.Hostname}
	result, err := svc.RequestRegistryAction(t.Context(), p, "conn", input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Generation != 2 || result.Action.ID != "operation" || result.Action.Phase != "backed_up" || result.Authority.UserID != "manager" {
		t.Fatal("retry lost checkpoint")
	}
	c.Details["namespace_uid"] = "replacement"
	if _, err = svc.RequestRegistryAction(t.Context(), p, "conn", input); !errors.Is(err, ErrConflict) {
		t.Fatal("replacement cluster accepted", err)
	}
}

func TestRegistryRemovalWithoutDNSReceiptDoesNotInspectManualDNS(t *testing.T) {
	svc := NewService(Deps{})
	if err := svc.reconcileRegistryDNS(t.Context(), nil, &RegistryState{}, &connections.Connection{}, true); err != nil {
		t.Fatal(err)
	}
}

func (s *registryTestStore) RegistryCandidates(context.Context) ([]*connections.Connection, error) {
	return s.candidates, nil
}

type registryOrderCredentials struct {
	routeDNSCredentials
	beforeLock func()
}

func (c registryOrderCredentials) RefreshRegistry(context.Context, string, string) (*connections.Connection, []byte, error) {
	return nil, nil, ErrInvalid
}
func (c registryOrderCredentials) RegistryCredential(context.Context, string, string) (*connections.Connection, []byte, error) {
	return nil, nil, ErrInvalid
}
func (c registryOrderCredentials) WithRegistryLock(context.Context, string, string, func() error) error {
	c.beforeLock()
	return ErrBusy
}

type registryAuthority struct{}

func (registryAuthority) ValidatePrincipal(context.Context, security.Principal) error { return nil }
func TestRegistryDNSRunsBeforeWaitingForBuildLock(t *testing.T) {
	adapter := &routeDNSAdapter{ownership: true}
	adapters := NewAdapters()
	if err := adapters.RegisterDNS("vercel", adapter); err != nil {
		t.Fatal(err)
	}
	zone := &Zone{ID: "zone", OrgID: "org", Name: "example.com", Mode: "managed", ConnectionID: "dns", ConnectionGeneration: 1, Provider: "vercel", AccountID: "account", RecordOwnership: true, ProviderZone: provider.DNSZone{ID: "zone", Name: "example.com", AccountID: "account"}, Revision: 1, VerifiedAt: time.Now(), ProofName: "_loomplane.example.com", ProofValue: "proof", ProofExpiresAt: time.Now().Add(time.Hour)}
	c := &connections.Connection{ID: "conn", OrgID: "org", CreatedBy: "manager", Cloud: connections.Kubernetes, Generation: 1, Details: map[string]string{"registry_hostname": "images.example.com", "ingress_address": "192.0.2.10", "namespace_uid": "ns", "endpoint": "https://cluster", "cluster_ca": "ca"}}
	st := &registryTestStore{zones: []*Zone{zone}, candidates: []*connections.Connection{c}}
	locked := false
	credentials := registryOrderCredentials{routeDNSCredentials: routeDNSCredentials{"vercel"}, beforeLock: func() {
		locked = true
		if adapter.writes != 1 || st.state == nil || st.state.DNS.Receipt == nil {
			t.Fatal("first build blocked DNS creation")
		}
	}}
	svc := NewService(Deps{Store: st, Domains: st, Connections: credentials, Adapters: adapters, RuntimeConnections: registryConnectionStore{connection: c}, Resolver: proofResolver{[]string{"proof"}}, Authz: cleanupTestAuth{}, CapacityPrincipal: registryAuthority{}})
	if err := svc.RecoverManagedRegistries(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("registry lock was not reached")
	}
	// Another reconciler can remove a registry after the candidate list is read.
	st.state.Status = "removed"
	st.state.DNS = nil
	locked = false
	if err := svc.RecoverManagedRegistries(t.Context()); err != nil {
		t.Fatal(err)
	}
	if locked || adapter.writes != 1 || st.state.Status != "removed" {
		t.Fatal("stale candidate recreated a removed registry's DNS")
	}
}
