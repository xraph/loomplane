package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPSCSourceDNSOwnsZoneAndAtomicRecordsAcrossRetry(t *testing.T) {
	for _, scenario := range []string{"paginated", "lost-change-paginated", "record-changed-at-complete", "create", "lost-zone", "lost-change", "pending", "foreign-zone", "foreign-record", "zone-replaced", "zone-public", "zone-other-network", "record-changed", "missing-operation", "wrong-operation", "missing-change", "ambiguous-change", "save-refused", "revoked-after-zone-intent", "revoked-after-record-intent", "zone-replaced-before-record"} {
		t.Run(scenario, func(t *testing.T) {
			var state, saved SQLPSCSourceDNSState
			revoked := false
			const network = "projects/app-project/global/networks/apps"
			endpoint := SQLPSCProvisionedEndpoint{AddressID: "456", ForwardingRuleID: "789", Address: "10.20.0.8", DNSName: "source-db.us-central1.sql.goog."}
			f := &pscDNSFixture{t: t, scenario: scenario, network: network, endpoint: endpoint, saved: func() SQLPSCSourceDNSState { return saved }}
			srv := httptest.NewServer(f)
			defer srv.Close()
			s := &Session{c: New(srv.URL), key: &Key{ProjectID: "source-project"}, http: srv.Client()}
			guard := func(context.Context) error {
				if revoked {
					return errors.New("revoked")
				}
				return nil
			}
			save := func(next SQLPSCSourceDNSState) error {
				if scenario == "save-refused" && next.ZoneRequest != "" {
					return errors.New("checkpoint refused")
				}
				if scenario == "revoked-after-zone-intent" && next.ZoneRequest != "" {
					revoked = true
				}
				if scenario == "revoked-after-record-intent" && next.RecordRequest != "" {
					revoked = true
				}
				if scenario == "zone-replaced-before-record" && next.RecordRequest != "" {
					f.zone["id"] = "999"
				}
				if scenario == "record-changed-at-complete" && next.Complete {
					f.records[0]["rrdatas"] = []string{"10.20.0.9"}
				}
				saved = next
				return nil
			}
			run := func() error {
				return s.ensurePSCSourceDNS(t.Context(), "network-scope", "app-project", network, endpoint, &state, save, guard)
			}
			err := run()
			if strings.HasPrefix(scenario, "lost-") || scenario == "missing-operation" || scenario == "wrong-operation" || scenario == "missing-change" || scenario == "ambiguous-change" {
				if err == nil {
					t.Fatal("lost response did not stop")
				}
				state = saved
				err = run()
			}
			switch scenario {
			case "foreign-zone", "save-refused", "revoked-after-zone-intent":
				if err == nil || f.zoneWrites != 0 || f.recordWrites != 0 {
					t.Fatal("unauthorized zone write", err, f.zoneWrites, f.recordWrites)
				}
				return
			case "foreign-record", "missing-operation", "wrong-operation", "revoked-after-record-intent", "zone-replaced-before-record":
				if err == nil || f.zoneWrites != 1 || f.recordWrites != 0 {
					t.Fatal("unauthorized record write", err, f.zoneWrites, f.recordWrites)
				}
				return
			case "missing-change", "ambiguous-change", "record-changed-at-complete":
				if err == nil || f.zoneWrites != 1 || f.recordWrites != 1 {
					t.Fatal("uncertain change replayed", err, f.zoneWrites, f.recordWrites)
				}
				return
			}
			if err != nil || !saved.Complete || saved.ZoneID != "321" || saved.ChangeID != "7" || f.zoneWrites != 1 || f.recordWrites != 1 {
				t.Fatal("DNS not provisioned", err, saved, f.zoneWrites, f.recordWrites)
			}
			switch scenario {
			case "zone-replaced":
				f.zone["id"] = "999"
			case "zone-public":
				f.zone["visibility"] = "public"
			case "zone-other-network":
				f.zone["privateVisibilityConfig"] = map[string]any{"networks": []map[string]any{{"networkUrl": "projects/app-project/global/networks/foreign"}}}
			case "record-changed":
				f.records[0]["rrdatas"] = []string{"10.20.0.9"}
			}
			state = saved
			err = run()
			wantError := scenario == "zone-replaced" || scenario == "zone-public" || scenario == "zone-other-network" || scenario == "record-changed"
			if wantError != (err != nil) || f.zoneWrites != 1 || f.recordWrites != 1 {
				t.Fatal("incorrect DNS resume", err, f.zoneWrites, f.recordWrites)
			}
		})
	}
}

type pscDNSFixture struct {
	t                        *testing.T
	scenario, network        string
	endpoint                 SQLPSCProvisionedEndpoint
	saved                    func() SQLPSCSourceDNSState
	zone, change             map[string]any
	records                  []map[string]any
	zoneRequest              string
	zoneWrites, recordWrites int
	lost                     bool
}

func (f *pscDNSFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/managedZones"):
		f.zoneWrites++
		if f.saved().ZoneRequest == "" || f.saved().ZoneRequest != r.URL.Query().Get("clientOperationId") {
			f.t.Error("zone write preceded durable intent")
		}
		if err := json.NewDecoder(r.Body).Decode(&f.zone); err != nil {
			f.t.Error(err)
		}
		f.zone["id"], f.zone["kind"] = "321", "dns#managedZone"
		if f.zone["visibility"] != "private" || f.zone["dnsName"] != f.endpoint.DNSName {
			f.t.Error("incorrect zone scope", f.zone)
		}
		visibility := f.zone["privateVisibilityConfig"].(map[string]any)
		networks := visibility["networks"].([]any)
		if len(networks) != 1 || computeResource(networks[0].(map[string]any)["networkUrl"].(string)) != f.network {
			f.t.Error("zone escaped reviewed VPC")
		}
		f.zoneRequest = f.saved().ZoneRequest
		if f.scenario == "foreign-record" {
			f.records = []map[string]any{{"name": f.endpoint.DNSName, "type": "A", "ttl": 60, "rrdatas": []string{"10.20.0.9"}}}
		}
		if (f.scenario == "lost-zone" || f.scenario == "missing-operation" || f.scenario == "wrong-operation") && !f.lost {
			f.lost = true
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(f.zone)
	case strings.Contains(path, "/operations/"):
		if f.scenario == "missing-operation" {
			w.WriteHeader(404)
			return
		}
		id := f.zoneRequest
		if f.scenario == "wrong-operation" {
			id = "foreign-operation"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"kind": "dns#operation", "id": id, "type": "insert", "status": "done", "zoneContext": map[string]any{"newValue": f.zone}})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/changes"):
		f.recordWrites++
		if f.saved().RecordRequest == "" || f.saved().RecordRequest != r.URL.Query().Get("clientOperationId") || !strings.Contains(path, "/managedZones/321/") {
			f.t.Error("record write lacks durable zone identity")
		}
		if err := json.NewDecoder(r.Body).Decode(&f.change); err != nil {
			f.t.Error(err)
		}
		f.change["id"], f.change["kind"], f.change["status"] = "7", "dns#change", "done"
		if f.scenario == "pending" {
			f.change["status"] = "pending"
		}
		additions := f.change["additions"].([]any)
		if len(additions) != 2 {
			f.t.Error("records were not atomic")
		}
		for _, addition := range additions {
			f.records = append(f.records, addition.(map[string]any))
		}
		if (f.scenario == "lost-change" || f.scenario == "lost-change-paginated" || f.scenario == "missing-change" || f.scenario == "ambiguous-change") && !f.lost {
			f.lost = true
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(f.change)
	case strings.HasSuffix(path, "/changes"):
		if f.scenario == "lost-change-paginated" && r.URL.Query().Get("pageToken") == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"nextPageToken": "next"})
			return
		}
		changes := []map[string]any{f.change}
		if f.scenario == "missing-change" {
			changes = nil
		}
		if f.scenario == "ambiguous-change" {
			changes = append(changes, f.change)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"changes": changes})
	case strings.Contains(path, "/changes/"):
		if f.scenario == "pending" {
			f.change["status"] = "done"
		}
		_ = json.NewEncoder(w).Encode(f.change)
	case strings.HasSuffix(path, "/rrsets"):
		if f.scenario == "paginated" && len(f.records) == 2 {
			if r.URL.Query().Get("pageToken") == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{"rrsets": f.records[:1], "nextPageToken": "next"})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"rrsets": f.records[1:]})
			}
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rrsets": f.records})
	default:
		if f.scenario == "foreign-zone" {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "999"})
			return
		}
		if f.zone == nil {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(f.zone)
	}
}
