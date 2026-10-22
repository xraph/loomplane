package cloudinfra

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/security"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

type routeDNSCredentials struct{ cloud string }

func (c routeDNSCredentials) Credential(context.Context, string, string) (*connections.Connection, []byte, error) {
	return &connections.Connection{OrgID: "org", Cloud: c.cloud, Generation: 1, Details: map[string]string{"team_id": "account", "account_uuid": "account"}}, []byte("fixture"), nil
}

type routeDNSStore struct {
	DomainStore
	durable     RouteOperation
	failReceipt bool
}

func (s *routeDNSStore) SaveRouteOperation(_ context.Context, op *RouteOperation) error {
	if op.Receipt != nil && s.failReceipt {
		s.failReceipt = false
		return errors.New("lost database write")
	}
	s.durable = *op
	return nil
}

type routeDNSAdapter struct {
	provider.DNSAdapter
	ownership bool
	records   []provider.DNSRecord
	writes    int
	last      provider.DNSUpsertRequest
	deleted   string
}

func (a *routeDNSAdapter) GetZone(_ context.Context, c provider.InfrastructureCredential, _ string) (provider.DNSZone, error) {
	return provider.DNSZone{ID: "zone", Name: "example.com", AccountID: c.AccountID, Verified: true}, nil
}
func (a *routeDNSAdapter) ListRecords(context.Context, provider.InfrastructureCredential, provider.DNSZone) ([]provider.DNSRecord, error) {
	return a.records, nil
}
func (a *routeDNSAdapter) UpsertRecord(_ context.Context, _ provider.InfrastructureCredential, req provider.DNSUpsertRequest) (provider.DNSRecord, error) {
	a.last = req
	if req.ReconcileOnly && req.Expected != nil && a.ownership {
		wanted := req.Record
		for _, record := range a.records {
			wanted.ID = record.ID
			if record == wanted {
				return record, nil
			}
		}
		return provider.DNSRecord{}, provider.ErrInfrastructureAmbiguous
	}
	prior, done, err := provider.PlanDNSUpsert(req, a.records, a.ownership)
	if err != nil {
		return provider.DNSRecord{}, err
	}
	if done {
		return prior, nil
	}
	a.writes++
	record := req.Record
	record.ID = "created"
	if req.Expected != nil {
		record.ID = "rotated"
	}
	for i, r := range a.records {
		if req.Expected != nil && r.ID == req.Expected.ID {
			a.records[i] = record
			return record, nil
		}
	}
	a.records = append(a.records, record)
	return record, nil
}
func (a *routeDNSAdapter) DeleteRecord(_ context.Context, _ provider.InfrastructureCredential, req provider.DNSDeleteRequest) error {
	for i, r := range a.records {
		if r.ID == req.Expected.ID {
			if err := provider.MatchDNSPrior(req.Expected, r); err != nil {
				return err
			}
			a.deleted = r.ID
			a.records = append(a.records[:i], a.records[i+1:]...)
			return nil
		}
	}
	return provider.ErrInfrastructureNotFound
}

func TestRouteDNSOwnershipAndDurableRecovery(t *testing.T) {
	for _, cloud := range []string{"digitalocean", "vercel"} {
		t.Run(cloud, func(t *testing.T) {
			adapter := &routeDNSAdapter{ownership: cloud == "vercel", records: []provider.DNSRecord{{ID: "unrelated", ZoneID: "zone", Name: "mail", Type: "TXT", Value: "keep", TTL: 60}}}
			registry := NewAdapters()
			if err := registry.RegisterDNS(cloud, adapter); err != nil {
				t.Fatal(err)
			}
			svc := NewService(Deps{Adapters: registry, Connections: routeDNSCredentials{cloud}})
			zone := &Zone{ID: "zone", OrgID: "org", Name: "example.com", Mode: "managed", ConnectionID: "dns", ConnectionGeneration: 1, Provider: cloud, AccountID: "account", RecordOwnership: adapter.ownership, ProviderZone: provider.DNSZone{ID: "zone", Name: "example.com"}}
			record, err := routeDNSRecord(zone, "app.example.com", "192.0.2.1", "route")
			if err != nil {
				t.Fatal(err)
			}
			if (record.Owner != "") != adapter.ownership {
				t.Fatal("incorrect ownership capability", record)
			}
			store := &routeDNSStore{}
			op := &RouteOperation{Kind: "apply", DesiredRecord: record}
			if err = svc.applyRouteDNS(t.Context(), store, op, zone); err != nil {
				t.Fatal(err)
			}
			if adapter.writes != 1 || store.durable.Receipt == nil {
				t.Fatal("receipt not persisted")
			}
			if err = svc.preflightRouteDNS(t.Context(), zone, record); !errors.Is(err, provider.ErrInfrastructureDrift) {
				t.Fatal("existing hostname accepted", err)
			}
			prior := *op.Receipt
			desired := prior
			desired.Value = "192.0.2.2"
			update := &RouteOperation{Kind: "cutover", DesiredRecord: desired, PreviousReceipt: &prior}
			store.failReceipt = true
			if err = svc.applyRouteDNS(t.Context(), store, update, zone); err == nil {
				t.Fatal("lost receipt not surfaced")
			}
			recovered := store.durable
			if !recovered.DNSAttempted || recovered.Receipt != nil {
				t.Fatal("attempt not durable before mutation")
			}
			err = svc.applyRouteDNS(t.Context(), store, &recovered, zone)
			if cloud == "vercel" {
				if err != nil || recovered.Receipt == nil || recovered.Receipt.ID != "rotated" || !adapter.last.ReconcileOnly || adapter.writes != 2 {
					t.Fatalf("rotated receipt recovery: %v %+v", err, recovered)
				}
				remove := &RouteOperation{Kind: "remove", Receipt: recovered.Receipt}
				if err = svc.applyRouteDNS(t.Context(), store, remove, zone); err != nil {
					t.Fatal(err)
				}
				if adapter.deleted != "rotated" || len(adapter.records) != 1 || adapter.records[0].ID != "unrelated" {
					t.Fatal("cleanup touched unrelated or stale receipt")
				}
			} else if err == nil || adapter.writes != 2 {
				t.Fatal("ID-only unknown update was replayed", err)
			}
		})
	}
}

type rotatedRouteCredential struct {
	conn *connections.Connection
	raw  []byte
}

func (c rotatedRouteCredential) Credential(context.Context, string, string) (*connections.Connection, []byte, error) {
	return c.conn, c.raw, nil
}

type identityRouteDNS struct {
	*routeDNSAdapter
	account, zoneID string
}

func (a *identityRouteDNS) GetZone(context.Context, provider.InfrastructureCredential, string) (provider.DNSZone, error) {
	return provider.DNSZone{ID: a.zoneID, Name: "example.com", AccountID: a.account}, nil
}

func TestRouteDNSCleanupAfterCredentialRotation(t *testing.T) {
	for _, variant := range []string{"same-account", "different-metadata-account", "different-provider-account", "different-zone"} {
		t.Run(variant, func(t *testing.T) {
			receipt := provider.DNSRecord{ID: "owned", ZoneID: "zone", Name: "app", Type: "A", Value: "192.0.2.1", TTL: 300}
			adapter := &identityRouteDNS{routeDNSAdapter: &routeDNSAdapter{records: []provider.DNSRecord{receipt}}, account: "account", zoneID: "zone"}
			conn := &connections.Connection{OrgID: "org", Cloud: "digitalocean", Generation: 2, Details: map[string]string{"account_uuid": "account"}}
			switch variant {
			case "different-metadata-account":
				conn.Details["account_uuid"] = "other"
			case "different-provider-account":
				adapter.account = "other"
			case "different-zone":
				adapter.zoneID = "other"
			}
			registry := NewAdapters()
			if err := registry.RegisterDNS("digitalocean", adapter); err != nil {
				t.Fatal(err)
			}
			svc := NewService(Deps{Adapters: registry, Connections: rotatedRouteCredential{conn: conn, raw: []byte("new-token")}})
			zone := &Zone{OrgID: "org", Name: "example.com", Mode: "managed", ConnectionID: "dns", ConnectionGeneration: 1, Provider: "digitalocean", AccountID: "account", ProviderZone: provider.DNSZone{ID: "zone", Name: "example.com", AccountID: "account"}}
			if _, _, err := svc.zoneCredential(t.Context(), zone); err == nil {
				t.Fatal("new work accepted rotated generation")
			}
			err := svc.applyRouteDNS(t.Context(), &routeDNSStore{}, &RouteOperation{Kind: "remove", Receipt: &receipt}, zone)
			if variant == "same-account" {
				if err != nil || adapter.deleted != "owned" {
					t.Fatal("cleanup refused same account", err)
				}
			} else if err == nil || adapter.deleted != "" {
				t.Fatal("cleanup crossed physical account/zone", err)
			}
		})
	}
}

func TestRouteRollbackBeforeDNSWritePreservesIDOnlyReceipt(t *testing.T) {
	receipt := provider.DNSRecord{ID: "source", ZoneID: "zone", Name: "app", Type: "A", Value: "192.0.2.1", TTL: 300}
	adapter := &routeDNSAdapter{records: []provider.DNSRecord{receipt}}
	registry := NewAdapters()
	if err := registry.RegisterDNS("digitalocean", adapter); err != nil {
		t.Fatal(err)
	}
	svc := NewService(Deps{Adapters: registry, Connections: routeDNSCredentials{"digitalocean"}})
	zone := &Zone{OrgID: "org", Name: "example.com", Mode: "managed", ConnectionID: "dns", ConnectionGeneration: 1, Provider: "digitalocean", AccountID: "account", ProviderZone: provider.DNSZone{ID: "zone", Name: "example.com"}}
	op := &RouteOperation{Kind: "rollback", DesiredRecord: receipt, PreviousReceipt: &receipt}
	if err := svc.applyRouteDNS(t.Context(), &routeDNSStore{}, op, zone); err != nil {
		t.Fatal(err)
	}
	if op.Receipt == nil || op.Receipt.ID != "source" || adapter.writes != 0 || adapter.deleted != "" || len(adapter.records) != 1 {
		t.Fatal("pre-DNS rollback mutated known source record")
	}
}

func TestRouteCleanupCredentialIdentity(t *testing.T) {
	route := Route{OrgID: "org", Binding: RouteBinding{RuntimeID: "daemon", ConnectionGeneration: 1}}
	conn := &connections.Connection{OrgID: "org", Cloud: "server", Generation: 2, Details: map[string]string{"docker_id": "daemon"}}
	if err := routeCredentialIdentity(route, conn, false); err == nil {
		t.Fatal("new work accepted rotated generation")
	}
	if err := routeCredentialIdentity(route, conn, true); err != nil {
		t.Fatal(err)
	}
	conn.Details["docker_id"] = "different"
	if err := routeCredentialIdentity(route, conn, true); err == nil {
		t.Fatal("cleanup accepted different daemon")
	}
	route.Binding.RuntimeID = ""
	if err := routeCredentialIdentity(route, conn, true); err == nil {
		t.Fatal("missing accepted identity was backfilled")
	}
}

type pendingCutoverStore struct {
	DomainStore
	op RouteOperation
}

func (s *pendingCutoverStore) RouteOperation(context.Context, string, string) (*RouteOperation, error) {
	return &s.op, nil
}

type cleanupTestAuth struct{}

func (cleanupTestAuth) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}
func TestRollbackRefusesUnresolvedDNSAttempt(t *testing.T) {
	st := &pendingCutoverStore{op: RouteOperation{Kind: "cutover", Status: "failed", DNSAttempted: true, Route: Route{ProjectID: "project"}, Source: &Route{}, PreviousReceipt: &provider.DNSRecord{ID: "known-old"}}}
	svc := NewService(Deps{Domains: st, Authz: cleanupTestAuth{}})
	if _, err := svc.RollbackRoute(t.Context(), security.Principal{OrgID: "org", UserID: "manager"}, "project", "cutover"); !errors.Is(err, ErrConflict) {
		t.Fatal("unresolved DNS attempt authorized rollback", err)
	}
}
func TestHandoffReleaseNeedsDrainAndIndependentVerification(t *testing.T) {
	svc := NewService(Deps{})
	now := time.Now()
	svc.now = func() time.Time { return now }
	for _, missing := range []string{"drain", "dns", "tls", "backend"} {
		t.Run(missing, func(t *testing.T) {
			op := &RouteOperation{Kind: "release", NotBefore: now.Add(-time.Second), Route: Route{Verification: RouteVerification{DNS: true, TLS: true, Backend: true}}}
			switch missing {
			case "drain":
				op.NotBefore = now.Add(time.Second)
			case "dns":
				op.Route.Verification.DNS = false
			case "tls":
				op.Route.Verification.TLS = false
			case "backend":
				op.Route.Verification.Backend = false
			}
			if err := svc.releaseVerifiedSource(t.Context(), nil, op, nil); !errors.Is(err, ErrPending) {
				t.Fatal("cleanup passed incomplete verification", err)
			}
		})
	}
	backend := server.IngressBackend{OrgID: "org", ProjectID: "project", ContainerID: "retired"}
	cfg := server.IngressConfig{Routes: []server.IngressRoute{{Backend: server.IngressBackend{ContainerID: "current"}, PreparedBackend: &backend}}}
	if !ingressReferences(cfg, backend) {
		t.Fatal("prepared backend treated as quiescent")
	}
}
