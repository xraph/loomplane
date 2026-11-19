package cloudinfra

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

// A host that can't connect out to port 22, such as DigitalOcean App
// Platform, sets another port. New servers open it beside 22 before they
// enroll, and enrollment checks them over it.
func TestBootstrapOpensManagedSSHPort(t *testing.T) {
	create := func(t *testing.T, port int, accelerator *provider.Accelerator) (*Operation, []string) {
		t.Helper()
		s := NewService(Deps{EnrollmentURL: "https://enroll.example.com", Secrets: fixedBootstrapSecrets(t, "infraop_fixture"), ManagedSSHPort: port})
		o := &Operation{ID: "infraop_fixture", OrgID: "org", Input: ServerInput{Kind: "create"}, Accelerator: accelerator}
		config, err := s.prepareBootstrap(t.Context(), o)
		if err != nil {
			t.Fatal(err)
		}
		runcmd, _ := bootstrapDocument(t, config)
		return o, runcmd
	}

	t.Run("default", func(t *testing.T) {
		o, runcmd := create(t, 0, nil)
		if o.SSHPort != 22 {
			t.Fatalf("recorded SSH port %d, want 22", o.SSHPort)
		}
		if slices.ContainsFunc(runcmd, func(c string) bool { return strings.Contains(c, "sshd_config") }) {
			t.Fatalf("port 22 changed the SSH config: %q", runcmd)
		}
	})

	for _, tc := range []struct {
		name        string
		accelerator *provider.Accelerator
	}{
		{"cpu", nil},
		{"gpu", &provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: "nvidia", Count: 1, MemoryGiB: 80}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, runcmd := create(t, 2222, tc.accelerator)
			if o.SSHPort != 2222 {
				t.Fatalf("recorded SSH port %d, want 2222", o.SSHPort)
			}
			enroll := slices.IndexFunc(runcmd, func(c string) bool { return strings.Contains(c, "loomplane-enroll.py") })
			script, ok := strings.CutPrefix(runcmd[0], "bash -c ")
			if !ok || enroll <= 0 {
				t.Fatalf("SSH port must open first, before enrollment: %q", runcmd)
			}
			for _, want := range []string{
				"/etc/ssh/sshd_config.d/10-loomplane-port.conf",
				"echo 'Port 22'", // the owner's own SSH stays
				"echo 'Port 2222'",
				"/usr/sbin/sshd -t", // a config sshd refuses is taken out again
				`rm -f "$conf"`,
				"systemctl restart ssh.socket", // Ubuntu's socket-started SSH
				"systemctl restart ssh ",
				"ufw allow 2222/tcp",
			} {
				if !strings.Contains(script, want) {
					t.Fatalf("SSH port script lacks %q:\n%s", want, script)
				}
			}
		})
	}
}

func TestRuntimeSettingsUseRecordedSSHPort(t *testing.T) {
	for _, tc := range []struct {
		recorded int
		want     string
	}{{0, "22"}, {22, "22"}, {2222, "2222"}} {
		settings, err := runtimeSettings(&Operation{PublicIPv4: []string{"203.0.113.7"}, HostKey: gpuHostKey, SSHPort: tc.recorded})
		if err != nil || settings["port"] != tc.want {
			t.Fatalf("recorded %d: port %q, %v", tc.recorded, settings["port"], err)
		}
	}
}

// The runtime connection a create records keeps the port enrollment used,
// so later checks and deploys reach the server the same way.
func TestEnrolledServerKeepsManagedSSHPort(t *testing.T) {
	s, store, conns, o, v := gpuEnrollment(t, RuntimeHealth{DockerID: "docker-gpu", Architecture: "amd64", CPUs: 2, MemoryBytes: 2 << 30, GPURuntime: provider.GPUStatusNone})
	o.Accelerator, o.SSHPort = nil, 2222
	if err := s.readyRuntime(t.Context(), o, v); err != nil {
		t.Fatal(err)
	}
	c, err := conns.GetConnection(t.Context(), "org", "conn_gpu")
	if err != nil || c.Details["port"] != "2222" {
		t.Fatalf("runtime connection port: %v %v", c, err)
	}
	if got := store.servers["server_gpu"]; got.Status != "ready" || got.Message != "" {
		t.Fatalf("enrolled server: %+v", got)
	}
}

// A create that failed marks its server failed, with the reason, instead
// of leaving it starting forever. Other servers are left alone.
func TestFailedCreateMarksItsServerFailed(t *testing.T) {
	store := &gpuServerStore{servers: map[string]*Server{
		"server_new":   {ID: "server_new", CreationOperationID: "infraop_create", Status: "provisioning"},
		"server_other": {ID: "server_other", CreationOperationID: "infraop_other", Status: "provisioning"},
		"server_ready": {ID: "server_ready", CreationOperationID: "infraop_ready", Status: "ready"},
	}}
	s := NewService(Deps{Store: store})
	for _, o := range []*Operation{
		{ID: "infraop_create", ServerID: "server_new", Message: "It never enrolled."},
		{ID: "infraop_create", ServerID: "server_other", Message: "Not this one's create."},
		{ID: "infraop_ready", ServerID: "server_ready", Message: "Already ready."},
		{ID: "infraop_gone", ServerID: "server_gone", Message: "No server row."},
	} {
		if err := s.failCreatedServer(t.Context(), o); err != nil {
			t.Fatalf("%s: %v", o.ServerID, err)
		}
	}
	if got := store.servers["server_new"]; got.Status != "failed" || got.Message != "It never enrolled." {
		t.Fatalf("failed create's server: %+v", got)
	}
	if store.servers["server_other"].Status != "provisioning" || store.servers["server_ready"].Status != "ready" {
		t.Fatalf("other servers changed: %+v %+v", store.servers["server_other"], store.servers["server_ready"])
	}
}

func TestExpiredEnrollmentSaysWhy(t *testing.T) {
	err := error(&enrollmentExpiredError{port: 2222})
	if !errors.Is(err, ErrConflict) {
		t.Fatal("an expired enrollment must stay a conflict")
	}
	message := safeFailure(err)
	for _, want := range []string{"never finished enrolling", "SSH on port 2222", "LOOMPLANE_MANAGED_SSH_PORT", "delete this server and create it again"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message lacks %q: %s", want, message)
		}
	}
}

func TestHealthCheckSaysWhyAServerCantBeChecked(t *testing.T) {
	for status, want := range map[string]string{
		"provisioning": "still starting",
		"failed":       "failed to start",
		"resizing":     "This server is resizing",
	} {
		store := &gpuServerStore{servers: map[string]*Server{"server_1": {ID: "server_1", Status: status}}}
		_, err := NewService(Deps{Store: store}).checkServer(t.Context(), "org", "cloud", "server_1")
		if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v, want a conflict saying %q", status, err, want)
		}
	}
}
