package cloudinfra

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestEnrollmentEndpointRequiresExplicitReachableTLS(t *testing.T) {
	for _, endpoint := range []string{"", "http://example.com", "https://localhost:8780", "https://127.0.0.1", "https://10.1.1.1", "https://user:pass@example.com", "https://example.com?token=x"} {
		s := NewService(Deps{EnrollmentURL: endpoint})
		if err := s.validateEnrollmentURL(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %q: %v", endpoint, err)
		}
	}
	if err := NewService(Deps{EnrollmentURL: "https://enroll.example.com"}).validateEnrollmentURL(); err != nil {
		t.Fatal(err)
	}
	if err := NewService(Deps{EnrollmentURL: "http://127.0.0.1:1234", AllowLocalEnrollment: true}).validateEnrollmentURL(); err != nil {
		t.Fatal(err)
	}
}

// bootstrapSecrets is an in-memory Vault keyed by org and ID.
type bootstrapSecrets map[string][]byte

func (s bootstrapSecrets) Put(_ context.Context, org, id string, raw []byte) error {
	s[org+"/"+id] = append([]byte{}, raw...)
	return nil
}
func (s bootstrapSecrets) Reveal(_ context.Context, org, id string) ([]byte, error) {
	raw, ok := s[org+"/"+id]
	if !ok {
		return nil, connections.ErrNotFound
	}
	return append([]byte{}, raw...), nil
}
func (s bootstrapSecrets) Delete(_ context.Context, org, id string) error {
	delete(s, org+"/"+id)
	return nil
}

// fixedBootstrapSecrets holds a bootstrap secret made from a zero seed, so the
// cloud-config prepareBootstrap renders is the same on every run.
func fixedBootstrapSecrets(t *testing.T, operation string) bootstrapSecrets {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(bootstrapSecret{PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), Token: "fixture-token"})
	if err != nil {
		t.Fatal(err)
	}
	secrets := bootstrapSecrets{}
	if err = secrets.Put(t.Context(), "org", bootstrapKey(operation), raw); err != nil {
		t.Fatal(err)
	}
	return secrets
}

func bootstrapDocument(t *testing.T, config string) (runcmd []string, packages []string) {
	t.Helper()
	body, ok := strings.CutPrefix(config, "#cloud-config\n")
	if !ok {
		t.Fatalf("not a cloud-config: %q", config)
	}
	var doc struct {
		RunCmd   [][]string `json:"runcmd"`
		Packages []string   `json:"packages"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	for _, command := range doc.RunCmd {
		runcmd = append(runcmd, strings.Join(command, " "))
	}
	return runcmd, doc.Packages
}

func TestBootstrapConfiguresNVIDIAToolkitForGPUServers(t *testing.T) {
	s := NewService(Deps{EnrollmentURL: "https://enroll.example.com", Secrets: fixedBootstrapSecrets(t, "infraop_fixture")})
	// A CPU server's cloud-config is byte for byte what it was before GPU servers.
	cpu, err := s.prepareBootstrap(t.Context(), &Operation{ID: "infraop_fixture", OrgID: "org", Input: ServerInput{Kind: "create"}})
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile("testdata/cpu-cloud-config.golden")
	if err != nil {
		t.Fatal(err)
	}
	if cpu != string(golden) {
		t.Fatalf("CPU cloud-config changed:\n%s\nwant:\n%s", cpu, golden)
	}

	gpu, err := s.prepareBootstrap(t.Context(), &Operation{ID: "infraop_fixture", OrgID: "org", Input: ServerInput{Kind: "create"}, Accelerator: &provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: "nvidia", Count: 1, MemoryGiB: 80}})
	if err != nil {
		t.Fatal(err)
	}
	runcmd, packages := bootstrapDocument(t, gpu)
	index := func(want string) int {
		return slices.IndexFunc(runcmd, func(c string) bool { return strings.Contains(c, want) })
	}
	install := index("nvidia-container-toolkit")
	configure := index("nvidia-ctk runtime configure --runtime=docker")
	restart := index("systemctl restart docker")
	enroll := index("python3 /root/loomplane-enroll.py")
	if index("systemctl enable --now docker") != 0 || install < 1 || configure <= install || restart <= configure || enroll <= restart {
		t.Fatalf("toolkit steps out of order: %q", runcmd)
	}
	script := runcmd[install]
	for _, want := range []string{"command -v nvidia-ctk", "exit 0", "https://nvidia.github.io/libnvidia-container/gpgkey", "https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list", "apt-get install -y nvidia-container-toolkit"} {
		if !strings.Contains(script, want) {
			t.Fatalf("toolkit script lacks %q: %s", want, script)
		}
	}
	// The script only installs when nvidia-ctk is missing, so the check comes first.
	if strings.Index(script, "command -v nvidia-ctk") > strings.Index(script, "apt-get") {
		t.Fatalf("toolkit script installs before checking: %s", script)
	}
	// The GPU-ready image ships the driver. The bootstrap never installs one.
	for _, c := range runcmd {
		if strings.Contains(c, "nvidia-driver") || strings.Contains(c, "cuda-drivers") || strings.Contains(c, "ubuntu-drivers") {
			t.Fatalf("bootstrap installs a driver: %s", c)
		}
	}
	if !slices.Contains(packages, "docker.io") || !slices.Contains(packages, "curl") || !slices.Contains(packages, "gnupg") {
		t.Fatalf("GPU packages: %q", packages)
	}
	// An AMD accelerator gets no NVIDIA steps (creation refuses AMD sizes anyway).
	amd, err := s.prepareBootstrap(t.Context(), &Operation{ID: "infraop_fixture", OrgID: "org", Input: ServerInput{Kind: "create"}, Accelerator: &provider.Accelerator{Kind: "amd-mi300x", Vendor: "amd", Count: 1}})
	if err != nil || amd != cpu {
		t.Fatalf("AMD cloud-config: %v\n%s", err, amd)
	}
}

// gpuServerStore keeps servers in memory for readyRuntime and checkServer.
type gpuServerStore struct {
	Store
	servers map[string]*Server
}

func (s *gpuServerStore) SaveServer(_ context.Context, v *Server) error {
	copied := *v
	s.servers[v.ID] = &copied
	return nil
}
func (s *gpuServerStore) GetServer(_ context.Context, _, _, id string) (*Server, error) {
	v, ok := s.servers[id]
	if !ok {
		return nil, ErrNotFound
	}
	copied := *v
	return &copied, nil
}

// gpuConnectionStore is a connections.Store that copies on every read and write.
type gpuConnectionStore struct {
	rows    map[string]*connections.Connection
	updates int
}

func copyConnection(c *connections.Connection) *connections.Connection {
	copied := *c
	copied.Details = maps.Clone(c.Details)
	return &copied
}
func (s *gpuConnectionStore) CreateConnection(_ context.Context, c *connections.Connection) error {
	if _, ok := s.rows[c.ID]; ok {
		return connections.ErrExists
	}
	s.rows[c.ID] = copyConnection(c)
	return nil
}
func (s *gpuConnectionStore) GetConnection(_ context.Context, _, id string) (*connections.Connection, error) {
	c, ok := s.rows[id]
	if !ok {
		return nil, connections.ErrNotFound
	}
	return copyConnection(c), nil
}
func (s *gpuConnectionStore) ListConnections(context.Context, string) ([]*connections.Connection, error) {
	return nil, nil
}
func (s *gpuConnectionStore) UpdateConnection(_ context.Context, c *connections.Connection) error {
	if _, ok := s.rows[c.ID]; !ok {
		return connections.ErrNotFound
	}
	s.updates++
	s.rows[c.ID] = copyConnection(c)
	return nil
}
func (s *gpuConnectionStore) DeleteConnection(_ context.Context, _, id string) error {
	delete(s.rows, id)
	return nil
}

// Credential serves checkServer the stored runtime connection.
func (s *gpuConnectionStore) Credential(ctx context.Context, org, id string) (*connections.Connection, []byte, error) {
	c, err := s.GetConnection(ctx, org, id)
	return c, []byte("key"), err
}

// gpuHealthRuntime answers every Inspect with the same health.
type gpuHealthRuntime struct{ health RuntimeHealth }

func (r *gpuHealthRuntime) Inspect(context.Context, []byte, map[string]string, bool) (RuntimeHealth, error) {
	return r.health, nil
}

// gpuHostKey is a real public host key, since enrollment parses the one it records.
var gpuHostKey = func() string {
	signer, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	if err != nil {
		panic(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
}()

var h100Device = provider.GPUDevice{Index: 0, UUID: "GPU-0", Kind: "nvidia-h100-80gb", Product: "NVIDIA H100 80GB HBM3", MemoryMiB: 81559}

func gpuEnrollment(t *testing.T, health RuntimeHealth) (*Service, *gpuServerStore, *gpuConnectionStore, *Operation, *Server) {
	t.Helper()
	store := &gpuServerStore{servers: map[string]*Server{}}
	conns := &gpuConnectionStore{rows: map[string]*connections.Connection{}}
	s := NewService(Deps{Store: store, Secrets: fixedBootstrapSecrets(t, "infraop_gpu"), RuntimeConnections: conns, Connections: conns, Runtime: &gpuHealthRuntime{health: health}})
	o := &Operation{ID: "infraop_gpu", OrgID: "org", CloudID: "cloud", ServerID: "server_gpu", Input: ServerInput{Kind: "create", Size: "gpu-h100x1-80gb"}, PublicIPv4: []string{"203.0.113.7"}, HostKey: gpuHostKey, Enrolled: true,
		EstimatedHourlyUSD: 3.39, HourlyPriceKnown: true, Accelerator: &provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: "nvidia", Count: 1, MemoryGiB: 80}}
	v := &Server{ID: "server_gpu", OrgID: "org", CloudID: "cloud", Ownership: "managed", Size: "gpu-h100x1-80gb", Status: "provisioning", Revision: 1}
	return s, store, conns, o, v
}

func TestRuntimeConnectionCarriesGPUInventory(t *testing.T) {
	ready := RuntimeHealth{DockerID: "docker-gpu", Architecture: "amd64", CPUs: 20, MemoryBytes: 240 << 30, GPUs: []provider.GPUDevice{h100Device}, GPURuntime: provider.GPUStatusReady, CheckedAt: time.Now().UTC()}
	inventory := func(t *testing.T, conns *gpuConnectionStore) provider.GPUInventory {
		t.Helper()
		c, err := conns.GetConnection(t.Context(), "org", "conn_gpu")
		if err != nil {
			t.Fatal(err)
		}
		inv, ok := provider.ParseGPUInventory(c.Details)
		if !ok || c.Details[provider.GPUDetailStatus] != inv.Status || c.Details["docker_id"] != "docker-gpu" || c.Details["host_key"] != gpuHostKey {
			t.Fatalf("runtime connection details: %v", c.Details)
		}
		return inv
	}

	t.Run("create", func(t *testing.T) {
		s, _, conns, o, v := gpuEnrollment(t, ready)
		if err := s.readyRuntime(t.Context(), o, v); err != nil {
			t.Fatal(err)
		}
		if inv := inventory(t, conns); inv.Status != provider.GPUStatusReady || len(inv.Devices) != 1 || inv.Devices[0].Kind != "nvidia-h100-80gb" {
			t.Fatalf("enrolled GPU inventory: %+v", inv)
		}
	})

	t.Run("exists", func(t *testing.T) {
		// A lost database response left the connection behind without GPU details.
		s, _, conns, o, v := gpuEnrollment(t, ready)
		conns.rows["conn_gpu"] = &connections.Connection{ID: "conn_gpu", OrgID: "org", Cloud: connections.Server, Account: "docker-gpu", Status: connections.StatusReady,
			Details: map[string]string{"host": "203.0.113.7", "port": "22", "user": "loomplane", "host_key": gpuHostKey, "docker_socket": "/var/run/docker.sock", "docker_id": "docker-gpu", "architecture": "amd64"}}
		if err := s.readyRuntime(t.Context(), o, v); err != nil {
			t.Fatal(err)
		}
		if inv := inventory(t, conns); inv.Status != provider.GPUStatusReady || len(inv.Devices) != 1 {
			t.Fatalf("recovered GPU inventory: %+v", inv)
		}
		// A second pass with the same inventory writes nothing.
		updates := conns.updates
		if err := s.readyRuntime(t.Context(), o, v); err != nil {
			t.Fatal(err)
		}
		if conns.updates != updates {
			t.Fatalf("unchanged inventory rewrote the connection: %d updates", conns.updates-updates)
		}
	})

	t.Run("check", func(t *testing.T) {
		// A server check, which inventory refresh runs, writes the latest inventory.
		s, store, conns, o, v := gpuEnrollment(t, RuntimeHealth{DockerID: "docker-gpu", Architecture: "amd64", CPUs: 20, MemoryBytes: 240 << 30, GPUs: []provider.GPUDevice{h100Device}, GPURuntime: provider.GPUStatusToolkitMissing, GPUMessage: "Docker can't use the GPU.", CheckedAt: time.Now().UTC()})
		if err := s.readyRuntime(t.Context(), o, v); err != nil {
			t.Fatal(err)
		}
		if inv := inventory(t, conns); inv.Status != provider.GPUStatusToolkitMissing || inv.Message != "Docker can't use the GPU." {
			t.Fatalf("unready GPU inventory: %+v", inv)
		}
		s.Runtime = &gpuHealthRuntime{health: ready}
		checked, err := s.checkServer(t.Context(), "org", "cloud", "server_gpu")
		if err != nil || checked.Health.GPURuntime != provider.GPUStatusReady {
			t.Fatalf("check: %+v %v", checked, err)
		}
		if inv := inventory(t, conns); inv.Status != provider.GPUStatusReady || inv.Message != "" {
			t.Fatalf("checked GPU inventory: %+v", inv)
		}
		if store.servers["server_gpu"].Health.GPURuntime != provider.GPUStatusReady {
			t.Fatal("server health kept the old GPU runtime")
		}
	})
}

func TestUnreadyGPUServerStillEnrolls(t *testing.T) {
	for _, tc := range []struct {
		name, runtime, message, want string
	}{
		{"toolkit missing", provider.GPUStatusToolkitMissing, "This server has 1 NVIDIA H100 80GB GPU but Docker can't use them.", provider.GPUStatusToolkitMissing},
		// A GPU Droplet whose probe found no GPU still has the one it was bought
		// with, so its count stays unknown rather than zero.
		{"none found", provider.GPUStatusNone, "", provider.GPUStatusUnknown},
		{"unread", "", "", provider.GPUStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store, conns, o, v := gpuEnrollment(t, RuntimeHealth{DockerID: "docker-gpu", Architecture: "amd64", CPUs: 20, MemoryBytes: 240 << 30, GPURuntime: tc.runtime, GPUMessage: tc.message, CheckedAt: time.Now().UTC()})
			if err := s.readyRuntime(t.Context(), o, v); err != nil {
				t.Fatal(err)
			}
			got := store.servers["server_gpu"]
			if got.Status != "ready" || got.RuntimeConnectionID != "conn_gpu" || got.Health.GPURuntime != tc.want || got.Health.GPUMessage == "" {
				t.Fatalf("unready GPU server: %+v", got)
			}
			if tc.message != "" && got.Health.GPUMessage != tc.message {
				t.Fatalf("probe message replaced: %q", got.Health.GPUMessage)
			}
			if got.Accelerator == nil || got.Accelerator.Kind != "nvidia-h100-80gb" || got.HourlyUSD != 3.39 || !got.HourlyPriceKnown {
				t.Fatalf("GPU server cost: %+v %v %v", got.Accelerator, got.HourlyUSD, got.HourlyPriceKnown)
			}
			c, err := conns.GetConnection(t.Context(), "org", "conn_gpu")
			if err != nil || c.Details[provider.GPUDetailStatus] != tc.want {
				t.Fatalf("runtime connection GPU status: %v %v", c, err)
			}
			if kind, gpus := serverGPUFacts(got, nil); gpus != nil && *gpus == 0 {
				t.Fatalf("a GPU server counted as having no GPUs: %q", kind)
			}
		})
	}
}

func TestCPUServerEnrollmentKeepsGPUFree(t *testing.T) {
	s, store, conns, o, v := gpuEnrollment(t, RuntimeHealth{DockerID: "docker-gpu", Architecture: "amd64", CPUs: 2, MemoryBytes: 2 << 30, GPURuntime: provider.GPUStatusNone, CheckedAt: time.Now().UTC()})
	o.Accelerator, o.EstimatedHourlyUSD, o.HourlyPriceKnown = nil, 0.02, true
	if err := s.readyRuntime(t.Context(), o, v); err != nil {
		t.Fatal(err)
	}
	got := store.servers["server_gpu"]
	if got.Health.GPURuntime != provider.GPUStatusNone || got.Health.GPUMessage != "" || got.Accelerator != nil || got.HourlyUSD != 0.02 {
		t.Fatalf("CPU server: %+v", got)
	}
	if c, err := conns.GetConnection(t.Context(), "org", "conn_gpu"); err != nil || c.Details[provider.GPUDetailStatus] != provider.GPUStatusNone {
		t.Fatalf("CPU runtime connection: %v %v", c, err)
	}
}
