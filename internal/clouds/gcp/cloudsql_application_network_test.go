package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestSQLApplicationNetworkPinsReviewedSubnet(t *testing.T) {
	for _, scenario := range []string{"accepted", "default-stack", "dual-stack", "legacy-purpose", "shared-range", "class-e-range", "wrong-network", "wrong-region", "wrong-subnet", "wrong-kind", "missing-id", "noncanonical-id", "proxy-only", "ipv6-only", "too-small", "noncanonical-range", "missing-range", "public-range", "too-wide", "draining", "replaced", "range-changed", "denied", "missing", "revoked", "late-revoked", "invalid-contract", "nil-guard"} {
		t.Run(scenario, func(t *testing.T) {
			route := provider.CloudSQLApplicationNetwork{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps", Subnetwork: "projects/app-project/regions/us-central1/subnetworks/run"}
			fields := map[string]any{"kind": "compute#subnetwork", "id": "1234567890123456789", "name": "run", "selfLink": "https://www.googleapis.com/compute/v1/" + route.Subnetwork, "region": "https://www.googleapis.com/compute/v1/projects/app-project/regions/us-central1", "network": "https://www.googleapis.com/compute/v1/" + route.Network, "ipCidrRange": "10.20.0.0/26", "purpose": "PRIVATE", "stackType": "IPV4_ONLY", "state": "READY"}
			switch scenario {
			case "default-stack":
				delete(fields, "stackType")
			case "dual-stack":
				fields["stackType"] = "IPV4_IPV6"
			case "legacy-purpose":
				fields["purpose"] = "PRIVATE_RFC_1918"
			case "shared-range":
				fields["ipCidrRange"] = "100.64.0.0/26"
			case "class-e-range":
				fields["ipCidrRange"] = "240.0.0.0/26"
			case "public-range":
				fields["ipCidrRange"] = "8.8.8.0/24"
			case "too-wide":
				fields["ipCidrRange"] = "10.0.0.0/7"
			case "draining":
				fields["state"] = "DRAINING"
			case "wrong-network":
				fields["network"] = "projects/app-project/global/networks/other"
			case "wrong-region":
				fields["region"] = "projects/app-project/regions/us-east1"
			case "wrong-subnet":
				fields["selfLink"] = "projects/app-project/regions/us-central1/subnetworks/other"
			case "wrong-kind":
				fields["kind"] = "compute#network"
			case "missing-id":
				delete(fields, "id")
			case "noncanonical-id":
				fields["id"] = "0012"
			case "proxy-only":
				fields["purpose"] = "REGIONAL_MANAGED_PROXY"
			case "ipv6-only":
				fields["stackType"] = "IPV6_ONLY"
			case "too-small":
				fields["ipCidrRange"] = "10.20.0.0/27"
			case "noncanonical-range":
				fields["ipCidrRange"] = "10.20.0.1/26"
			case "missing-range":
				delete(fields, "ipCidrRange")
			case "invalid-contract":
				route.Subnetwork = "../other"
			}
			reads, guards := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reads++
				if r.Method != http.MethodGet || r.URL.Path != "/compute/v1/projects/app-project/regions/us-central1/subnetworks/run" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				if scenario == "denied" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if scenario == "missing" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if reads > 1 {
					if scenario == "replaced" {
						fields["id"] = "987654321"
					}
					if scenario == "range-changed" {
						fields["ipCidrRange"] = "10.20.0.0/24"
					}
				}
				_ = json.NewEncoder(w).Encode(fields)
			}))
			defer srv.Close()
			s := &Session{c: New(srv.URL), key: &Key{ProjectID: "service-project"}, http: srv.Client()}
			guard := func(context.Context) error {
				guards++
				if scenario == "revoked" || scenario == "late-revoked" && guards >= 3 {
					return errors.New("lease revoked")
				}
				return nil
			}
			if scenario == "nil-guard" {
				guard = nil
			}
			got, err := s.ReadSQLApplicationNetwork(t.Context(), "us-central1", route, guard)
			if scenario != "accepted" && scenario != "default-stack" && scenario != "dual-stack" && scenario != "legacy-purpose" && scenario != "shared-range" && scenario != "class-e-range" {
				if err == nil {
					t.Fatal("unsafe subnet accepted", got)
				}
				if scenario == "wrong-network" && !strings.Contains(err.Error(), "application subnet") {
					t.Fatal("misleading subnet error", err)
				}
				if (scenario == "invalid-contract" || scenario == "nil-guard" || scenario == "revoked") && reads != 0 {
					t.Fatal("invalid authorization reached provider", reads)
				}
				return
			}
			if err != nil || got == nil || got.ID != "1234567890123456789" || got.Route != route || got.IPCIDR != fields["ipCidrRange"] || reads != 2 || guards < 3 {
				t.Fatal("subnet witness missing", got, err, reads, guards)
			}
		})
	}
}
