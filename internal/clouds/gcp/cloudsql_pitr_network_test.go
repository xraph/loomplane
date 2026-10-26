package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/manageddb"
)

func recoveryPSCNetwork() manageddb.CloudSQLRecoveryNetwork {
	return manageddb.CloudSQLRecoveryNetwork{ConsumerProject: "worker-project", Network: "projects/worker-project/global/networks/recovery"}
}

func pitrNetworkCopy(t *testing.T, prepared bool) (*SQLInstance, map[string]any, SQLInstanceSpec) {
	t.Helper()
	_, fields := pscPITRInstance(t)
	fields["name"], fields["connectionName"] = "recovery-copy", "source-project:us-central1:recovery-copy"
	spec := pscPITRSpec()
	spec.Name, spec.Intent = "recovery-copy", "copy-intent"
	spec.Inspect = func(*SQLInstance) error { return nil }
	if prepared {
		settings := fields["settings"].(map[string]any)
		settings["userLabels"] = map[string]string{"managed-by": "loomplane", SQLIntentLabel: spec.Intent}
		settings["backupConfiguration"] = map[string]any{"enabled": false, "pointInTimeRecoveryEnabled": false}
		settings["deletionProtectionEnabled"] = false
		settings["ipConfiguration"].(map[string]any)["pscConfig"] = map[string]any{
			"pscEnabled": true, "allowedConsumerProjects": []string{"worker-project"}, "pscAutoDnsEnabled": true,
			"pscAutoConnections": []any{map[string]any{"consumerProject": "worker-project", "consumerNetwork": recoveryPSCNetwork().Network, "status": "ACTIVE", "consumerNetworkStatus": "VALID", "instanceAutoDnsStatus": "AUTO_DNS_OK", "ipAddress": "10.10.0.5"}},
		}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var in SQLInstance
	if err = json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	return &in, fields, spec
}

func TestSQLPITRCopyWaitsForOnlyReviewedPSCEndpointAndDNS(t *testing.T) {
	for _, scenario := range []string{"ready", "endpoint-pending", "dns-pending", "maintenance", "public", "extra-consumer", "extra-endpoint", "wrong-network", "wrong-project", "outbound", "auto-policy", "dns-disabled", "dns-failed", "endpoint-rejected", "public-endpoint", "backup-enabled", "logs-enabled", "missing-label", "reused-intent", "disk-growth", "protection-enabled", "unsafe-state", "invalid-route"} {
		t.Run(scenario, func(t *testing.T) {
			_, fields, spec := pitrNetworkCopy(t, true)
			route := recoveryPSCNetwork()
			settings := fields["settings"].(map[string]any)
			network := settings["ipConfiguration"].(map[string]any)
			psc := network["pscConfig"].(map[string]any)
			endpoint := psc["pscAutoConnections"].([]any)[0].(map[string]any)
			switch scenario {
			case "endpoint-pending":
				endpoint["status"] = "PENDING"
			case "dns-pending":
				endpoint["instanceAutoDnsStatus"] = "AUTO_DNS_STATUS_UNSPECIFIED"
			case "maintenance":
				fields["state"] = "MAINTENANCE"
			case "public":
				network["ipv4Enabled"] = true
			case "extra-consumer":
				psc["allowedConsumerProjects"] = []string{"worker-project", "app-project"}
			case "extra-endpoint":
				psc["pscAutoConnections"] = []any{endpoint, endpoint}
			case "wrong-network":
				endpoint["consumerNetwork"] = "projects/worker-project/global/networks/production"
			case "wrong-project":
				endpoint["consumerProject"] = "app-project"
			case "outbound":
				psc["networkAttachmentUri"] = "projects/worker-project/regions/us-central1/networkAttachments/egress"
			case "auto-policy":
				psc["pscAutoConnectionPolicyEnabled"] = true
			case "dns-disabled":
				psc["pscAutoDnsEnabled"] = false
			case "dns-failed":
				endpoint["instanceAutoDnsStatus"] = "AUTO_DNS_FAILED"
			case "endpoint-rejected":
				endpoint["status"] = "REJECTED"
			case "public-endpoint":
				endpoint["ipAddress"] = "198.51.100.10"
			case "backup-enabled":
				settings["backupConfiguration"].(map[string]any)["enabled"] = true
			case "logs-enabled":
				settings["backupConfiguration"].(map[string]any)["pointInTimeRecoveryEnabled"] = true
			case "missing-label":
				delete(settings, "userLabels")
			case "reused-intent":
				settings["userLabels"].(map[string]string)[SQLIntentLabel] = "other-copy"
			case "disk-growth":
				settings["dataDiskSizeGb"] = "1000"
			case "protection-enabled":
				settings["deletionProtectionEnabled"] = true
			case "unsafe-state":
				fields["state"] = "FAILED"
			case "invalid-route":
				route.Network = "https://example.test/network"
			}
			raw, _ := json.Marshal(fields)
			var in SQLInstance
			if err := json.Unmarshal(raw, &in); err != nil {
				t.Fatal(err)
			}
			ready, err := in.CheckPITRCopyNetwork(spec, route)
			switch scenario {
			case "ready":
				if err != nil || !ready {
					t.Fatalf("ready=%v err=%v", ready, err)
				}
			case "endpoint-pending", "dns-pending", "maintenance":
				if err != nil || ready {
					t.Fatalf("pending endpoint admitted or rejected: %v %v", ready, err)
				}
			default:
				if err == nil || ready {
					t.Fatalf("unsafe copy admitted: %v %v", ready, err)
				}
			}
		})
	}
}

func TestPrepareSQLPITRCopyPinsCloneAndCapturesNetworkPatch(t *testing.T) {
	for _, scenario := range []string{"prepare", "resume", "failed-clone", "missing-operation", "foreign-operation", "reused-name", "unsafe-inherited", "checkpoint-failure", "capture-failure", "inspect-failure", "guard-failure", "dns-pending", "wrong-endpoint"} {
		t.Run(scenario, func(t *testing.T) {
			in, original, spec := pitrNetworkCopy(t, false)
			pin, err := in.Incarnation("source-project", spec.Region, spec.Name)
			if err != nil {
				t.Fatal(err)
			}
			created, _ := time.Parse(time.RFC3339Nano, in.CreateTime)
			op := func(name, kind string) map[string]any {
				return map[string]any{"kind": "sql#operation", "name": name, "status": "DONE", "targetProject": "source-project", "targetId": spec.Name, "operationType": kind, "insertTime": created.Add(-time.Second), "endTime": created.Add(time.Second)}
			}
			_, prepared, _ := pitrNetworkCopy(t, true)
			patches, reads := 0, 0
			before, captured := false, scenario == "resume" || scenario == "missing-operation"
			patched := captured
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "/operations/") {
					if strings.HasSuffix(r.URL.Path, "/clone-original") {
						clone := op("clone-original", "CLONE")
						if scenario == "failed-clone" {
							clone["error"] = map[string]any{"errors": []any{map[string]string{"message": "clone failed"}}}
						}
						_ = json.NewEncoder(w).Encode(clone)
						return
					}
					if scenario == "missing-operation" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					if !captured {
						t.Error("patch operation polled before checkpoint")
					}
					operation := op("network-patch", "UPDATE")
					if scenario == "foreign-operation" {
						operation["targetId"] = "someone-else"
					}
					_ = json.NewEncoder(w).Encode(operation)
					return
				}
				if r.Method == http.MethodPatch {
					patches++
					if !before || r.URL.Path != "/v1/projects/source-project/instances/recovery-copy" {
						t.Errorf("uncheckpointed or misrouted patch: %s", r.URL)
					}
					var body map[string]any
					if decodeErr := json.NewDecoder(r.Body).Decode(&body); decodeErr != nil {
						t.Error(decodeErr)
					}

					expected := map[string]any{"settings": map[string]any{
						"settingsVersion": "42", "deletionProtectionEnabled": false,
						"userLabels":          map[string]any{"managed-by": "loomplane", SQLIntentLabel: spec.Intent},
						"backupConfiguration": map[string]any{"enabled": false, "pointInTimeRecoveryEnabled": false},
						"ipConfiguration": map[string]any{
							"ipv4Enabled": false, "authorizedNetworks": []any{}, "privateNetwork": "", "enablePrivatePathForGoogleCloudServices": false,
							"pscConfig": map[string]any{
								"pscEnabled": true, "allowedConsumerProjects": []any{"worker-project"}, "networkAttachmentUri": "",
								"pscAutoConnectionPolicyEnabled": false, "pscAutoDnsEnabled": true, "pscWriteEndpointDnsEnabled": false,
								"pscAutoConnections": []any{map[string]any{"consumerProject": "worker-project", "consumerNetwork": recoveryPSCNetwork().Network}},
							},
						},
					}}
					if !reflect.DeepEqual(body, expected) {
						t.Errorf("patch differs from the reviewed recovery settings: %#v", body)
					}

					patched = true
					operation := op("network-patch", "UPDATE")
					if scenario == "foreign-operation" {
						operation["targetId"] = "someone-else"
					}
					_ = json.NewEncoder(w).Encode(operation)
					return
				}
				if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/recovery-copy") {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					w.WriteHeader(500)
					return
				}
				fields := original
				if patched {
					fields = prepared
				}
				if scenario == "reused-name" {
					fields["createTime"] = created.Add(time.Hour)
				}
				if scenario == "unsafe-inherited" {
					fields["settings"].(map[string]any)["ipConfiguration"].(map[string]any)["ipv4Enabled"] = true
				}
				if patched {
					reads++
					endpoint := fields["settings"].(map[string]any)["ipConfiguration"].(map[string]any)["pscConfig"].(map[string]any)["pscAutoConnections"].([]any)[0].(map[string]any)
					if scenario == "dns-pending" && reads < 3 {
						endpoint["instanceAutoDnsStatus"] = "AUTO_DNS_STATUS_UNSPECIFIED"
					} else {
						endpoint["instanceAutoDnsStatus"] = "AUTO_DNS_OK"
					}
					if scenario == "wrong-endpoint" {
						endpoint["consumerNetwork"] = "projects/worker-project/global/networks/production"
					}
				}
				_ = json.NewEncoder(w).Encode(fields)
			}))
			defer server.Close()
			client := New(server.URL)
			client.poll = time.Millisecond
			session := &Session{c: client, key: &Key{ProjectID: "source-project"}, http: server.Client()}
			spec.Inspect = func(*SQLInstance) error {
				if scenario == "inspect-failure" {
					return errors.New("save pin failed")
				}
				return nil
			}
			track := SQLMutation{Check: func(context.Context) error {
				if scenario == "guard-failure" {
					return errors.New("lease lost")
				}
				return nil
			}, Before: func() error {
				before = true
				if scenario == "checkpoint-failure" {
					return errors.New("save uncertain failed")
				}
				return nil
			}, Started: func(operation string) error {
				if operation != "network-patch" {
					t.Error("wrong operation captured", operation)
				}
				if scenario == "capture-failure" {
					return errors.New("capture failed")
				}
				captured = true
				return nil
			}}
			if scenario == "resume" || scenario == "missing-operation" {
				track.Resume = "network-patch"
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			got, err := session.PrepareSQLPITRCopy(ctx, spec, pin, "clone-original", recoveryPSCNetwork(), track)
			switch scenario {
			case "prepare", "resume", "dns-pending":
				if err != nil || got == nil {
					t.Fatalf("copy preparation failed: %v", err)
				}
				want := 1
				if scenario == "resume" {
					want = 0
				}
				if patches != want {
					t.Fatalf("patches=%d want=%d", patches, want)
				}
			default:
				if err == nil || got != nil {
					t.Fatalf("unsafe preparation succeeded: %v %v", got, err)
				}
				if scenario != "capture-failure" && scenario != "foreign-operation" && scenario != "wrong-endpoint" && patches != 0 {
					t.Fatalf("unsafe mutation: %d patches", patches)
				}
			}
		})
	}
}
