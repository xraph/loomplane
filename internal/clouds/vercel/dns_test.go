package vercel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

type dnsFixture struct {
	records  []infrastructureRecord
	writes   int
	status   int
	next     *int64
	zoneTeam string
	lastBody map[string]any
}

func newDNSFixture(t *testing.T) (*dnsFixture, *Client, provider.InfrastructureCredential, provider.DNSZone) {
	t.Helper()
	f := &dnsFixture{zoneTeam: "team_a"}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Error("authorization missing")
		}
		if r.URL.Path == "/v2/teams/team_a" {
			fmt.Fprint(w, `{"id":"team_a","slug":"renamed"}`)
			return
		}
		if r.URL.Query().Get("teamId") != "team_a" {
			t.Errorf("lost team scope: %s", r.URL)
		}
		if f.status != 0 {
			w.WriteHeader(f.status)
			fmt.Fprint(w, `{"error":{"message":"secret-token"}}`)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v5/domains/example.test":
			_ = json.NewEncoder(w).Encode(map[string]any{"domain": map[string]any{"id": "zone_a", "name": "example.test", "teamId": f.zoneTeam, "verified": true, "serviceType": "zeit.world", "nameservers": []string{"ns1.vercel-dns.com", "ns2.vercel-dns.com"}}})
		case "GET /v5/domains/example.test/records":
			_ = json.NewEncoder(w).Encode(map[string]any{"records": f.records, "pagination": map[string]any{"next": f.next}})
		case "POST /v2/domains/example.test/records":
			f.writes++
			_ = json.NewDecoder(r.Body).Decode(&f.lastBody)
			var record infrastructureRecord
			data, _ := json.Marshal(f.lastBody)
			_ = json.Unmarshal(data, &record)
			record.ID = "rec_a"
			f.records = append(f.records, record)
			fmt.Fprint(w, `{"uid":"rec_a","updated":100}`)
		case "PATCH /v1/domains/records/rec_a":
			f.writes++
			_ = json.NewDecoder(r.Body).Decode(&f.lastBody)
			var record infrastructureRecord
			data, _ := json.Marshal(f.lastBody)
			_ = json.Unmarshal(data, &record)
			record.ID = "rec_a"
			record.Domain = "example.test"
			record.RecordType = record.Type
			record.Type = "record"
			f.records = []infrastructureRecord{record}
			_ = json.NewEncoder(w).Encode(record)
		case "DELETE /v2/domains/example.test/records/rec_a":
			f.writes++
			f.records = nil
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected API call: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	credential := provider.InfrastructureCredential{Provider: "vercel", AccountID: "team_a", TeamID: "team_a", Token: "secret-token"}
	zone := provider.DNSZone{ID: "zone_a", Name: "example.test", AccountID: "team_a", Verified: true}
	return f, New(api.URL), credential, zone
}

func TestDNSRecordActualAPICreatePatchDeleteContracts(t *testing.T) {
	f, c, credential, zone := newDNSFixture(t)
	request := provider.DNSUpsertRequest{Zone: zone, Record: provider.DNSRecord{ZoneID: zone.ID, Name: "test-api", Type: "A", Value: "192.0.2.1", TTL: 60, Owner: "loomplane:route-1"}}
	record, err := c.UpsertRecord(t.Context(), credential, request)
	if err != nil || record.ID != "rec_a" || f.writes != 1 || f.lastBody["comment"] != "loomplane:route-1" || f.lastBody["ttl"] != float64(60) {
		t.Fatalf("create: %+v %v %v", record, err, f.lastBody)
	}
	request.ReconcileOnly = true
	recovered, err := c.UpsertRecord(t.Context(), credential, request)
	if err != nil || recovered != record || f.writes != 1 {
		t.Fatalf("lost create reconciliation: %+v %v", recovered, err)
	}
	desired := record
	desired.Value = "192.0.2.2"
	request = provider.DNSUpsertRequest{Zone: zone, Record: desired, Expected: &record}
	updated, err := c.UpsertRecord(t.Context(), credential, request)
	if err != nil || updated != desired || f.writes != 2 {
		t.Fatalf("patch recordType/domain: %+v %v", updated, err)
	}
	if err = c.DeleteRecord(t.Context(), credential, provider.DNSDeleteRequest{Zone: zone, Expected: record}); !errors.Is(err, provider.ErrInfrastructureDrift) || f.writes != 2 {
		t.Fatalf("stale cleanup: %v", err)
	}
	if err = c.DeleteRecord(t.Context(), credential, provider.DNSDeleteRequest{Zone: zone, Expected: updated}); err != nil || f.writes != 3 {
		t.Fatalf("delete HTTP 200: %v", err)
	}
	if err = c.DeleteRecord(t.Context(), credential, provider.DNSDeleteRequest{Zone: zone, Expected: updated}); err != nil || f.writes != 3 {
		t.Fatalf("idempotent delete: %v", err)
	}
}
func TestDNSRejectsForeignOwnerWrongZoneScopeErrorsAndCancellation(t *testing.T) {
	f, c, credential, zone := newDNSFixture(t)
	request := provider.DNSUpsertRequest{Zone: zone, Record: provider.DNSRecord{ZoneID: zone.ID, Name: "test-api", Type: "A", Value: "192.0.2.1", TTL: 60, Owner: "loomplane:route-1"}}
	f.records = []infrastructureRecord{{ID: "foreign", Name: "test-api", Type: "A", Value: "192.0.2.1", TTL: 60, Comment: "foreign"}}
	if _, err := c.UpsertRecord(t.Context(), credential, request); !errors.Is(err, provider.ErrInfrastructureDrift) || f.writes != 0 {
		t.Fatalf("foreign record: %v", err)
	}
	f.zoneTeam = "team_other"
	if _, err := c.ListRecords(t.Context(), credential, zone); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatalf("wrong team: %v", err)
	}
	f.zoneTeam = "team_a"
	for _, status := range []int{403, 404, 429, 500} {
		f.status = status
		_, err := c.ListRecords(t.Context(), credential, zone)
		if err == nil || strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("unsafe HTTP %d: %v", status, err)
		}
		if status == 403 && !errors.Is(err, provider.ErrInfrastructureScope) {
			t.Fatalf("scope: %v", err)
		}
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.ListRecords(canceled, credential, zone); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
func TestDNSPaginationUsesUntilAndRetainsTeam(t *testing.T) {
	pages := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/teams/team_a":
			fmt.Fprint(w, `{"id":"team_a"}`)
		case "/v5/domains/example.test":
			fmt.Fprint(w, `{"domain":{"id":"zone_a","name":"example.test","teamId":"team_a","verified":true,"serviceType":"zeit.world"}}`)
		case "/v5/domains/example.test/records":
			pages++
			if r.URL.Query().Get("teamId") != "team_a" || r.URL.Query().Get("limit") != "100" {
				t.Error("scope or page size")
			}
			if pages == 1 {
				fmt.Fprint(w, `{"records":[{"id":"rec_1","name":"a","type":"A","value":"192.0.2.1","ttl":60}],"pagination":{"next":99}}`)
				return
			}
			if r.URL.Query().Get("until") != "99" {
				t.Error("wrong next cursor")
			}
			fmt.Fprint(w, `{"records":[{"id":"rec_2","name":"b","type":"TXT","value":"hello","ttl":60}],"pagination":{"next":null}}`)
		default:
			t.Error("unexpected endpoint")
		}
	}))
	defer api.Close()
	records, err := New(api.URL).ListRecords(t.Context(), provider.InfrastructureCredential{Provider: "vercel", AccountID: "team_a", TeamID: "team_a", Token: "secret"}, provider.DNSZone{ID: "zone_a", Name: "example.test", AccountID: "team_a"})
	if err != nil || len(records) != 2 || pages != 2 {
		t.Fatalf("pagination: %+v %v", records, err)
	}
}
func TestDNSRepeatedCursorAndMissingCreateStayBounded(t *testing.T) {
	f, c, credential, zone := newDNSFixture(t)
	next := int64(99)
	f.next = &next
	if _, err := c.ListRecords(t.Context(), credential, zone); !errors.Is(err, provider.ErrInfrastructurePagination) {
		t.Fatalf("repeated cursor: %v", err)
	}
	f.next = nil
	request := provider.DNSUpsertRequest{Zone: zone, Record: provider.DNSRecord{ZoneID: zone.ID, Name: "test-api", Type: "A", Value: "192.0.2.1", TTL: 60, Owner: "loomplane:route-1"}, ReconcileOnly: true}
	if _, err := c.UpsertRecord(t.Context(), credential, request); !errors.Is(err, provider.ErrInfrastructureAmbiguous) || f.writes != 0 {
		t.Fatalf("blind retry: %v", err)
	}
}

func TestDNSZoneDiscoveryAndCreationPreserveRegistrarAndNameservers(t *testing.T) {
	exists := false
	pages := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/teams/team_a" {
			fmt.Fprint(w, `{"id":"team_a"}`)
			return
		}
		if r.URL.Query().Get("teamId") != "team_a" {
			t.Error("lost team scope")
		}
		domain := map[string]any{"id": "zone_a", "name": "example.test", "teamId": "team_a", "verified": true, "serviceType": "zeit.world", "nameservers": []string{"ns1.vercel-dns.com", "ns2.vercel-dns.com"}}
		switch r.Method + " " + r.URL.Path {
		case "GET /v5/domains/example.test":
			if !exists {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"error":{"message":"missing"}}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"domain": domain})
		case "GET /v5/domains":
			pages++
			if r.URL.Query().Get("until") == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{"domains": []any{domain}, "pagination": map[string]any{"next": 99}})
				return
			}
			if r.URL.Query().Get("until") != "99" {
				t.Error("zone cursor")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"domains": []any{}, "pagination": map[string]any{"next": nil}})
		case "POST /v7/domains":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body) != 3 || body["method"] != "add" || body["zone"] != true || body["name"] != "example.test" {
				t.Errorf("unexpected domain operation: %v", body)
			}
			exists = true
			_ = json.NewEncoder(w).Encode(map[string]any{"domain": domain})
		default:
			t.Error("unexpected domain operation")
		}
	}))
	defer api.Close()
	c := New(api.URL)
	credential := provider.InfrastructureCredential{Provider: "vercel", AccountID: "team_a", TeamID: "team_a", Token: "fixture"}
	zone, err := c.CreateZone(t.Context(), credential, provider.DNSZoneSpec{Name: "example.test"})
	if err != nil || zone.ID != "zone_a" || len(zone.Nameservers) != 2 {
		t.Fatalf("zone create: %+v %v", zone, err)
	}
	zones, err := c.ListZones(t.Context(), credential)
	if err != nil || len(zones) != 1 || pages != 2 {
		t.Fatalf("zones: %+v %v", zones, err)
	}
}
