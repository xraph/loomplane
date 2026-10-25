package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestPSCSourceCleanupKeepsOwnedIdentitiesAcrossRetry(t *testing.T) {
	for _, scenario := range []string{"async-failed-once", "retry-rejected-records", "retry-rejected-rule", "address-still-in-use", "pending", "lost-rule-pending", "checkpoint-after-rule", "delete", "lost-records", "lost-zone", "lost-rule", "lost-address", "denied", "save-refused", "revoked", "zone-replaced", "foreign-record", "rule-replaced", "address-in-use", "rule-replaced-at-intent", "wrong-operation", "missing-operation"} {
		t.Run(scenario, func(t *testing.T) {
			const network = "projects/app-project/global/networks/apps"
			const prefix = "projects/app-project/regions/us-central1"
			const rule = prefix + "/forwardingRules/database"
			const subnet = prefix + "/subnetworks/psc"
			scope := strings.Repeat("a", 64)
			dnsScope := strings.Repeat("b", 64)
			endpoint := SQLPSCProvisionedEndpoint{AddressID: "456", ForwardingRuleID: "789", Address: "10.20.0.8", DNSName: "source-db.us-central1.sql.goog.", ServiceAttachment: "projects/sql-tenant/regions/us-central1/serviceAttachments/source"}
			spec := SQLPSCProvisionSpec{Source: pscPITRSpec(), PhysicalID: "cloud-sql:" + strings.Repeat("c", 64), Subnetwork: subnet, Endpoint: provider.CloudSQLSourceEndpoint{ConsumerProject: "app-project", Network: network, ForwardingRule: rule}}
			creation := func(id string) PSCComputeState {
				return PSCComputeState{Attempted: true, RequestID: uuid.NewString(), Operation: "create-original", OperationID: "123", ResourceID: id, Complete: true}
			}
			owned := SQLPSCProvisionState{Version: 2, Scope: scope, SourceProject: "source-project", SourcePhysicalID: spec.PhysicalID, Subnet: SQLApplicationNetwork{Route: provider.CloudSQLApplicationNetwork{ConsumerProject: "app-project", Network: network, Subnetwork: subnet}, ID: "234", IPCIDR: "10.20.0.0/28"}, Address: creation("456"), ForwardingRule: creation("789"), DNS: SQLPSCSourceDNSState{Scope: dnsScope, ZoneRequest: uuid.NewString(), ZoneID: "678", RecordRequest: uuid.NewString(), ChangeID: "1", Complete: true}}
			addressPath := prefix + "/addresses/lp-psc-" + scope[:24]
			records := pscSourceRecords(endpoint.DNSName, endpoint.Address, owned.DNS.RecordRequest)
			var zone pscDNSZone
			zone.Name, zone.ID, zone.Kind, zone.DNSName, zone.Description, zone.Visibility = "lp-psc-"+dnsScope[:24], "678", "dns#managedZone", endpoint.DNSName, "loomplane-psc:"+dnsScope, "private"
			zone.Private.Networks = append(zone.Private.Networks, struct {
				URL string `json:"networkUrl"`
			}{URL: network})
			objects := map[string]map[string]any{
				rule:        {"kind": "compute#forwardingRule", "id": "789", "name": "database", "selfLink": rule, "region": prefix, "network": network, "target": endpoint.ServiceAttachment, "IPAddress": endpoint.Address, "pscConnectionStatus": "ACCEPTED", "description": "loomplane-psc:" + scope, "allowPscGlobalAccess": false},
				addressPath: {"kind": "compute#address", "id": "456", "name": "lp-psc-" + scope[:24], "selfLink": addressPath, "region": prefix, "description": "loomplane-psc:" + scope, "address": endpoint.Address, "addressType": "INTERNAL", "purpose": "GCE_ENDPOINT", "subnetwork": subnet, "status": "RESERVED"},
			}
			if scenario == "zone-replaced" {
				zone.ID = "999"
			}
			if scenario == "foreign-record" {
				records = append(records, pscDNSRecord{Name: "foreign." + endpoint.DNSName, Type: "A", TTL: 60, Data: []string{"10.0.0.9"}})
			}
			if scenario == "rule-replaced" {
				objects[rule]["id"] = "999"
			}
			if scenario == "address-in-use" {
				objects[addressPath]["status"] = "IN_USE"
				objects[addressPath]["users"] = []string{"projects/app-project/zones/us-central1-a/instances/foreign"}
			}
			if scenario == "address-still-in-use" {
				objects[addressPath]["status"] = "IN_USE"
				objects[addressPath]["users"] = []string{rule}
			}
			var state, saved SQLPSCCleanupState
			var change pscDNSChange
			var rejectedRequest string
			ops := map[string]pscComputeOperation{}
			writes := map[string]int{}
			zoneGone, revoked, lost, checkpointRefused := false, false, false, false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if scenario == "denied" {
					w.WriteHeader(403)
					return
				}
				if strings.HasPrefix(r.URL.Path, "/dns/") {
					if !strings.Contains(r.URL.Path, "/managedZones/678") {
						t.Error("DNS used mutable zone name", r.URL.Path)
					}
					if zoneGone {
						w.WriteHeader(404)
						return
					}
					switch {
					case strings.HasSuffix(r.URL.Path, "/rrsets"):
						_ = json.NewEncoder(w).Encode(map[string]any{"rrsets": records})
					case strings.HasSuffix(r.URL.Path, "/changes") && r.Method == http.MethodPost:
						if scenario == "retry-rejected-records" && !lost {
							lost = true
							w.WriteHeader(403)
							return
						}
						writes["records"]++
						if saved.Records.RequestID == "" || saved.Records.RequestID != r.URL.Query().Get("clientOperationId") {
							t.Error("record deletion before durable intent")
						}
						var got pscDNSChange
						if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
							t.Error(err)
						}
						if len(got.Additions) != 0 || !pscRecordsMatch(got.Deletions, records) {
							t.Error("deleted unowned records")
						}
						records = nil
						change = got
						change.ID = "2"
						change.Kind = "dns#change"
						change.Status = "done"
						if scenario == "lost-records" && !lost {
							lost = true
							w.WriteHeader(503)
							return
						}
						_ = json.NewEncoder(w).Encode(change)
					case strings.HasSuffix(r.URL.Path, "/changes"):
						_ = json.NewEncoder(w).Encode(map[string]any{"changes": []pscDNSChange{change}})
					case strings.Contains(r.URL.Path, "/changes/"):
						_ = json.NewEncoder(w).Encode(change)
					case r.Method == http.MethodDelete:
						writes["zone"]++
						if saved.ZoneRequest == "" || len(records) > 0 {
							t.Error("zone deletion before records and durable intent")
						}
						zoneGone = true
						if scenario == "lost-zone" && !lost {
							lost = true
							w.WriteHeader(503)
							return
						}
						_ = json.NewEncoder(w).Encode(map[string]any{})
					default:
						_ = json.NewEncoder(w).Encode(zone)
					}
					return
				}
				path := strings.TrimPrefix(r.URL.Path, "/compute/v1/")
				if strings.HasSuffix(path, "/operations") {
					items := []pscComputeOperation{}
					if scenario != "missing-operation" {
						for _, op := range ops {
							items = append(items, op)
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
					return
				}
				if op, ok := ops[path]; ok {
					if scenario == "pending" || scenario == "lost-rule-pending" {
						op.Status = "DONE"
						ops[path] = op
						delete(objects, op.Target)
					}
					_ = json.NewEncoder(w).Encode(op)
					return
				}
				obj := objects[path]
				if obj == nil {
					w.WriteHeader(404)
					return
				}
				if r.Method == http.MethodDelete {
					kind, kept := "rule", saved.ForwardingRule
					if path == addressPath {
						kind, kept = "address", saved.Address
					}
					if scenario == "retry-rejected-rule" && !lost {
						lost = true
						w.WriteHeader(403)
						return
					}
					writes[kind]++
					if !kept.Attempted || kept.RequestID != r.URL.Query().Get("requestId") || !zoneGone {
						t.Error("compute deletion before durable intent or DNS removal")
					}
					if kind == "address" && objects[rule] != nil {
						t.Error("address deletion preceded rule")
					}
					op := pscComputeOperation{Kind: "compute#operation", ID: "999", RequestID: kept.RequestID, Type: "delete", Target: path, TargetID: obj["id"].(string)}
					op.Name, op.Region, op.Status = "delete-"+kind, prefix, "DONE"
					if scenario == "async-failed-once" && kind == "rule" {
						if !lost {
							lost = true
							rejectedRequest = kept.RequestID
							_ = json.Unmarshal([]byte(`{"error":{"errors":[{"message":"resource still in use"}]}}`), &op)
							ops[prefix+"/operations/"+op.Name] = op
							_ = json.NewEncoder(w).Encode(op)
							return
						}
						if kept.RequestID == rejectedRequest {
							t.Error("reused failed request identity")
						}
						op.Name, op.ID = "delete-rule-retry", "1000"
					}
					if scenario == "wrong-operation" {
						op.TargetID = "foreign"
					}
					if scenario == "pending" || scenario == "lost-rule-pending" {
						op.Status = "RUNNING"
					}
					ops[prefix+"/operations/"+op.Name] = op
					if scenario != "missing-operation" && scenario != "pending" && scenario != "lost-rule-pending" {
						delete(objects, path)
					}
					if (scenario == "lost-"+kind || scenario == "lost-rule-pending" || scenario == "missing-operation") && !lost {
						lost = true
						w.WriteHeader(503)
						return
					}
					_ = json.NewEncoder(w).Encode(op)
					return
				}
				_ = json.NewEncoder(w).Encode(obj)
			}))
			defer srv.Close()
			s := &Session{c: New(srv.URL), key: &Key{ProjectID: "source-project"}, http: srv.Client()}
			save := func(next SQLPSCCleanupState) error {
				if scenario == "checkpoint-after-rule" && next.ForwardingRule.Operation != "" && !checkpointRefused {
					checkpointRefused = true
					return errors.New("checkpoint unavailable")
				}

				if scenario == "save-refused" && next.Records.RequestID != "" {
					return errors.New("storage unavailable")
				}
				if scenario == "revoked" && next.Records.RequestID != "" {
					revoked = true
				}
				if scenario == "rule-replaced-at-intent" && next.ForwardingRule.Attempted {
					objects[rule]["id"] = "999"
				}
				saved = next
				return nil
			}
			check := func(context.Context) error {
				if revoked {
					return errors.New("revoked")
				}
				return nil
			}
			run := func() error {
				return s.DeleteSQLPSCSourceEndpoint(t.Context(), spec, owned, endpoint, &state, save, check)
			}
			err := run()
			if scenario == "async-failed-once" || strings.HasPrefix(scenario, "retry-rejected-") || strings.HasPrefix(scenario, "lost-") || scenario == "missing-operation" || scenario == "checkpoint-after-rule" {
				if err == nil {
					t.Fatal("lost response did not stop")
				}
				state = saved
				err = run()
			}
			switch scenario {
			case "denied", "save-refused", "revoked", "zone-replaced", "foreign-record", "rule-replaced", "address-in-use":
				if err == nil || len(writes) != 0 {
					t.Fatal("unsafe cleanup", err, writes)
				}
			case "rule-replaced-at-intent":
				if err == nil || writes["rule"] != 0 || writes["address"] != 0 {
					t.Fatal("deleted replaced rule", err, writes)
				}
			case "address-still-in-use", "wrong-operation", "missing-operation":
				if err == nil || writes["rule"] != 1 || writes["address"] != 0 {
					t.Fatal("unverified operation advanced", err, writes)
				}
			default:
				if err != nil || !saved.Address.Complete || !saved.ForwardingRule.Complete || !saved.ZoneComplete || !saved.Records.Complete {
					t.Fatal("cleanup incomplete", err, saved)
				}
				for _, kind := range []string{"records", "zone", "rule", "address"} {
					want := 1
					if scenario == "async-failed-once" && kind == "rule" {
						want = 2
					}
					if writes[kind] != want {
						t.Fatal("mutation repeated or missing", writes)
					}
				}
				if err = run(); err != nil {
					t.Fatal("completed cleanup did not resume", err)
				}
				for kind, count := range writes {
					want := 1
					if scenario == "async-failed-once" && kind == "rule" {
						want = 2
					}
					if count != want {
						t.Fatal("completion replayed deletion", writes)
					}
				}
			}
		})
	}
}
