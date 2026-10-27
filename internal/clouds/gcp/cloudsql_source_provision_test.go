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

func TestPSCSourceForwardingRulePersistsBeforeMutationAndResumes(t *testing.T) {
	for _, scenario := range []string{"legacy-checkpoint", "dns-check-budget", "dns-lease-revoked-during-read", "dns-create", "dns-lost-zone", "dns-lost-change", "dns-source-replaced", "dns-endpoint-replaced", "create", "lost-address-response", "lost-rule-response", "checkpoint-refused", "revoked-after-intent", "outside-private-subnet", "lost-address-missing-operation", "lost-address-duplicate-operation", "checkpoint-after-operation", "pending", "foreign-address", "source-replaced", "subnet-replaced", "address-replaced-before-rule", "operation-replaced", "endpoint-replaced", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			in, fields := pscPITRInstance(t)
			physical, err := in.Incarnation("source-project", "us-central1", "source-db")
			if err != nil {
				t.Fatal(err)
			}
			const route = "projects/app-project/global/networks/apps"
			const subnet = "projects/app-project/regions/us-central1/subnetworks/psc"
			const rulePath = "projects/app-project/regions/us-central1/forwardingRules/database"
			const attachment = "projects/sql-tenant/regions/us-central1/serviceAttachments/source"
			fields["pscServiceAttachmentLink"], fields["dnsName"] = attachment, "source-db.us-central1.sql.goog."
			fields["settings"].(map[string]any)["ipConfiguration"].(map[string]any)["pscConfig"].(map[string]any)["allowedConsumerProjects"] = []string{"app-project"}
			state := SQLPSCProvisionState{}
			var saved SQLPSCProvisionState
			writes := map[string]int{}
			objects := map[string]map[string]any{}
			operations := map[string]map[string]any{}
			broken := false
			revoked, checkpointRejected := false, false
			checks := 0
			subnetID := "123"
			dnsFixture := &pscDNSFixture{t: t, scenario: strings.TrimPrefix(scenario, "dns-"), network: route, endpoint: SQLPSCProvisionedEndpoint{DNSName: "source-db.us-central1.sql.goog."}, saved: func() SQLPSCSourceDNSState { return saved.DNS }}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasPrefix(r.URL.Path, "/dns/") {
					dnsFixture.ServeHTTP(w, r)
					return
				}
				path := strings.TrimPrefix(r.URL.Path, "/compute/v1/")
				if r.URL.Path == "/v1/projects/source-project/instances/source-db" {
					if scenario == "dns-lease-revoked-during-read" && saved.DNS.ZoneRequest != "" {
						revoked = true
					}
					_ = json.NewEncoder(w).Encode(fields)
					return
				}
				if path == subnet {
					cidr := "10.20.0.0/28"
					if scenario == "outside-private-subnet" {
						cidr = "192.168.0.0/15"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"kind": "compute#subnetwork", "id": subnetID, "name": "psc", "selfLink": subnet, "region": "projects/app-project/regions/us-central1", "network": route, "ipCidrRange": cidr, "purpose": "PRIVATE", "stackType": "IPV4_ONLY", "state": "READY"})
					return
				}
				if strings.HasSuffix(path, "/operations") {
					var items []map[string]any
					for _, op := range operations {
						items = append(items, op)
					}
					if scenario == "lost-address-missing-operation" {
						items = nil
					}
					if scenario == "lost-address-duplicate-operation" {
						items = append(items, items[0])
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
					return
				}
				if op := operations[path]; op != nil {
					if scenario == "pending" {
						op["status"] = "DONE"
					}
					_ = json.NewEncoder(w).Encode(op)
					return
				}
				kind := "addresses"
				if strings.Contains(path, "/forwardingRules") {
					kind = "forwardingRules"
				}
				if r.Method == http.MethodPost {
					writes[kind]++
					kept := saved.Address
					if kind == "forwardingRules" {
						kept = saved.ForwardingRule
					}
					if !kept.Attempted || kept.RequestID == "" || kept.RequestID != r.URL.Query().Get("requestId") {
						t.Error("write preceded durable request identity")
					}
					var body map[string]any
					if decodeErr := json.NewDecoder(r.Body).Decode(&body); decodeErr != nil {
						t.Error(decodeErr)
					}
					name := body["name"].(string)
					resourcePath := path + "/" + name
					id, resourceKind := "456", "compute#address"
					if kind == "forwardingRules" {
						id, resourceKind = "789", "compute#forwardingRule"
					}
					body["id"], body["kind"], body["selfLink"], body["region"] = id, resourceKind, resourcePath, "projects/app-project/regions/us-central1"
					if kind == "addresses" {
						if body["addressType"] != "INTERNAL" || body["subnetwork"] != subnet {
							t.Error("address escaped reviewed subnet")
						}
						body["address"], body["status"] = "10.20.0.8", "RESERVED"
					} else {
						addressRef, _ := body["IPAddress"].(string)
						if body["target"] != attachment || body["network"] != route || !strings.HasPrefix(addressRef, "projects/app-project/regions/us-central1/addresses/") || objects[addressRef] == nil || body["allowPscGlobalAccess"] != false {
							t.Error("forwarding rule escaped reviewed route")
						}
						body["IPAddress"] = "10.20.0.8"
						body["pscConnectionStatus"] = "ACCEPTED"
					}
					objects[resourcePath] = body
					op := map[string]any{"kind": "compute#operation", "id": id, "name": "create-" + kind, "status": "DONE", "operationType": "insert", "targetLink": resourcePath, "targetId": id, "clientOperationId": kept.RequestID, "region": "projects/app-project/regions/us-central1"}
					if scenario == "pending" {
						op["status"] = "RUNNING"
					}
					operations["projects/app-project/regions/us-central1/operations/create-"+kind] = op
					if scenario == "source-replaced" {
						fields["createTime"] = "2026-10-25T00:00:00Z"
					}
					if scenario == "subnet-replaced" {
						subnetID = "999"
					}
					if !broken && (strings.HasPrefix(scenario, "lost-address-") && kind == "addresses" || scenario == "lost-rule-response" && kind == "forwardingRules") {
						broken = true
						w.WriteHeader(503)
						return
					}
					_ = json.NewEncoder(w).Encode(op)
					return
				}
				if scenario == "denied" {
					w.WriteHeader(403)
					return
				}
				if scenario == "foreign-address" && kind == "addresses" {
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "999"})
					return
				}
				if obj := objects[path]; obj != nil {
					_ = json.NewEncoder(w).Encode(obj)
					return
				}
				w.WriteHeader(404)
			}))
			defer srv.Close()
			session := &Session{c: New(srv.URL), key: &Key{ProjectID: "source-project"}, http: srv.Client()}
			spec := SQLPSCProvisionSpec{Source: pscPITRSpec(), PhysicalID: physical, Subnetwork: subnet}
			spec.Endpoint.ConsumerProject, spec.Endpoint.Network, spec.Endpoint.ForwardingRule = "app-project", route, rulePath
			save := func(next SQLPSCProvisionState) error {
				if scenario == "checkpoint-refused" && next.Address.Attempted {
					return errors.New("lease revoked")
				}
				if scenario == "revoked-after-intent" && next.Address.Attempted {
					revoked = true
				}
				if scenario == "checkpoint-after-operation" && next.Address.Operation != "" && !checkpointRejected {
					checkpointRejected = true
					return errors.New("checkpoint unavailable")
				}
				if scenario == "address-replaced-before-rule" && next.ForwardingRule.Attempted {
					for path, object := range objects {
						if strings.Contains(path, "/addresses/") {
							object["id"] = "999"
						}
					}
				}
				if scenario == "dns-source-replaced" && next.DNS.ZoneRequest != "" {
					fields["createTime"] = "2026-10-25T00:00:00Z"
				}
				if scenario == "dns-endpoint-replaced" && next.DNS.RecordRequest != "" {
					objects[rulePath]["id"] = "999"
				}
				saved = next
				return nil
			}
			guard := func(context.Context) error {
				checks++
				if scenario == "dns-check-budget" && checks > 250 {
					return errors.New("nested ownership reads exhausted the durable lease-check budget")
				}
				if revoked {
					return errors.New("authority revoked")
				}
				return nil
			}
			ensure := session.EnsureSQLPSCSourceForwardingRule
			if strings.HasPrefix(scenario, "dns-") {
				ensure = session.EnsureSQLPSCSourceEndpoint
			}
			got, err := ensure(t.Context(), spec, &state, save, guard)
			if strings.HasPrefix(scenario, "lost-") || strings.HasPrefix(scenario, "dns-lost-") || scenario == "checkpoint-after-operation" {
				if err == nil {
					t.Fatal("lost response did not stop provision")
				}
				state = saved
				got, err = ensure(t.Context(), spec, &state, save, guard)
			}
			if scenario == "dns-lease-revoked-during-read" {
				if err == nil || !revoked || dnsFixture.zoneWrites != 0 || dnsFixture.recordWrites != 0 {
					t.Fatal("DNS mutated after lease revocation during ownership reads", err, revoked, dnsFixture.zoneWrites, dnsFixture.recordWrites)
				}
				return
			}
			if scenario == "dns-source-replaced" || scenario == "dns-endpoint-replaced" {
				if err == nil || dnsFixture.recordWrites != 0 || scenario == "dns-source-replaced" && dnsFixture.zoneWrites != 0 {
					t.Fatal("DNS consumed replaced source or endpoint", err, dnsFixture.zoneWrites, dnsFixture.recordWrites)
				}
				return
			}
			if strings.HasPrefix(scenario, "dns-") && (!saved.DNS.Complete || dnsFixture.zoneWrites != 1 || dnsFixture.recordWrites != 1) {
				t.Fatal("combined endpoint omitted DNS", err, saved.DNS)
			}
			if scenario == "lost-address-missing-operation" || scenario == "lost-address-duplicate-operation" {
				if err == nil || writes["addresses"] != 1 || writes["forwardingRules"] != 0 {
					t.Fatal("uncertain create was replayed or adopted", err, writes)
				}
				return
			}
			if scenario == "address-replaced-before-rule" {
				if err == nil || writes["addresses"] != 1 || writes["forwardingRules"] != 0 {
					t.Fatal("forwarding rule consumed a replacement reservation", err, writes)
				}
				return
			}
			if scenario == "revoked-after-intent" || scenario == "outside-private-subnet" || scenario == "checkpoint-refused" || scenario == "foreign-address" || scenario == "denied" || scenario == "source-replaced" || scenario == "subnet-replaced" {
				if err == nil || writes["forwardingRules"] != 0 {
					t.Fatal("unsafe provision accepted", err, writes)
				}
				if scenario != "source-replaced" && scenario != "subnet-replaced" && writes["addresses"] != 0 {
					t.Fatal("unauthorized address mutation")
				}
				return
			}
			if err != nil || got == nil || got.Address != "10.20.0.8" || got.ForwardingRuleID != "789" || got.ServiceAttachment != attachment || writes["addresses"] != 1 || writes["forwardingRules"] != 1 {
				t.Fatal("owned endpoint not provisioned", got, err, writes)
			}
			if scenario == "dns-check-budget" {
				t.Logf("creation lease checks: %d", checks)
				checks = 0
			}
			state = saved
			if scenario == "legacy-checkpoint" {
				state.Version = 1
				state.Source = SQLPSCSourceWitness{}
				state.AddressValue = ""
				state.SourceProject = ""
				state.SourcePhysicalID = ""
			}
			if scenario == "endpoint-replaced" {
				objects[rulePath]["id"] = "999"
			}
			if scenario == "operation-replaced" {
				state.Address.Complete = false
				operations["projects/app-project/regions/us-central1/operations/create-addresses"]["id"] = "999"
			}
			_, err = ensure(t.Context(), spec, &state, save, guard)
			if (scenario == "endpoint-replaced" || scenario == "operation-replaced") != (err != nil) {
				t.Fatal("incorrect restart identity check", err)
			}
			if scenario == "dns-check-budget" {
				t.Logf("replay lease checks: %d", checks)
			}
			if writes["addresses"] != 1 || writes["forwardingRules"] != 1 {
				t.Fatal("replayed network mutation", writes)
			}
		})
	}
}
