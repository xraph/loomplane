package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/xraph/loomplane/internal/manageddb"
)

type sourceDNSLookup func(context.Context, string, string) ([]netip.Addr, error)

func (f sourceDNSLookup) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

func TestSQLPITRManualEndpointBindsSourceNetworkAndDNS(t *testing.T) {
	for _, scenario := range []string{"accepted", "extra-consumer", "automatic-dns", "wrong-target", "wrong-network", "wrong-rule", "wrong-region", "public-address", "pending", "missing-id", "dns-other-address", "dns-extra-address", "dns-error", "source-replaced", "settings-changed", "endpoint-replaced", "revoked", "invalid-contract", "foreign-attachment-host", "late-revoked", "nil-guard", "invalid-dns-name", "empty-dns-answer"} {
		t.Run(scenario, func(t *testing.T) {
			in, fields := pscPITRInstance(t)
			physical, err := in.Incarnation("source-project", "us-central1", "source-db")
			if err != nil {
				t.Fatal(err)
			}
			const attachment = "https://www.googleapis.com/compute/v1/projects/sql-tenant/regions/us-central1/serviceAttachments/source-attachment"
			const endpointPath = "projects/app-project/regions/us-central1/forwardingRules/database"
			const networkPath = "projects/app-project/global/networks/apps"
			fields["pscServiceAttachmentLink"], fields["dnsName"] = attachment, "source-db.us-central1.sql.goog."
			psc := fields["settings"].(map[string]any)["ipConfiguration"].(map[string]any)["pscConfig"].(map[string]any)
			psc["allowedConsumerProjects"] = []string{"app-project"}
			rule := map[string]any{"kind": "compute#forwardingRule", "id": "1234567890123456789", "name": "database", "selfLink": "https://www.googleapis.com/compute/v1/" + endpointPath, "region": "https://www.googleapis.com/compute/v1/projects/app-project/regions/us-central1", "network": "https://www.googleapis.com/compute/v1/" + networkPath, "target": attachment, "IPAddress": "10.20.0.8", "pscConnectionStatus": "ACCEPTED"}
			switch scenario {
			case "extra-consumer":
				psc["allowedConsumerProjects"] = []string{"app-project", "another-project"}
			case "automatic-dns":
				psc["pscAutoDnsEnabled"] = true
			case "invalid-dns-name":
				fields["dnsName"] = "attacker.invalid"
			case "wrong-target":
				rule["target"] = "https://www.googleapis.com/compute/v1/projects/sql-tenant/regions/us-central1/serviceAttachments/another-source"
			case "wrong-network":
				rule["network"] = "https://www.googleapis.com/compute/v1/projects/app-project/global/networks/other"
			case "wrong-rule":
				rule["selfLink"] = "https://www.googleapis.com/compute/v1/projects/app-project/regions/us-central1/forwardingRules/other"
			case "wrong-region":
				rule["region"] = "https://www.googleapis.com/compute/v1/projects/app-project/regions/us-east1"
			case "public-address":
				rule["IPAddress"] = "198.51.100.7"
			case "pending":
				rule["pscConnectionStatus"] = "PENDING"
			case "missing-id":
				delete(rule, "id")
			case "foreign-attachment-host":
				fields["pscServiceAttachmentLink"] = "https://attacker.invalid/compute/v1/projects/sql-tenant/regions/us-central1/serviceAttachments/source-attachment"
				rule["target"] = fields["pscServiceAttachmentLink"]
			}
			sourceReads, endpointReads, calls, guards := 0, 0, 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				if r.Method != http.MethodGet {
					t.Errorf("verification mutated a resource: %s", r.Method)
					w.WriteHeader(400)
					return
				}
				switch r.URL.Path {
				case "/v1/projects/source-project/instances/source-db":
					sourceReads++
					if sourceReads > 1 {
						if scenario == "source-replaced" {
							fields["createTime"] = "2026-10-21T00:00:00Z"
						}
						if scenario == "settings-changed" {
							fields["settings"].(map[string]any)["settingsVersion"] = "43"
						}
					}
					_ = json.NewEncoder(w).Encode(fields)
				case "/compute/v1/" + endpointPath:
					endpointReads++
					if endpointReads > 1 && scenario == "endpoint-replaced" {
						rule["id"] = "987654321"
					}
					_ = json.NewEncoder(w).Encode(rule)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(400)
				}
			}))
			defer srv.Close()
			c := New(srv.URL)
			c.resolver = sourceDNSLookup(func(_ context.Context, network, host string) ([]netip.Addr, error) {
				if network != "ip" || host != "source-db.us-central1.sql.goog." {
					t.Error("lookup did not use the instance DNS name", network, host)
				}
				if scenario == "empty-dns-answer" {
					return nil, nil
				}
				if scenario == "dns-error" {
					return nil, errors.New("NXDOMAIN")
				}
				if scenario == "dns-other-address" {
					return []netip.Addr{netip.MustParseAddr("10.20.0.9")}, nil
				}
				ips := []netip.Addr{netip.MustParseAddr("10.20.0.8")}
				if scenario == "dns-extra-address" {
					ips = append(ips, netip.MustParseAddr("203.0.113.1"))
				}
				return ips, nil
			})
			s := &Session{c: c, key: &Key{ProjectID: "source-project"}, http: srv.Client()}
			endpoint := manageddb.CloudSQLSourceEndpoint{ConsumerProject: "app-project", Network: networkPath, ForwardingRule: endpointPath}
			if scenario == "invalid-contract" {
				endpoint.ForwardingRule = "../other"
			}
			guard := func(context.Context) error {
				guards++
				if scenario == "revoked" && guards > 1 || scenario == "late-revoked" && guards > 4 {
					return errors.New("lease revoked")
				}
				return nil
			}
			if scenario == "nil-guard" {
				guard = nil
			}
			got, err := s.ReadSQLPITRSourceEndpoint(t.Context(), pscPITRSpec(), physical, endpoint, guard)
			if scenario != "accepted" {
				if err == nil {
					t.Fatal("unsafe endpoint accepted", got)
				}
				if scenario == "invalid-contract" && calls != 0 {
					t.Fatal("invalid contract reached provider")
				}
				return
			}
			if err != nil || got.ID != "1234567890123456789" || got.Address != "10.20.0.8" || got.DNSName != fields["dnsName"] || got.SourcePhysicalID != physical || got.SettingsDigest == "" || sourceReads != 2 || endpointReads != 2 {
				t.Fatal(fmt.Sprint("endpoint evidence missing: ", got, " ", err, sourceReads, endpointReads))
			}
		})
	}
}
