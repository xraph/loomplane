//go:build integration

package platformcloud

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	"github.com/xraph/vault/store/memory"

	"github.com/xraph/loomplane/internal/connections"
	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/instance"
)

// stubDriver reports whatever capabilities the test sets.
type stubDriver struct {
	fc.MachineDriver
	caps fc.Capabilities
}

func (d *stubDriver) Check(context.Context) (fc.Capabilities, error) { return d.caps, nil }

func goodCaps(hostID string) fc.Capabilities {
	return fc.Capabilities{HostID: hostID, Architecture: "amd64", RuntimeIdentity: strings.Repeat("a", 64), IngressAddress: "203.0.113.7",
		Isolation: true, Platform: true, DeniedPorts: []int{25, 465, 587}}
}

type hostEnv struct {
	svc    *Service
	store  *PgStore
	vault  *secret.Service
	secret *connections.VaultAppSecrets
	driver *stubDriver
	server *httptest.Server
	pg     *pgdriver.PgDB
	hostID string
	in     HostInput
}

func newHostEnv(t *testing.T) *hostEnv {
	t.Helper()
	svc, store, pg := newTenantService(t)
	enc, err := crypto.NewEncryptor(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	vault := secret.NewService(memory.New(), enc, secret.WithAppID(connections.PlatformCloudVaultAppID))
	secrets := connections.NewVaultAppSecretsFor(vault, connections.PlatformCloudVaultAppID)
	svc.WithSecrets(secrets)
	if err = store.CreateRegion(t.Context(), &Region{ID: "reg_1", Name: "eu-west", DisplayName: "EU West", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	e := &hostEnv{svc: svc, store: store, vault: vault, secret: secrets, hostID: "hw-1", pg: pg}
	e.driver = &stubDriver{caps: goodCaps(e.hostID)}
	e.driver.caps.Limits = fc.Limits{}
	return e
}

// start serves the fake agent. It must run after AddHost, which makes the
// client certificate the agent trusts, so AddHost is given the server's
// certificate through serverCA first.
func (e *hostEnv) serve(t *testing.T, limits fc.Limits) {
	t.Helper()
	e.server = httptest.NewUnstartedServer(nil)
	e.server.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13}
	e.server.StartTLS()
	t.Cleanup(e.server.Close)
	e.in = HostInput{RegionID: "reg_1", Endpoint: e.server.URL, HostID: e.hostID,
		ServerCA:  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: e.server.Certificate().Raw})),
		Allotment: Allotment{VCPUs: 4, MemoryMiB: 4096, DiskMiB: 10000, VMs: 4}}
	e.driver.caps.Limits = limits
}

// trust makes the agent accept the client certificate AddHost produced.
func (e *hostEnv) trust(t *testing.T, nh *NewHost) {
	t.Helper()
	block, _ := pem.Decode([]byte(nh.ClientCertificate))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	e.server.TLS.ClientCAs = pool
	e.server.Config.Handler = fc.Handler{Host: &fc.Host{Driver: e.driver, Limits: e.driver.caps.Limits}, Platform: nh.ClientFingerprint}
}

func agentLimits() fc.Limits {
	return fc.Limits{VMs: 8, VCPUs: 16, MemoryMiB: 32768, DiskMiB: 100000, PerOrg: fc.OrgLimits{VMs: 4, VCPUs: 8, MemoryMiB: 8192}}
}

func TestAddHostShowsCertificateOnce(t *testing.T) {
	e := newHostEnv(t)
	e.serve(t, agentLimits())
	nh, err := e.svc.AddHost(t.Context(), "owner", e.in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(nh.ClientCertificate, "BEGIN CERTIFICATE") || len(nh.ClientFingerprint) != 64 {
		t.Fatalf("certificate %q fingerprint %q", nh.ClientCertificate, nh.ClientFingerprint)
	}
	if nh.Host.Status != "pending" || nh.Host.ClientFingerprint != nh.ClientFingerprint {
		t.Fatalf("host %+v", nh.Host)
	}
	got, err := e.store.GetHost(t.Context(), nh.Host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(got); strings.Contains(string(raw), "BEGIN") {
		t.Fatal("stored host carries a certificate")
	}
	raw, err := e.secret.Reveal(t.Context(), "platform-cloud/host/"+nh.Host.ID)
	if err != nil || !strings.Contains(string(raw), "PRIVATE KEY") {
		t.Fatalf("vault: %v %q", err, raw)
	}
}

func TestCheckHostReadyOnlyWithPlatformEvidence(t *testing.T) {
	e := newHostEnv(t)
	e.serve(t, agentLimits())
	nh, err := e.svc.AddHost(t.Context(), "owner", e.in)
	if err != nil {
		t.Fatal(err)
	}
	e.trust(t, nh)
	id := nh.Host.ID

	e.driver.caps.SMTActive = true
	h, err := e.svc.CheckHost(t.Context(), "owner", id)
	if err != nil {
		t.Fatal(err)
	}
	if h.Status != "pending" || !strings.Contains(h.Evidence.Reason, "SMT") {
		t.Fatalf("smt on: %s %q", h.Status, h.Evidence.Reason)
	}

	e.driver.caps.SMTActive = false
	e.driver.caps.HostID = "other"
	if h, err = e.svc.CheckHost(t.Context(), "owner", id); err != nil || h.Status != "pending" || !strings.Contains(h.Evidence.Reason, "different host ID") {
		t.Fatalf("host id mismatch: %v %+v", err, h)
	}

	e.driver.caps.HostID = e.hostID
	h, err = e.svc.CheckHost(t.Context(), "owner", id)
	if err != nil {
		t.Fatal(err)
	}
	if h.Status != "ready" || h.Evidence.Reason != "" || h.Evidence.CheckedAt.IsZero() || h.Evidence.PerOrgVMs != 4 || h.Evidence.PerOrgVCPUs != 8 || h.Evidence.PerOrgMemoryMiB != 8192 || !h.Evidence.Platform {
		t.Fatalf("good: %+v", h)
	}

	// A later failure takes a ready host back to pending.
	e.driver.caps.SMTActive = true
	if h, err = e.svc.CheckHost(t.Context(), "owner", id); err != nil || h.Status != "pending" {
		t.Fatalf("regression: %v %+v", err, h)
	}
}

func TestCheckHostRefusesAllotmentBeyondAgentLimits(t *testing.T) {
	e := newHostEnv(t)
	e.serve(t, agentLimits())
	e.in.Allotment.VCPUs = 64
	nh, err := e.svc.AddHost(t.Context(), "owner", e.in)
	if err != nil {
		t.Fatal(err)
	}
	e.trust(t, nh)
	h, err := e.svc.CheckHost(t.Context(), "owner", nh.Host.ID)
	if err != nil || h.Status != "pending" || h.Evidence.Reason == "" {
		t.Fatalf("%v %+v", err, h)
	}
}

func TestRetireOnlyDrainedHostsWithoutPlacements(t *testing.T) {
	e := newHostEnv(t)
	e.serve(t, agentLimits())
	nh, err := e.svc.AddHost(t.Context(), "owner", e.in)
	if err != nil {
		t.Fatal(err)
	}
	e.trust(t, nh)
	host := nh.Host.ID
	key := "platform-cloud/host/" + host
	if _, err = e.svc.CheckHost(t.Context(), "owner", host); err != nil {
		t.Fatal(err)
	}
	if err = e.svc.RetireHost(t.Context(), "owner", host); !errors.Is(err, ErrConflict) {
		t.Fatalf("retire of a ready host: %v", err)
	}
	if _, err = e.svc.DrainHost(t.Context(), "owner", host); err != nil {
		t.Fatal(err)
	}
	addPlacement(t, e.pg, "p1", "org1", "service", "running", &host, 1, 128, 10)
	if err = e.svc.RetireHost(t.Context(), "owner", host); !errors.Is(err, ErrConflict) {
		t.Fatalf("retire with placement: %v", err)
	}
	if _, err = e.secret.Reveal(t.Context(), key); err != nil {
		t.Fatal("secret deleted despite refusal")
	}
	if _, err = e.pg.Exec(t.Context(), `UPDATE loomplane_platform_placements SET state='released' WHERE id='p1'`); err != nil {
		t.Fatal(err)
	}
	if err = e.svc.RetireHost(t.Context(), "owner", host); err != nil {
		t.Fatal(err)
	}
	got, _ := e.store.GetHost(t.Context(), host)
	if got.Status != "retired" {
		t.Fatal(got.Status)
	}
	if _, err = e.secret.Reveal(t.Context(), key); err == nil {
		t.Fatal("secret kept after retire")
	}
}

func TestDrainAndSetAllotment(t *testing.T) {
	e := newHostEnv(t)
	e.serve(t, agentLimits())
	nh, err := e.svc.AddHost(t.Context(), "owner", e.in)
	if err != nil {
		t.Fatal(err)
	}
	h, err := e.svc.SetAllotment(t.Context(), "owner", nh.Host.ID, Allotment{VCPUs: 2, MemoryMiB: 1024, DiskMiB: 5000, VMs: 2})
	if err != nil || h.Allotment.VCPUs != 2 {
		t.Fatalf("%v %+v", err, h)
	}
	if h, err = e.svc.DrainHost(t.Context(), "owner", nh.Host.ID); err != nil || h.Status != "draining" {
		t.Fatalf("%v %+v", err, h)
	}
}

func TestRegistryConfigIsPerWorkspace(t *testing.T) {
	e := newHostEnv(t)
	set := instance.PlatformCloudSettings{Registry: instance.RegistrySettings{Endpoint: "https://registry.example", Repository: "platform", AuthEndpoint: "https://auth.example"}}
	e.svc.settings = fixedSettings{set}
	if err := e.svc.SetRegistrySecret(t.Context(), "owner", "robot", "pw", ""); err != nil {
		t.Fatal(err)
	}
	cfg, err := e.svc.registryConfig(t.Context(), "org-a")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repository != "platform/ws-org-a" || cfg.Endpoint != "https://registry.example" || cfg.AuthEndpoint != "https://auth.example" || cfg.Username != "robot" || cfg.Password != "pw" {
		t.Fatalf("%+v", cfg)
	}
	b, _ := e.svc.registryConfig(t.Context(), "org-b")
	if b.Repository != "platform/ws-org-b" {
		t.Fatal(b.Repository)
	}
}

type fixedSettings struct {
	s instance.PlatformCloudSettings
}

func (f fixedSettings) PlatformCloud(context.Context) (instance.PlatformCloudSettings, error) {
	return f.s, nil
}
