package cloudflare_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/pkg/provider"
)

var _ provider.DNSAdapter = (*cloudflare.Client)(nil)

type cfRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
}

// dnsFixture is a Cloudflare zone with a record list. Requests run one at
// a time, so the fields need no lock.
type dnsFixture struct {
	records     []cfRecord
	writes      int
	status      int
	zoneStatus  string
	zoneAccount string
	perPage     int
	pagesServed int
	lastBody    map[string]any
	zoneCreated bool
	zoneExists  bool
	nextID      int
	deleteGone  bool
}

func (f *dnsFixture) zone() map[string]any {
	return map[string]any{"id": "zone1", "name": "example.test", "status": f.zoneStatus, "account": map[string]any{"id": f.zoneAccount}, "name_servers": []string{"a.ns.test", "b.ns.test"}}
}

func newDNSFixture(t *testing.T) (*dnsFixture, *cloudflare.Client, provider.InfrastructureCredential, provider.DNSZone) {
	t.Helper()
	f := &dnsFixture{zoneStatus: "active", zoneAccount: "acct1", zoneExists: true}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Error("authorization missing")
		}
		if f.status != 0 {
			w.WriteHeader(f.status)
			fmt.Fprint(w, `{"success":false,"errors":[{"code":9109,"message":"secret-token"}],"result":null}`)
			return
		}
		reply := func(result any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result})
		}
		const records = "/zones/zone1/dns_records"
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone1":
			reply(f.zone())
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			list := []any{}
			if f.zoneExists && r.URL.Query().Get("name") == "example.test" && r.URL.Query().Get("account.id") == "acct1" {
				list = append(list, f.zone())
			}
			reply(list)
		case r.Method == http.MethodPost && r.URL.Path == "/zones":
			f.writes++
			f.zoneCreated = true
			_ = json.NewDecoder(r.Body).Decode(&f.lastBody)
			f.zoneExists = true
			reply(f.zone())
		case r.Method == http.MethodGet && r.URL.Path == records:
			f.pagesServed++
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			size := f.perPage
			if size == 0 {
				size = len(f.records) + 1
			}
			if r.URL.Query().Get("per_page") != "100" {
				t.Errorf("per_page %q", r.URL.Query().Get("per_page"))
			}
			total := (len(f.records) + size - 1) / size
			start, end := (page-1)*size, page*size
			if start > len(f.records) {
				start = len(f.records)
			}
			if end > len(f.records) {
				end = len(f.records)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": f.records[start:end], "result_info": map[string]any{"page": page, "per_page": size, "total_pages": total}})
		case r.Method == http.MethodPost && r.URL.Path == records:
			f.writes++
			_ = json.NewDecoder(r.Body).Decode(&f.lastBody)
			f.nextID++
			rec := fromBody(f.lastBody)
			rec.ID = "rec" + strconv.Itoa(f.nextID)
			f.records = append(f.records, rec)
			reply(withZone(rec))
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, records+"/"):
			f.writes++
			_ = json.NewDecoder(r.Body).Decode(&f.lastBody)
			id := strings.TrimPrefix(r.URL.Path, records+"/")
			for i := range f.records {
				if f.records[i].ID == id {
					rec := fromBody(f.lastBody)
					rec.ID = id
					f.records[i] = rec
					reply(withZone(rec))
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"success":false,"errors":[{"code":81044}],"result":null}`)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, records+"/"):
			f.writes++
			id := strings.TrimPrefix(r.URL.Path, records+"/")
			for i := range f.records {
				if f.records[i].ID == id && !f.deleteGone {
					f.records = append(f.records[:i], f.records[i+1:]...)
					reply(map[string]any{"id": id})
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"success":false,"errors":[{"code":81044}],"result":null}`)
		default:
			t.Errorf("unexpected API call: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	credential := provider.InfrastructureCredential{Provider: "cloudflare", AccountID: "acct1", Token: "secret-token"}
	zone := provider.DNSZone{ID: "zone1", Name: "example.test", AccountID: "acct1", Verified: true}
	return f, cloudflare.New(api.URL, false), credential, zone
}

func fromBody(body map[string]any) cfRecord {
	rec := cfRecord{}
	data, _ := json.Marshal(body)
	_ = json.Unmarshal(data, &rec)
	return rec
}

func withZone(rec cfRecord) map[string]any {
	return map[string]any{"id": rec.ID, "zone_id": "zone1", "zone_name": "example.test", "name": rec.Name, "type": rec.Type, "content": rec.Content, "ttl": rec.TTL, "proxied": rec.Proxied, "comment": rec.Comment}
}

func upsert(zone provider.DNSZone, name string) provider.DNSUpsertRequest {
	return provider.DNSUpsertRequest{Zone: zone, Record: provider.DNSRecord{ZoneID: zone.ID, Name: name, Type: "A", Value: "192.0.2.1", TTL: 60, Owner: "loomplane:route:r"}}
}

func TestCloudflareDNSRefusesProxiedAndForeignRecords(t *testing.T) {
	f, c, cred, zone := newDNSFixture(t)
	f.records = []cfRecord{{ID: "user", Name: "app.example.test", Type: "A", Content: "192.0.2.9", TTL: 60, Proxied: true}}
	req := provider.DNSUpsertRequest{Zone: zone, Record: provider.DNSRecord{ZoneID: zone.ID, Name: "app", Type: "A", Value: "192.0.2.1", TTL: 60, Owner: "loomplane:route:r"}}
	if _, err := c.UpsertRecord(t.Context(), cred, req); !errors.Is(err, provider.ErrInfrastructureDrift) || f.writes != 0 {
		t.Fatalf("%v writes=%d", err, f.writes)
	}
	f.records = []cfRecord{{ID: "user", Name: "app.example.test", Type: "A", Content: "192.0.2.1", TTL: 60, Comment: "loomplane:route:r", Proxied: true}}
	if _, err := c.UpsertRecord(t.Context(), cred, req); !errors.Is(err, provider.ErrInfrastructureDrift) || f.writes != 0 {
		t.Fatalf("adopted proxied record: %v writes=%d", err, f.writes)
	}
	f.records = []cfRecord{{ID: "user", Name: "app.example.test", Type: "A", Content: "192.0.2.1", TTL: 60, Comment: "someone else"}}
	if _, err := c.UpsertRecord(t.Context(), cred, req); !errors.Is(err, provider.ErrInfrastructureDrift) || f.writes != 0 {
		t.Fatalf("foreign record: %v writes=%d", err, f.writes)
	}
}

func TestCloudflareDNSProxiedRecordIsOwnedOnlyByExactReceipt(t *testing.T) {
	f, c, cred, zone := newDNSFixture(t)
	f.records = []cfRecord{{ID: "rec9", Name: "app.example.test", Type: "A", Content: "192.0.2.1", TTL: 60, Comment: "loomplane:route:r", Proxied: true}}
	listed, err := c.ListRecords(t.Context(), cred, zone)
	if err != nil || len(listed) != 1 || listed[0].Owner != "" {
		t.Fatalf("a proxied record was listed as owned: %+v %v", listed, err)
	}
	receipt := provider.DNSRecord{ID: "rec9", ZoneID: zone.ID, Name: "app", Type: "A", Value: "192.0.2.1", TTL: 60, Owner: "loomplane:route:r"}
	desired := receipt
	desired.Value = "192.0.2.2"
	got, err := c.UpsertRecord(t.Context(), cred, provider.DNSUpsertRequest{Zone: zone, Record: desired, Expected: &receipt})
	if err != nil || got != desired || f.records[0].Proxied || f.lastBody["proxied"] != false {
		t.Fatalf("%+v %v %+v", got, err, f.records)
	}
	// A stale receipt with another ID still can't reach it.
	other := receipt
	other.ID = "rec8"
	f.records[0].Proxied = true
	if _, err = c.UpsertRecord(t.Context(), cred, provider.DNSUpsertRequest{Zone: zone, Record: other, Expected: &other}); !errors.Is(err, provider.ErrInfrastructureDrift) {
		t.Fatal(err)
	}
}

func TestCloudflareDNSAutomaticTTLIsNeverOwned(t *testing.T) {
	f, c, cred, zone := newDNSFixture(t)
	f.records = []cfRecord{{ID: "auto", Name: "app.example.test", Type: "A", Content: "192.0.2.1", TTL: 1, Comment: "loomplane:route:r"}}
	listed, err := c.ListRecords(t.Context(), cred, zone)
	if err != nil || len(listed) != 1 || listed[0].TTL != 300 || listed[0].Owner != "" {
		t.Fatalf("%+v %v", listed, err)
	}
	req := upsert(zone, "app")
	req.Record.TTL = 300
	if _, err = c.UpsertRecord(t.Context(), cred, req); !errors.Is(err, provider.ErrInfrastructureDrift) || f.writes != 0 {
		t.Fatalf("adopted an automatic TTL record: %v writes=%d", err, f.writes)
	}
}

func TestCloudflareDNSMapsApexAndWildcardNames(t *testing.T) {
	f, c, cred, zone := newDNSFixture(t)
	for name, fqdn := range map[string]string{"": "example.test", "*": "*.example.test", "api": "api.example.test", "*.sub": "*.sub.example.test"} {
		f.records = nil
		created, err := c.UpsertRecord(t.Context(), cred, upsert(zone, name))
		if err != nil || created.Name != name || f.lastBody["name"] != fqdn {
			t.Fatalf("%q: %+v %v body=%v", name, created, err, f.lastBody)
		}
		if f.records[0].Name != fqdn {
			t.Fatalf("stored %q", f.records[0].Name)
		}
		listed, err := c.ListRecords(t.Context(), cred, zone)
		if err != nil || len(listed) != 1 || listed[0] != created {
			t.Fatalf("%q: %+v %v", name, listed, err)
		}
	}
	f.records = []cfRecord{{ID: "x", Name: "app.elsewhere.test", Type: "A", Content: "192.0.2.1", TTL: 60}}
	if _, err := c.ListRecords(t.Context(), cred, zone); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatalf("record from another zone: %v", err)
	}
}

func TestCloudflareDNSWritesSendUnproxiedOwnedRecordsAndPatchInPlace(t *testing.T) {
	f, c, cred, zone := newDNSFixture(t)
	created, err := c.UpsertRecord(t.Context(), cred, upsert(zone, "app"))
	if err != nil || created.ID == "" || f.lastBody["proxied"] != false || f.lastBody["comment"] != "loomplane:route:r" || f.lastBody["ttl"] != float64(60) || f.lastBody["content"] != "192.0.2.1" || f.lastBody["type"] != "A" {
		t.Fatalf("%+v %v %v", created, err, f.lastBody)
	}
	again := upsert(zone, "app")
	again.ReconcileOnly = true
	if got, err := c.UpsertRecord(t.Context(), cred, again); err != nil || got != created || f.writes != 1 {
		t.Fatalf("reconcile: %+v %v writes=%d", got, err, f.writes)
	}
	desired := created
	desired.Value = "192.0.2.2"
	updated, err := c.UpsertRecord(t.Context(), cred, provider.DNSUpsertRequest{Zone: zone, Record: desired, Expected: &created})
	if err != nil || updated != desired || updated.ID != created.ID || f.writes != 2 {
		t.Fatalf("update: %+v %v", updated, err)
	}
	if err = c.DeleteRecord(t.Context(), cred, provider.DNSDeleteRequest{Zone: zone, Expected: created}); !errors.Is(err, provider.ErrInfrastructureDrift) || f.writes != 2 {
		t.Fatalf("stale delete: %v", err)
	}
	foreign := updated
	foreign.Owner = "someone else"
	if err = c.DeleteRecord(t.Context(), cred, provider.DNSDeleteRequest{Zone: zone, Expected: foreign}); !errors.Is(err, provider.ErrInfrastructureDrift) || f.writes != 2 {
		t.Fatalf("foreign delete: %v", err)
	}
	if err = c.DeleteRecord(t.Context(), cred, provider.DNSDeleteRequest{Zone: zone, Expected: updated}); err != nil || f.writes != 3 || len(f.records) != 0 {
		t.Fatalf("delete: %v", err)
	}
	if err = c.DeleteRecord(t.Context(), cred, provider.DNSDeleteRequest{Zone: zone, Expected: updated}); err != nil || f.writes != 3 {
		t.Fatalf("idempotent delete: %v", err)
	}
}

func TestCloudflareDNSDeleteTreatsNotFoundAsDone(t *testing.T) {
	f, c, cred, zone := newDNSFixture(t)
	created, err := c.UpsertRecord(t.Context(), cred, upsert(zone, "app"))
	if err != nil {
		t.Fatal(err)
	}
	// The record is listed, then gone by the time the DELETE arrives.
	f.deleteGone = true
	if err = c.DeleteRecord(t.Context(), cred, provider.DNSDeleteRequest{Zone: zone, Expected: created}); err != nil {
		t.Fatal(err)
	}
}

func TestCloudflareDNSPagination(t *testing.T) {
	f, c, cred, zone := newDNSFixture(t)
	for i := 0; i < 5; i++ {
		f.records = append(f.records, cfRecord{ID: "r" + strconv.Itoa(i), Name: "h" + strconv.Itoa(i) + ".example.test", Type: "A", Content: "192.0.2.1", TTL: 60})
	}
	f.perPage = 2
	listed, err := c.ListRecords(t.Context(), cred, zone)
	if err != nil || len(listed) != 5 || f.pagesServed != 3 {
		t.Fatalf("%d records %d pages %v", len(listed), f.pagesServed, err)
	}
	f.records[4].ID = "r0"
	if _, err = c.ListRecords(t.Context(), cred, zone); !errors.Is(err, provider.ErrInfrastructurePagination) {
		t.Fatalf("repeated ID: %v", err)
	}
	f.records = nil
	for i := 0; i < 101; i++ {
		f.records = append(f.records, cfRecord{ID: "r" + strconv.Itoa(i), Name: "h" + strconv.Itoa(i) + ".example.test", Type: "A", Content: "192.0.2.1", TTL: 60})
	}
	f.perPage, f.pagesServed = 1, 0
	if _, err = c.ListRecords(t.Context(), cred, zone); !errors.Is(err, provider.ErrInfrastructurePagination) || f.pagesServed != 100 {
		t.Fatalf("unbounded pages (%d): %v", f.pagesServed, err)
	}
}

func TestCloudflareDNSNeverReturnsBodiesOrTokens(t *testing.T) {
	f, c, cred, zone := newDNSFixture(t)
	for _, status := range []int{403, 404, 429, 500} {
		f.status = status
		calls := map[string]func() error{
			"records": func() error { _, err := c.ListRecords(t.Context(), cred, zone); return err },
			"zones":   func() error { _, err := c.ListZones(t.Context(), cred); return err },
			"zone":    func() error { _, err := c.GetZone(t.Context(), cred, "example.test"); return err },
			"upsert":  func() error { _, err := c.UpsertRecord(t.Context(), cred, upsert(zone, "app")); return err },
		}
		for name, call := range calls {
			err := call()
			if err == nil || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "9109") {
				t.Fatalf("%s HTTP %d: %v", name, status, err)
			}
			if status == 403 && !errors.Is(err, provider.ErrInfrastructureScope) {
				t.Fatalf("%s scope: %v", name, err)
			}
			if status == 404 && name == "records" && !errors.Is(err, provider.ErrInfrastructureNotFound) {
				t.Fatalf("%s not found: %v", name, err)
			}
		}
	}
}

func TestCloudflareZoneVerifiedOnlyWhenActive(t *testing.T) {
	f, c, cred, _ := newDNSFixture(t)
	f.zoneStatus = "pending"
	zone, err := c.GetZone(t.Context(), cred, "example.test")
	if err != nil || zone.Verified || zone.ID != "zone1" || zone.AccountID != "acct1" || len(zone.Nameservers) != 2 {
		t.Fatalf("%+v %v", zone, err)
	}
	f.zoneStatus = "active"
	zone, err = c.GetZone(t.Context(), cred, "example.test")
	if err != nil || !zone.Verified {
		t.Fatalf("%+v %v", zone, err)
	}
	zones, err := c.ListZones(t.Context(), cred)
	if err != nil {
		t.Fatal(err)
	}
	_ = zones
	f.zoneAccount = "acct_other"
	if _, err = c.GetZone(t.Context(), cred, "example.test"); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatalf("foreign account: %v", err)
	}
	if _, err = c.ListRecords(t.Context(), cred, provider.DNSZone{ID: "zone1", Name: "example.test", AccountID: "acct1"}); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatalf("foreign account zone: %v", err)
	}
}

func TestCloudflareCreateZoneAsksFirstAndTouchesNothingElse(t *testing.T) {
	f, c, cred, _ := newDNSFixture(t)
	existing, err := c.CreateZone(t.Context(), cred, provider.DNSZoneSpec{Name: "example.test"})
	if err != nil || existing.ID != "zone1" || f.writes != 0 {
		t.Fatalf("%+v %v writes=%d", existing, err, f.writes)
	}
	f.zoneExists = false
	if _, err = c.CreateZone(t.Context(), cred, provider.DNSZoneSpec{Name: "example.test", ReconcileOnly: true}); !errors.Is(err, provider.ErrInfrastructureAmbiguous) || f.writes != 0 {
		t.Fatalf("reconcile: %v writes=%d", err, f.writes)
	}
	created, err := c.CreateZone(t.Context(), cred, provider.DNSZoneSpec{Name: "example.test"})
	if err != nil || created.ID != "zone1" || !f.zoneCreated || f.writes != 1 {
		t.Fatalf("%+v %v", created, err)
	}
	account, _ := f.lastBody["account"].(map[string]any)
	if f.lastBody["name"] != "example.test" || f.lastBody["type"] != "full" || account["id"] != "acct1" || len(f.lastBody) != 3 {
		t.Fatalf("body %v", f.lastBody)
	}
}

func TestCloudflareDNSRefusesBadCredentialsAndCancellation(t *testing.T) {
	_, c, cred, zone := newDNSFixture(t)
	wrong := cred
	wrong.AccountID = "other"
	if _, err := c.ListRecords(t.Context(), wrong, zone); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatalf("foreign account: %v", err)
	}
	bad := cred
	bad.Provider = "vercel"
	if _, err := c.ListRecords(t.Context(), bad, zone); !errors.Is(err, provider.ErrInfrastructureInvalid) {
		t.Fatalf("wrong provider: %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.ListRecords(canceled, cred, zone); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestCloudflareDNSCapabilities(t *testing.T) {
	var adapter provider.DNSAdapter = cloudflare.New("", false)
	if o, ok := adapter.(provider.DNSRecordOwnership); !ok || !o.SupportsDNSRecordOwnership() {
		t.Fatal("ownership not declared")
	}
	want := []provider.InfrastructureCapability{provider.DNSRecordDeletion, provider.DNSRecordDiscovery, provider.DNSRecordUpsert, provider.DNSZoneCreation, provider.DNSZoneDiscovery}
	got := cloudflare.InfrastructureCapabilities()
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatal(got)
		}
	}
}
