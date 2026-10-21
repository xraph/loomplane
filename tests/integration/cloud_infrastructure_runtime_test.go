//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/pkg/provider"
)

type localSSHRuntime struct{ port string }

func (r localSSHRuntime) Inspect(ctx context.Context, raw []byte, settings map[string]string, vacant bool) (cloudinfra.RuntimeHealth, error) {
	local := maps.Clone(settings)
	local["port"] = r.port
	return (cloudinfra.SSHRuntime{}).Inspect(ctx, raw, local, vacant)
}
func infraDocker(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "docker", args...)
	raw, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("local Docker fixture %v: %v\n%s", args, err, raw)
	}
	return strings.TrimSpace(string(raw))
}
func infraDockerFile(t *testing.T, container, path string, raw []byte) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "docker", "exec", "-i", container, "sh", "-c", "cat > "+path)
	cmd.Stdin = bytes.NewReader(raw)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write fixture file: %v %s", err, out)
	}
}

func TestCloudInfrastructureRealSSHAndDockerEnrollment(t *testing.T) {
	cloudInfrastructureRuntimeFixture(t, false)
}
func TestServerPoolsEffectiveConstrainedRuntime(t *testing.T) {
	cloudInfrastructureRuntimeFixture(t, true)
}
func cloudInfrastructureRuntimeFixture(t *testing.T, constrained bool) {
	t.Helper()
	if os.Getenv("LOOMPLANE_TEST_CLOUD_RUNTIME") != "1" {
		t.Skip("set LOOMPLANE_TEST_CLOUD_RUNTIME=1 for isolated Docker/SSH verification")
	}
	f := newInfraFixture(t)
	name := fmt.Sprintf("loomplane-cloudinfra-test-%d", time.Now().UnixNano())
	args := []string{"run", "-d", "--privileged", "--name", name, "--label", "loomplane.test=cloud-infrastructure", "-e", "DOCKER_TLS_CERTDIR=", "-p", "127.0.0.1::22"}
	if constrained {
		args = append(args, "--cpus=1", "--memory=768m")
	}
	args = append(args, "docker:29-dind", "dockerd", "--host=unix:///var/run/docker.sock")
	infraDocker(t, args...)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "docker", "rm", "-fv", name).CombinedOutput(); err != nil {
			t.Errorf("remove owned fixture %s: %v %s", name, err, out)
		}
		if exec.CommandContext(ctx, "docker", "inspect", name).Run() == nil {
			t.Errorf("owned fixture %s remains", name)
		}
	})
	for attempt := 0; attempt < 50; attempt++ {
		if exec.CommandContext(t.Context(), "docker", "exec", name, "docker", "info").Run() == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	infraDocker(t, "exec", name, "sh", "-c", "apk add --no-cache openssh-server iproute2 python3 && ssh-keygen -A && adduser -D -G docker loomplane && passwd -d loomplane && mkdir -p /home/loomplane/.ssh && chmod 700 /home/loomplane/.ssh")
	port := strings.TrimPrefix(infraDocker(t, "port", name, "22/tcp"), "127.0.0.1:")
	f.svc.Runtime = localSSHRuntime{port: port}
	var bootstrap map[string]any
	f.provider.beforeCreate = func(spec provider.ServerSpec) {
		if err := json.Unmarshal([]byte(strings.TrimPrefix(spec.UserData, "#cloud-config\n")), &bootstrap); err != nil {
			t.Fatal(err)
		}
	}
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in cloudinfra.Enrollment
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			w.WriteHeader(400)
			return
		}
		parts := strings.Split(r.URL.Path, "/")
		id := parts[len(parts)-1]
		if err := f.svc.Enroll(r.Context(), "org", id, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), in); err != nil {
			w.WriteHeader(409)
			return
		}
		w.WriteHeader(204)
	}))
	defer callback.Close()
	f.svc.EnrollmentURL = callback.URL
	o := f.create(t)
	o = f.reconcile(t, o)
	users := bootstrap["users"].([]any)
	user := users[1].(map[string]any)
	publicKey := user["ssh_authorized_keys"].([]any)[0].(string)
	infraDockerFile(t, name, "/home/loomplane/.ssh/authorized_keys", []byte(publicKey+"\n"))
	infraDocker(t, "exec", name, "sh", "-c", "chmod 600 /home/loomplane/.ssh/authorized_keys && chown -R loomplane:docker /home/loomplane/.ssh && /usr/sbin/sshd -o PasswordAuthentication=no -o AllowUsers=loomplane -o AllowTcpForwarding=yes")
	hostKey := infraDocker(t, "exec", name, "cat", "/etc/ssh/ssh_host_ed25519_key.pub")
	raw, err := f.secrets.Reveal(t.Context(), "org", "infrastructure/"+o.ID)
	if err != nil {
		t.Fatal(err)
	}
	var secret struct {
		PrivateKey string `json:"private_key"`
		Token      string `json:"token"`
	}
	if err = json.Unmarshal(raw, &secret); err != nil {
		t.Fatal(err)
	}
	send := func(token, key string) int {
		t.Helper()
		body, _ := json.Marshal(cloudinfra.Enrollment{HostKey: key})
		request, requestErr := http.NewRequestWithContext(t.Context(), http.MethodPost, callback.URL+"/api/v1/infrastructure/enroll/org/"+o.ID, bytes.NewReader(body))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if code := send("wrong", hostKey); code != 409 {
		t.Fatal("invalid token accepted")
	}
	_, wrongSettings := fixtureSSH(t, infrastructureState{})
	if code := send(secret.Token, wrongSettings["host_key"]); code != 409 {
		t.Fatal("unpossessed host key accepted")
	}
	if code := send(secret.Token, hostKey); code != 204 {
		t.Fatalf("real pinned SSH/Docker enrollment returned %d", code)
	}
	if code := send(secret.Token, hostKey); code != 409 {
		t.Fatal("single-use enrollment replay accepted")
	}
	o = f.reconcile(t, o)
	if o.Status != "succeeded" {
		t.Fatalf("real runtime never ready: %+v", o)
	}
	v, err := f.store.GetServer(t.Context(), "org", "cloud", o.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if constrained {
		if v.Health.EffectiveCPUMillis <= 0 || v.Health.EffectiveCPUMillis > 1000 || v.Health.EffectiveMemoryBytes <= 0 || v.Health.EffectiveMemoryBytes > 768<<20 {
			t.Fatalf("constrained effective capacity: %+v", v.Health)
		}
		pool := &cloudinfra.Pool{ID: "constrained-pool", OrgID: "org", CloudID: "cloud", Name: "Constrained", Revision: 1, Members: []cloudinfra.PoolMember{{ServerID: v.ID, CPUMillis: 2000, MemoryBytes: 512 << 20}}}
		if err = f.store.SavePool(t.Context(), pool, 0); err == nil {
			t.Fatal("outer host CPU was reservable")
		}
		pool.Members[0].CPUMillis = 500
		if err = f.store.SavePool(t.Context(), pool, 0); err != nil {
			t.Fatal(err)
		}
	}
	if v.Health.DockerID == "" || v.Health.CPUs < 1 || v.Health.MemoryBytes < 1 {
		t.Fatalf("capacity unverified: %+v", v.Health)
	}
	runtimeConn, err := f.connections.GetConnection(t.Context(), "org", v.RuntimeConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	settings := maps.Clone(runtimeConn.Details)
	settings["port"] = port
	cfg, err := server.ParseConfig([]byte(secret.PrivateKey), settings)
	if err != nil {
		t.Fatal(err)
	}
	client, err := server.Dial(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if constrained {
		cfg.Details["docker_id"] = v.Health.DockerID
		verifyRuntimeEffectFence(t, name, cfg)
	}
	// Bind a real conflicting listener inside this host, then refuse takeover.
	infraDocker(t, "exec", "-d", name, "sh", "-c", "nc -lk -p 80 >/dev/null")
	for attempt := 0; attempt < 20; attempt++ {
		if err = client.CheckVacant(t.Context()); err != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err == nil {
		t.Fatal("existing ingress listener accepted")
	}
	settings["host_key"] = wrongSettings["host_key"]
	cfg, err = server.ParseConfig([]byte(secret.PrivateKey), settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.Dial(t.Context(), cfg); !errors.Is(err, server.ErrRejected) {
		t.Fatalf("unknown host key: %v", err)
	}
	t.Logf("Verified isolated host %s: Docker identity, %d CPUs, %d bytes, enrollment replay refusal and listener conflict", name, v.Health.CPUs, v.Health.MemoryBytes)
}

func verifyRuntimeEffectFence(t *testing.T, host string, cfg *server.Config) {
	t.Helper()
	infraDocker(t, "exec", host, "sh", "-c", "tar -C / -cf /tmp/fence-rootfs.tar bin lib && docker import /tmp/fence-rootfs.tar fence-fixture >/dev/null && docker run -d --name fence-sleeper fence-fixture /bin/busybox sleep 300")
	old, err := server.Dial(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	first := server.EffectFence{Operation: "fixture/plan/api", Generation: 1}
	if err = old.ArmEffectFence(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		response, e := old.Request(server.WithEffectFence(t.Context(), first), http.MethodPost, "/containers/fence-sleeper/wait?condition=not-running", nil)
		if e == nil {
			_, e = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		finished <- e
	}()
	for attempt := 0; attempt < 100; attempt++ {
		state := infraDocker(t, "exec", host, "sh", "-c", "cat /var/tmp/loomplane-fence-*/state")
		if strings.Contains(state, `"dirty": true`) {
			break
		}
		if attempt == 99 {
			t.Fatal("mutation never entered host journal")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := server.Dial(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	advanced := make(chan error, 1)
	second := server.EffectFence{Operation: first.Operation, Generation: 2}
	go func() { advanced <- next.ArmEffectFence(t.Context(), second) }()
	select {
	case err = <-advanced:
		t.Fatalf("takeover passed live mutation after caller loss: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	infraDocker(t, "exec", host, "docker", "stop", "-t", "0", "fence-sleeper")
	select {
	case err = <-advanced:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("completed mutation did not permit takeover")
	}
	response, err := next.Request(server.WithEffectFence(t.Context(), first), http.MethodPost, "/networks/create", strings.NewReader(`{"Name":"stale-fence-network"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatal("delayed old generation mutated runtime", response.StatusCode)
	}
	if err = next.ArmEffectFence(t.Context(), server.EffectFence{Operation: "fixture/other/api", Generation: 1}); err != nil {
		t.Fatal("unrelated operation was fenced", err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("disconnected caller did not return")
	}
	// An uncatchably terminated helper leaves an unknown Docker outcome. Even
	// after the container stops, another operation cannot clear the dirty journal.
	infraDocker(t, "exec", host, "docker", "start", "fence-sleeper")
	unknown := make(chan error, 1)
	go func() {
		response, requestErr := next.Request(server.WithEffectFence(t.Context(), second), http.MethodPost, "/containers/fence-sleeper/wait?condition=not-running", nil)
		if requestErr == nil {
			_, requestErr = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		unknown <- requestErr
	}()
	for attempt := 0; attempt < 100; attempt++ {
		state := infraDocker(t, "exec", host, "sh", "-c", "cat /var/tmp/loomplane-fence-*/state")
		if strings.Contains(state, `"dirty": true`) {
			break
		}
		if attempt == 99 {
			t.Fatal("unknown mutation never entered host journal")
		}
		time.Sleep(20 * time.Millisecond)
	}
	infraDocker(t, "exec", host, "killall", "-9", "python3")
	infraDocker(t, "exec", host, "docker", "stop", "-t", "0", "fence-sleeper")
	if err = next.ArmEffectFence(t.Context(), server.EffectFence{Operation: "fixture/other/api", Generation: 2}); err == nil {
		t.Fatal("unknown helper outcome allowed takeover")
	}
	select {
	case <-unknown:
	case <-time.After(time.Second):
		t.Fatal("terminated helper caller did not return")
	}
	t.Log("Runtime takeover waited for Docker after SSH caller loss; stale requests and dirty-journal takeover refused")
}
