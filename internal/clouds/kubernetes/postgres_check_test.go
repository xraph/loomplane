package kubernetes

import (
	"encoding/pem"
	"slices"
	"strings"
	"testing"
)

func TestPostgresConnectionChecksRecoveryPermissions(t *testing.T) {
	for _, denied := range []string{"", "pods/exec:create", "pods:create", "pods:delete", "networkpolicies:list", "networkpolicies:update", "persistentvolumes:update", "persistentvolumeclaims:delete"} {
		t.Run(denied, func(t *testing.T) {
			server, paths, mu := clusterFixture(t, false, denied)
			ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
			cfg, err := ParseConfig(kubeconfig(t, server.URL, ca), map[string]string{"namespace": "production", "storage_class": "retained", "architecture": "arm64"})
			if err != nil {
				t.Fatal(err)
			}
			client, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Check(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Missing) != 0 {
				t.Fatalf("optional database permissions blocked ordinary workloads: %+v", result.Missing)
			}
			if result.Details["architecture"] != "arm64" {
				t.Fatal("database architecture was not pinned")
			}
			if denied == "" {
				if result.Details["postgres_recovery_permissions"] != "ready" || result.Details["postgres_missing_permissions"] != "" {
					t.Fatalf("unchecked database profile: %+v", result.Details)
				}
			} else if result.Details["postgres_recovery_permissions"] != "unavailable" || !strings.Contains(result.Details["postgres_missing_permissions"], denied) {
				t.Fatalf("database permission denial was hidden: %+v", result.Details)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, request := range []string{"production:pods/exec:create", "production:pods:create", "production:pods:delete", "production:networkpolicies:get", "production:networkpolicies:list", "production:networkpolicies:create", "production:networkpolicies:update", "production:networkpolicies:delete", ":persistentvolumes:update", "production:persistentvolumeclaims:delete"} {
				if !slices.Contains(*paths, request) {
					t.Fatalf("unchecked PostgreSQL permission %s", request)
				}
			}
		})
	}
}

func TestPostgresConnectionRequiresExplicitSupportedArchitecture(t *testing.T) {
	raw := kubeconfig(t, "https://cluster.example.com", testCA(t))
	for _, arch := range []string{"amd64", "arm64", "", "s390x", "arm64/foreign"} {
		cfg, err := ParseConfig(raw, map[string]string{"architecture": arch})
		if arch == "s390x" || arch == "arm64/foreign" {
			if err == nil {
				t.Fatalf("accepted unsupported architecture %s", arch)
			}
		} else if err != nil || cfg.Details["architecture"] != arch {
			t.Fatalf("explicit architecture %s was not retained: %v", arch, err)
		}
	}
}
