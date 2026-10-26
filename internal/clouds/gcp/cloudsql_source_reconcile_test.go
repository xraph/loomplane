package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPSCSourceReconciliationDoesNotProvisionMissingResources(t *testing.T) {
	cases := []struct{ stop, scenario string }{
		{"none", "normal"}, {"address", "normal"}, {"rule", "normal"}, {"zone", "normal"}, {"records", "normal"},
		{"rule", "pending"}, {"zone", "closed"}, {"rule", "unknown-status"},
		{"address", "missing-operation"}, {"address", "ambiguous-operation"}, {"address", "replaced-address"},
		{"address", "source-changed"}, {"address", "save-refused"}, {"address", "guard-refused"},
		{"zone", "missing-zone-operation"}, {"zone", "foreign-record"},
		{"records", "missing-change"}, {"records", "ambiguous-change"}, {"records", "zone-replaced"},
		{"none", "invalid-address-state"}, {"address", "out-of-order-rule"}, {"rule", "out-of-order-dns"},
	}
	for _, tc := range cases {
		t.Run(tc.stop+"/"+tc.scenario, func(t *testing.T) {
			stop, scenario := tc.stop, tc.scenario
			in, fields := pscPITRInstance(t)
			physical, identityErr := in.Incarnation("source-project", "us-central1", "source-db")
			if identityErr != nil {
				t.Fatal(identityErr)
			}
			const prefix = "projects/app-project/regions/us-central1"
			const network = "projects/app-project/global/networks/apps"
			const subnet = prefix + "/subnetworks/psc"
			const rule = prefix + "/forwardingRules/database"
			const attachment = "projects/sql-tenant/regions/us-central1/serviceAttachments/source"
			fields["pscServiceAttachmentLink"], fields["dnsName"] = attachment, "source-db.us-central1.sql.goog."
			fields["settings"].(map[string]any)["ipConfiguration"].(map[string]any)["pscConfig"].(map[string]any)["allowedConsumerProjects"] = []string{"app-project"}
			var state, saved SQLPSCProvisionState
			objects := map[string]map[string]any{}
			operations := map[string]map[string]any{}
			writes := 0
			dnsScenario := "create"
			if stop == "zone" {
				dnsScenario = "lost-zone"
			}
			if stop == "records" {
				dnsScenario = "lost-change"
			}
			dns := &pscDNSFixture{t: t, scenario: dnsScenario, network: network, endpoint: SQLPSCProvisionedEndpoint{DNSName: "source-db.us-central1.sql.goog."}, saved: func() SQLPSCSourceDNSState { return saved.DNS }}
			lost := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != http.MethodGet {
					writes++
				}
				if strings.HasPrefix(r.URL.Path, "/dns/") {
					dns.ServeHTTP(w, r)
					return
				}
				path := strings.TrimPrefix(r.URL.Path, "/compute/v1/")
				switch {
				case r.URL.Path == "/v1/projects/source-project/instances/source-db":
					_ = json.NewEncoder(w).Encode(fields)
				case path == subnet:
					_ = json.NewEncoder(w).Encode(map[string]any{"kind": "compute#subnetwork", "id": "123", "name": "psc", "selfLink": subnet, "region": prefix, "network": network, "ipCidrRange": "10.20.0.0/28", "purpose": "PRIVATE", "stackType": "IPV4_ONLY", "state": "READY"})
				case strings.HasSuffix(path, "/operations"):
					var items []map[string]any
					for _, op := range operations {
						items = append(items, op)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
				case operations[path] != nil:
					_ = json.NewEncoder(w).Encode(operations[path])
				case r.Method == http.MethodPost:
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					name := body["name"].(string)
					kind, id, resourceKind, kept := "address", "456", "compute#address", saved.Address
					if strings.HasSuffix(path, "/forwardingRules") {
						kind, id, resourceKind, kept = "rule", "789", "compute#forwardingRule", saved.ForwardingRule
					}
					if !kept.Attempted || kept.RequestID != r.URL.Query().Get("requestId") {
						t.Error("create before durable intent")
					}
					resource := path + "/" + name
					body["id"], body["kind"], body["selfLink"], body["region"] = id, resourceKind, resource, prefix
					if kind == "address" {
						body["address"], body["status"] = "10.20.0.8", "RESERVED"
					} else {
						body["IPAddress"], body["pscConnectionStatus"] = "10.20.0.8", "ACCEPTED"
					}
					objects[resource] = body
					op := map[string]any{"kind": "compute#operation", "id": id, "name": "create-" + kind, "status": "DONE", "operationType": "insert", "targetLink": resource, "targetId": id, "clientOperationId": kept.RequestID, "region": prefix}
					operations[prefix+"/operations/create-"+kind] = op
					if stop == kind && !lost {
						lost = true
						w.WriteHeader(503)
						return
					}
					_ = json.NewEncoder(w).Encode(op)
				case objects[path] != nil:
					_ = json.NewEncoder(w).Encode(objects[path])
				default:
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			session := &Session{c: New(srv.URL), key: &Key{ProjectID: "source-project"}, http: srv.Client()}
			spec := SQLPSCProvisionSpec{Source: pscPITRSpec(), PhysicalID: physical, Subnetwork: subnet}
			spec.Endpoint.ConsumerProject, spec.Endpoint.Network, spec.Endpoint.ForwardingRule = "app-project", network, rule
			reconciling := false
			refused := errors.New("checkpoint or lease refused")
			save := func(next SQLPSCProvisionState) error {
				if reconciling && scenario == "save-refused" {
					return refused
				}
				saved = next
				return nil
			}
			guard := func(context.Context) error {
				if reconciling && scenario == "guard-refused" {
					return refused
				}
				return nil
			}
			if stop != "none" {
				if _, err := session.EnsureSQLPSCSourceEndpoint(t.Context(), spec, &state, save, guard); err == nil {
					t.Fatal("interrupted create did not stop")
				}
			}
			before := writes
			state = saved
			switch scenario {
			case "pending":
				objects[rule]["pscConnectionStatus"] = "PENDING"
			case "closed":
				objects[rule]["pscConnectionStatus"] = "CLOSED"
			case "unknown-status":
				objects[rule]["pscConnectionStatus"] = "UNKNOWN"
			case "missing-operation":
				clear(operations)
			case "ambiguous-operation":
				operations[prefix+"/operations/duplicate"] = operations[prefix+"/operations/create-address"]
			case "replaced-address":
				for _, object := range objects {
					object["id"] = "999"
				}
			case "source-changed":
				fields["dnsName"] = "replacement.us-central1.sql.goog."
			case "missing-zone-operation":
				dns.scenario = "missing-operation"
			case "foreign-record":
				dns.records = []map[string]any{{"name": dns.endpoint.DNSName, "type": "A", "ttl": 60, "rrdatas": []string{"10.20.0.9"}}}
			case "missing-change", "ambiguous-change":
				dns.scenario = scenario
			case "zone-replaced":
				dns.zone["id"] = "999"
			case "invalid-address-state":
				state.Address.ResourceID = "456"
			case "out-of-order-rule":
				state.ForwardingRule.Attempted = true
			case "out-of-order-dns":
				state.DNS.ZoneID = "321"
			}
			reconciling = true
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			endpoint, err := session.ReconcileSQLPSCSourceEndpoint(ctx, spec, &state, save, guard)
			if writes != before {
				t.Fatalf("reconciliation created missing resources: before=%d after=%d", before, writes)
			}
			wantError := scenario != "normal" && scenario != "pending" && scenario != "closed"
			if wantError {
				if err == nil || endpoint != nil {
					t.Fatal("unsafe reconciliation accepted", endpoint, err)
				}
				if (scenario == "save-refused" || scenario == "guard-refused") && (!errors.Is(err, refused) || state != saved) {
					t.Fatal("refused persistence or guard lost", err, state, saved)
				}
				return
			}
			if err != nil || endpoint == nil {
				t.Fatal("reconcile", endpoint, err)
			}
			wantAddress := stop != "none"
			wantRule := stop == "rule" || stop == "zone" || stop == "records"
			wantZone := stop == "zone" || stop == "records"
			if state.Address.Complete != wantAddress || state.ForwardingRule.Complete != wantRule || (state.DNS.ZoneID != "") != wantZone || state.DNS.Complete != (stop == "records") || (state.DNS.RecordRequest != "") != (stop == "records") {
				t.Fatalf("incorrect recovered ownership: %+v", state)
			}
			if !wantAddress && endpoint.AddressID != "" || !wantRule && endpoint.ForwardingRuleID != "" {
				t.Fatal("claimed uncreated endpoint", endpoint)
			}
			if wantAddress && endpoint.AddressID != "456" || wantRule && endpoint.ForwardingRuleID != "789" || wantZone && state.DNS.ZoneID != "321" {
				t.Fatal("lost original resource identity", endpoint, state)
			}
			kept := saved
			state = saved
			again, err := session.ReconcileSQLPSCSourceEndpoint(ctx, spec, &state, save, guard)
			if err != nil || again == nil || *again != *endpoint || state != kept || writes != before {
				t.Fatal("repeated reconciliation changed ownership", again, err, state, kept)
			}
		})
	}
}
