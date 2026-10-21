package digitalocean

import (
	"errors"
	"net/http"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func infrastructureDNS(f *fake) provider.DNSZone {
	infrastructureAccount(f)
	f.on("GET /v2/domains/example.test", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"domain": map[string]any{"name": "example.test"}})
	})
	return provider.DNSZone{ID: "example.test", Name: "example.test", AccountID: "team-1", Verified: true}
}
func TestInfrastructureDNSRequiresExactPriorReceipt(t *testing.T) {
	f, c := newFake(t)
	zone := infrastructureDNS(f)
	record := map[string]any{"id": 7, "name": "api", "type": "A", "data": "192.0.2.1", "ttl": 60}
	f.on("GET /v2/domains/example.test/records", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"domain_records": []any{record}})
	})
	changes := 0
	f.on("PUT /v2/domains/example.test/records/7", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		changes++
		record["data"] = body["data"]
		reply(w, 200, map[string]any{"domain_record": record})
	})
	f.on("DELETE /v2/domains/example.test/records/7", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) { changes++; w.WriteHeader(204) })
	prior := provider.DNSRecord{ID: "7", ZoneID: zone.ID, Name: "api", Type: "A", Value: "192.0.2.1", TTL: 60}
	desired := prior
	desired.Value = "192.0.2.2"
	result, err := c.UpsertRecord(t.Context(), infrastructureCredential(), provider.DNSUpsertRequest{Zone: zone, Record: desired, Expected: &prior})
	if err != nil || result != desired || changes != 1 {
		t.Fatalf("update: %+v %v", result, err)
	}
	if err = c.DeleteRecord(t.Context(), infrastructureCredential(), provider.DNSDeleteRequest{Zone: zone, Expected: prior}); !errors.Is(err, provider.ErrInfrastructureDrift) || changes != 1 {
		t.Fatalf("drifted delete: %v", err)
	}
	if err = c.DeleteRecord(t.Context(), infrastructureCredential(), provider.DNSDeleteRequest{Zone: zone, Expected: desired}); err != nil || changes != 2 {
		t.Fatalf("owned delete: %v", err)
	}
	desired.ID = ""
	if _, err = c.UpsertRecord(t.Context(), infrastructureCredential(), provider.DNSUpsertRequest{Zone: zone, Record: desired}); !errors.Is(err, provider.ErrInfrastructureDrift) {
		t.Fatalf("foreign create collision: %v", err)
	}
}
func TestInfrastructureDNSPagesScopeAndZoneIdentity(t *testing.T) {
	f, c := newFake(t)
	zone := infrastructureDNS(f)
	f.on("GET /v2/domains/example.test/records", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.URL.Query().Get("page") == "1" {
			reply(w, 200, map[string]any{"domain_records": []any{map[string]any{"id": 1, "name": "@", "type": "NS", "data": "ns.example.test", "ttl": 1800}}, "links": map[string]any{"pages": map[string]any{"next": "http://evil.invalid"}}})
			return
		}
		reply(w, 200, map[string]any{"domain_records": []any{map[string]any{"id": 2, "name": "api", "type": "A", "data": "192.0.2.1", "ttl": 60}}})
	})
	records, err := c.ListRecords(t.Context(), infrastructureCredential(), zone)
	if err != nil || len(records) != 2 || records[0].Name != "" {
		t.Fatalf("records: %+v %v", records, err)
	}
	zone.AccountID = "foreign"
	if _, err = c.ListRecords(t.Context(), infrastructureCredential(), zone); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatalf("zone account: %v", err)
	}
	zone.AccountID = "team-1"
	f.on("GET /v2/domains/example.test/records", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 403, map[string]any{"message": "missing domain:read"})
	})
	if _, err = c.ListRecords(t.Context(), infrastructureCredential(), zone); !errors.Is(err, provider.ErrInfrastructureScope) {
		t.Fatalf("DNS permission: %v", err)
	}
}
func TestInfrastructureDNSCreateDoesNotReplaceApexAndAmbiguityDoesNotRetry(t *testing.T) {
	f, c := newFake(t)
	zone := infrastructureDNS(f)
	f.on("GET /v2/domains/example.test/records", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"domain_records": []any{}})
	})
	writes := 0
	f.on("POST /v2/domains/example.test/records", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		writes++
		if body["name"] != "test-api" {
			t.Error("unexpected apex mutation")
		}
		reply(w, 201, map[string]any{"domain_record": map[string]any{"id": 8, "name": body["name"], "type": body["type"], "data": body["data"], "ttl": body["ttl"]}})
	})
	request := provider.DNSUpsertRequest{Zone: zone, Record: provider.DNSRecord{ZoneID: zone.ID, Name: "test-api", Type: "A", Value: "192.0.2.1", TTL: 60}}
	record, err := c.UpsertRecord(t.Context(), infrastructureCredential(), request)
	if err != nil || record.ID != "8" || writes != 1 {
		t.Fatalf("create: %+v %v", record, err)
	}
	request.ReconcileOnly = true
	if _, err = c.UpsertRecord(t.Context(), infrastructureCredential(), request); !errors.Is(err, provider.ErrInfrastructureAmbiguous) || writes != 1 {
		t.Fatalf("blind retry: %v", err)
	}
}

func TestInfrastructureDNSZoneDiscoveryAndCreate(t *testing.T) {
	f, c := newFake(t)
	infrastructureAccount(f)
	calls := 0
	f.on("GET /v2/domains", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		calls++
		if r.URL.Query().Get("page") == "1" {
			reply(w, 200, map[string]any{"domains": []any{map[string]any{"name": "example.test"}}, "links": map[string]any{"pages": map[string]any{"next": "page=2"}}})
			return
		}
		reply(w, 200, map[string]any{"domains": []any{}})
	})
	f.on("POST /v2/domains", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		if len(body) != 1 || body["name"] != "example.test" {
			t.Errorf("zone create changed initial records: %v", body)
		}
		reply(w, 201, map[string]any{"domain": map[string]any{"name": "example.test"}})
	})
	zones, err := c.ListZones(t.Context(), infrastructureCredential())
	if err != nil || len(zones) != 1 || calls != 2 {
		t.Fatalf("zones: %+v %v", zones, err)
	}
	zone, err := c.CreateZone(t.Context(), infrastructureCredential(), provider.DNSZoneSpec{Name: "example.test"})
	if err != nil || zone.ID != "example.test" {
		t.Fatalf("create: %+v %v", zone, err)
	}
}
