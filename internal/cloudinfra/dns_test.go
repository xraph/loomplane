package cloudinfra

import (
	"context"
	"errors"
	"testing"

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
