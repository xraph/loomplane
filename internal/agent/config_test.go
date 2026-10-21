package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func launchFixture(t *testing.T) LaunchConfig {
	t.Helper()
	identity := domain.AgentIdentity{Scope: domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, DestinationID: "destination", AgentID: "agent", Epoch: 1}
	uri, err := workloadidentity.AgentURI("loomplane.test", identity)
	if err != nil {
		t.Fatal(err)
	}
	identity.URI = uri
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return LaunchConfig{Version: 1, ControlEndpoint: "https://control.example:9443", WorkloadAPI: "unix:///run/spire/agent.sock", StatePath: filepath.Join(t.TempDir(), "state", "agent.db"), EnrollmentID: "01234567-89ab-cdef-0123-456789abcdef", EnrollmentExpiresAt: time.Now().Add(time.Minute), Registration: runtimecatalog.Registration{Agent: identity, DestinationGeneration: 1, Active: true, Exports: map[string]runtimecatalog.ExportBounds{}}, Namespace: "workloads", TrustGeneration: 1, SigningKeys: []access.SnapshotKey{{PublicKey: public, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}}}
}
func writeLaunchConfig(t *testing.T, cfg LaunchConfig) string {
	t.Helper()
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestLaunchConfigurationPinsEnrollmentAndTrust(t *testing.T) {
	cfg := launchFixture(t)
	path := writeLaunchConfig(t, cfg)
	loaded, err := LoadLaunchConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Registration.Agent != cfg.Registration.Agent || loaded.SigningKeys[0].ID() != cfg.SigningKeys[0].ID() {
		t.Fatal("launch authority changed")
	}
	for name, change := range map[string]func(*LaunchConfig){
		"version":   func(c *LaunchConfig) { c.Version = 2 },
		"identity":  func(c *LaunchConfig) { c.Registration.Agent.OrgID = "other" },
		"inactive":  func(c *LaunchConfig) { c.Registration.Active = false },
		"epoch":     func(c *LaunchConfig) { c.Registration.Agent.Epoch = 0 },
		"bootstrap": func(c *LaunchConfig) { c.EnrollmentID = "not-an-enrollment" },
		"expiry":    func(c *LaunchConfig) { c.EnrollmentExpiresAt = time.Now().Add(-time.Second) },
		"lifetime":  func(c *LaunchConfig) { c.EnrollmentExpiresAt = time.Now().Add(time.Hour) },
		"endpoint":  func(c *LaunchConfig) { c.ControlEndpoint = "http://control.example" },
		"socket":    func(c *LaunchConfig) { c.WorkloadAPI = "tcp://127.0.0.1:8081" },
		"state":     func(c *LaunchConfig) { c.StatePath = "relative.db" },
		"namespace": func(c *LaunchConfig) { c.Namespace = "../other" },
		"missing-targets": func(c *LaunchConfig) {
			c.Registration.Exports = map[string]runtimecatalog.ExportBounds{"export": {Scope: c.Registration.Agent.Scope, DestinationID: c.Registration.Agent.DestinationID, PlacementID: "placement", BindingID: "binding", Port: 8080, CIDRs: []string{"10.0.0.0/24"}}}
		},
		"trust":            func(c *LaunchConfig) { c.SigningKeys = nil },
		"expired-trust":    func(c *LaunchConfig) { c.SigningKeys[0].NotAfter = time.Now().Add(-time.Second) },
		"trust-generation": func(c *LaunchConfig) { c.TrustGeneration = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			current := launchFixture(t)
			change(&current)
			if _, err := LoadLaunchConfig(writeLaunchConfig(t, current)); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}
func TestLaunchConfigurationRejectsTamperedFiles(t *testing.T) {
	for _, mode := range []string{"writable", "symlink", "unknown", "trailing", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			path := writeLaunchConfig(t, launchFixture(t))
			switch mode {
			case "writable":
				if err := os.Chmod(path, 0o666); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				link := path + ".link"
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			default:
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "unknown":
					body = append([]byte(`{"private_key":"must-never-appear",`), body[1:]...)
				case "trailing":
					body = append(body, []byte(" {}")...)
				case "oversized":
					body = []byte(strings.Repeat(" ", 2<<20))
				}
				if err := os.WriteFile(path, body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := LoadLaunchConfig(path)
			if err == nil {
				t.Fatal("unsafe file accepted")
			}
			if strings.Contains(err.Error(), "must-never-appear") {
				t.Fatal("configuration leaked into error")
			}
		})
	}
}
