//go:build integration

package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func ingressDocker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("Docker %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func ingressFixture(t *testing.T) (*Client, string) {
	t.Helper()
	if os.Getenv("LOOMPLANE_TEST_INGRESS") != "1" {
		t.Skip("set LOOMPLANE_TEST_INGRESS=1 for disposable real Docker/SSH ingress")
	}
	name := fmt.Sprintf("loomplane-ingress-test-%d", time.Now().UnixNano())
	ingressDocker(t, "run", "-d", "--privileged", "--name", name, "--label", "loomplane.test=domain-ingress", "-e", "DOCKER_TLS_CERTDIR=", "-p", "127.0.0.1::22", "docker:29-dind", "dockerd", "--host=unix:///var/run/docker.sock")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "docker", "rm", "-fv", name).CombinedOutput(); err != nil {
			t.Errorf("remove owned ingress fixture: %v %s", err, out)
		}
	})
	for range 60 {
		if exec.CommandContext(t.Context(), "docker", "exec", name, "docker", "info").Run() == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	ingressDocker(t, "exec", name, "sh", "-c", "apk add --no-cache openssh-server python3 && ssh-keygen -A && passwd -d root && mkdir -p /root/.ssh && chmod 700 /root/.ssh")
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "docker", "exec", "-i", name, "sh", "-c", "cat > /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys && /usr/sbin/sshd -o PermitRootLogin=yes -o PasswordAuthentication=no -o AllowTcpForwarding=yes")
	cmd.Stdin = bytes.NewReader(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("start SSH: %v %s", runErr, out)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	port := strings.TrimPrefix(ingressDocker(t, "port", name, "22/tcp"), "127.0.0.1:")
	cfg, err := ParseConfig(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), map[string]string{"host": "127.0.0.1", "port": port, "user": "root", "host_key": ingressDocker(t, "exec", name, "cat", "/etc/ssh/ssh_host_ed25519_key.pub")})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Details["docker_id"] = ingressDocker(t, "exec", name, "docker", "info", "--format", "{{.ID}}")
	c, err := Dial(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.RequireFence = true
	t.Cleanup(func() { _ = c.Close() })
	return c, name
}

func TestIngressRealCASAndIsolation(t *testing.T) {
	c, host := ingressFixture(t)
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.CommandContext(context.Background(), "docker", "exec", host, "docker", "logs", IngressName("org", "server")).CombinedOutput()
			t.Logf("Caddy diagnostics: %s", out)
		}
	})
	ctx := WithEffectFence(t.Context(), EffectFence{Operation: "org/ingress/server", Generation: 1})
	snapshot, err := EnsureIngress(ctx, c, "org", "server")
	if err != nil {
		t.Fatal(err)
	}
	config := IngressConfig{OrgID: "org", ServerID: "server", Version: "v1", InternalCA: true}
	for _, project := range []string{"a", "b"} {
		// Each app starts on its own environment network. Neither exposes ports.
		envnet := "env-" + project
		ingressDocker(t, "exec", host, "docker", "network", "create", envnet)
		id := ingressDocker(t, "exec", host, "docker", "run", "-d", "--network", envnet, "--name", "app-"+project,
			"--label", "loomplane.org=org", "--label", "loomplane.project="+project, "--label", "loomplane.environment=env-"+project,
			"--label", "loomplane.instance=instance-"+project, "--label", "loomplane.revision=release-"+project, "--label", "loomplane.service=web",
			IngressImage, "sh", "-c", "mkdir -p /tmp/www/admin && echo project-"+project+"-release-"+project+" > /tmp/www/index.html && echo admin-"+project+" > /tmp/www/admin/index.html && exec caddy file-server --listen :8080 --root /tmp/www")
		backend, connectErr := ConnectIngressBackend(ctx, c, "org", "server", IngressBackend{OrgID: "org", ProjectID: project, EnvironmentID: "env-" + project, Service: "web", InstanceID: "instance-" + project, ReleaseID: "release-" + project, ContainerID: id, Port: 8080})
		if connectErr != nil {
			t.Fatal(connectErr)
		}
		config.Routes = append(config.Routes, IngressRoute{ID: "route-" + project, Hostname: project + ".ingress.test", Backend: backend})
	}
	config.Routes[1].BlockedPathPrefixes = []string{"/admin"}
	applied, err := ApplyIngress(ctx, c, snapshot, config)
	if err != nil {
		t.Fatal(err)
	}
	if err = ProbeIngressBackend(ctx, c, config, applied, "route-a"); err != nil {
		t.Fatal(err)
	}
	if err = ProbeIngressBackend(ctx, c, config, applied, "route-b"); err != nil {
		t.Fatal(err)
	}
	conflict := config
	conflict.Version = "stale"
	if _, err = ApplyIngress(ctx, c, snapshot, conflict); err == nil {
		t.Fatal("stale Caddy ETag was accepted")
	}
	read, err := ReadIngress(ctx, c, "org", "server")
	if err != nil || read.ETag != applied.ETag {
		t.Fatalf("CAS conflict changed config: %v", err)
	}
	newctx := WithEffectFence(t.Context(), EffectFence{Operation: "org/ingress/server", Generation: 2})
	if err = c.ArmEffectFence(newctx, EffectFence{Operation: "org/ingress/server", Generation: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyIngress(ctx, c, applied, conflict); err == nil {
		t.Fatal("stale worker generation was accepted")
	}
	for _, target := range []string{config.Routes[1].Backend.Address + ":8080", config.Routes[1].Backend.Address + ":2019"} {
		out, runErr := exec.CommandContext(t.Context(), "docker", "exec", host, "docker", "exec", "app-a", "wget", "-T", "2", "-q", "-O", "-", "http://"+target).CombinedOutput()
		if runErr == nil {
			t.Fatalf("project A reached project B private port %s: %s", target, out)
		}
	}
	proxyIP := ingressDocker(t, "exec", host, "docker", "inspect", "--format", "{{(index .NetworkSettings.Networks \""+IngressNetwork(config.Routes[0].Backend)+"\").IPAddress}}", IngressName("org", "server"))
	if exec.CommandContext(t.Context(), "docker", "exec", host, "docker", "exec", "app-a", "wget", "-T", "2", "-q", "-O", "-", "http://"+proxyIP+":2019/config/").Run() == nil {
		t.Fatal("application reached Caddy admin")
	}
	bad := config
	bad.Routes = append([]IngressRoute(nil), config.Routes...)
	bad.Routes[0].Backend.ReleaseID = "other-release"
	if err = ProbeIngressBackend(newctx, c, bad, applied, "route-a"); err == nil {
		t.Fatal("unrelated release passed destination proof")
	}

	// Same-server preparation changes only the private probe upstream. The old
	// HTTPS app remains available even when the new exact instance fails.
	candidateID := ingressDocker(t, "exec", host, "docker", "run", "-d", "--name", "app-a-next", "--label", "loomplane.org=org", "--label", "loomplane.project=a", "--label", "loomplane.environment=env-a", "--label", "loomplane.instance=next-instance", "--label", "loomplane.revision=next-release", IngressImage, "sh", "-c", "mkdir -p /tmp/www && echo next-instance-next-release > /tmp/www/index.html && exec caddy file-server --listen :8080 --root /tmp/www")
	next := config.Routes[0].Backend
	next.ContainerID = candidateID
	next.InstanceID = "next-instance"
	next.ReleaseID = "next-release"
	next.Address = ""
	next, err = ConnectIngressBackend(newctx, c, "org", "server", next)
	if err != nil {
		t.Fatal(err)
	}
	prepared := config
	prepared.Version = "prepare-next"
	prepared.Routes = append([]IngressRoute(nil), config.Routes...)
	prepared.Routes[0].PreparedBackend = &next
	preparedSnapshot, err := ApplyIngress(newctx, c, applied, prepared)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(ingressDocker(t, "exec", host, "docker", "exec", IngressName("org", "server"), "cat", "/data/caddy/pki/authorities/local/root.crt")))
	public := ingressHTTPS(c, "127.0.0.1:443", "a.ingress.test", roots)
	serving := func(want string) {
		t.Helper()
		response, getErr := ingressGet(t, public, "https://a.ingress.test/")
		if getErr != nil {
			t.Fatal(getErr)
		}
		defer response.Body.Close()
		body, readErr := io.ReadAll(response.Body)
		if readErr != nil || strings.TrimSpace(string(body)) != want {
			t.Fatalf("public backend %q: %v", body, readErr)
		}
	}
	// Route b keeps /admin off its hostname; route a still serves it.
	for hostname, want := range map[string]string{"a.ingress.test": "admin-a", "b.ingress.test": ""} {
		for _, path := range []string{"/admin", "/admin/", "/admin/index.html"} {
			response, getErr := ingressGet(t, ingressHTTPS(c, "127.0.0.1:443", hostname, roots), "https://"+hostname+path)
			if getErr != nil {
				t.Fatal(getErr)
			}
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil || strings.TrimSpace(string(body)) != want || (want == "" && response.StatusCode != 404) {
				t.Fatalf("%s%s: %d %q %v", hostname, path, response.StatusCode, body, readErr)
			}
		}
	}
	// Caddy matches the cleaned, unescaped, case-folded path, so spellings of
	// /admin can't slip past the block.
	for _, path := range []string{"//admin/", "/ADMIN/", "/%61dmin/", "/x/../admin/"} {
		response, getErr := ingressGet(t, ingressHTTPS(c, "127.0.0.1:443", "b.ingress.test", roots), "https://b.ingress.test"+path)
		if getErr != nil {
			t.Fatal(getErr)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 404 || strings.Contains(string(body), "admin-b") {
			t.Fatalf("b.ingress.test%s: %d %q", path, response.StatusCode, body)
		}
	}
	ingressDocker(t, "exec", host, "docker", "stop", candidateID)
	if err = ProbeIngressPreparation(newctx, c, prepared, preparedSnapshot, "route-a"); err == nil {
		t.Fatal("failed candidate accepted")
	}
	serving("project-a-release-a")
	ingressDocker(t, "exec", host, "docker", "start", candidateID)
	if err = ProbeIngressPreparation(newctx, c, prepared, preparedSnapshot, "route-a"); err != nil {
		t.Fatal(err)
	}
	serving("project-a-release-a")
	promoted := config
	promoted.Version = "promote-next"
	promoted.Routes = append([]IngressRoute(nil), config.Routes...)
	promoted.Routes[0].Backend = next
	applied, err = ApplyIngress(newctx, c, preparedSnapshot, promoted)
	if err != nil {
		t.Fatal(err)
	}
	serving("next-instance-next-release")
	if err = DisconnectIngressBackend(newctx, c, promoted, config.Routes[0].Backend); err != nil {
		t.Fatal(err)
	}
	if exec.CommandContext(t.Context(), "docker", "exec", host, "docker", "network", "inspect", IngressNetwork(config.Routes[0].Backend)).Run() == nil {
		t.Fatal("retired instance still connected")
	}
	config = promoted

	wrong := config.Routes[0].Backend
	wrong.InstanceID = "foreign"
	if err = inspectIngressBackend(newctx, c, wrong); err == nil {
		t.Fatal("wrong exact instance accepted")
	}
	wrong = config.Routes[1].Backend
	wrong.Service = "foreign"
	wrong.Address = ""
	if err = inspectIngressBackend(newctx, c, wrong); err == nil {
		t.Fatal("conflicting service label accepted")
	}
	invalid := config
	invalid.Routes = append([]IngressRoute(nil), config.Routes...)
	invalid.Routes[0].Backend.Port = 0
	if _, err = ApplyIngress(newctx, c, applied, invalid); err == nil {
		t.Fatal("invalid config accepted")
	}
	remaining := config
	remaining.Version = "remove-a"
	remaining.Routes = append([]IngressRoute(nil), config.Routes[1:]...)
	last, err := ApplyIngress(newctx, c, applied, remaining)
	if err != nil {
		t.Fatal(err)
	}
	if err = DisconnectIngressBackend(newctx, c, remaining, config.Routes[0].Backend); err != nil {
		t.Fatal(err)
	}
	if exec.CommandContext(t.Context(), "docker", "exec", host, "docker", "network", "inspect", IngressNetwork(config.Routes[0].Backend)).Run() == nil {
		t.Fatal("removed service bridge retained private access")
	}
	if err = ProbeIngressBackend(newctx, c, remaining, last, "route-b"); err != nil {
		t.Fatal("removal broke unrelated route", err)
	}
	t.Log("real pinned SSH/Docker, Caddy native CAS, stale worker rejection, two-project private network isolation, loopback admin, blocked path and exact release request checks passed")
}
