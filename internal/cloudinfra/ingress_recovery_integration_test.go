//go:build integration

package cloudinfra

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"

	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func recoveryDocker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("Docker %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func recoveryFixture(t *testing.T) (*server.Client, string, []byte) {
	t.Helper()
	if os.Getenv("LOOMPLANE_TEST_INGRESS") != "1" {
		t.Skip("set LOOMPLANE_TEST_INGRESS=1 for disposable real Docker/SSH ingress")
	}
	name := fmt.Sprintf("loomplane-ingress-test-%d", time.Now().UnixNano())
	recoveryDocker(t, "run", "-d", "--privileged", "--name", name, "--label", "loomplane.test=domain-ingress", "-e", "DOCKER_TLS_CERTDIR=", "-p", "127.0.0.1::22", "docker:29-dind", "dockerd", "--host=unix:///var/run/docker.sock")
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
	recoveryDocker(t, "exec", name, "sh", "-c", "apk add --no-cache openssh-server python3 && ssh-keygen -A && passwd -d root && mkdir -p /root/.ssh && chmod 700 /root/.ssh")
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
	port := strings.TrimPrefix(recoveryDocker(t, "port", name, "22/tcp"), "127.0.0.1:")
	cfg, err := server.ParseConfig(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), map[string]string{"host": "127.0.0.1", "port": port, "user": "root", "host_key": recoveryDocker(t, "exec", name, "cat", "/etc/ssh/ssh_host_ed25519_key.pub")})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Details["docker_id"] = recoveryDocker(t, "exec", name, "docker", "info", "--format", "{{.ID}}")
	c, err := server.Dial(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.RequireFence = true
	t.Cleanup(func() { _ = c.Close() })
	return c, name, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// recoveryZone holds the exact test hostnames; none is a wildcard.
var recoveryZone = &Zone{ID: "zone", OrgID: "org", Name: "ingress.test"}

type recoveryIngressStore struct {
	DomainStore
	state     IngressState
	durable   RouteOperation
	failSave  bool
	completed bool
}

func (s *recoveryIngressStore) SaveRouteOperation(_ context.Context, op *RouteOperation) error {
	s.durable = *op
	return nil
}
func (s *recoveryIngressStore) SaveIngress(_ context.Context, _ *RouteOperation, state *IngressState) error {
	if s.failSave {
		s.failSave = false
		return errors.New("lost SaveIngress after remote effect")
	}
	s.state = *state
	return nil
}
func (s *recoveryIngressStore) CompleteHandoff(context.Context, *RouteOperation) error {
	s.completed = true
	return nil
}

func TestIngressCancelledSaveRecoveryAndRotatedCleanup(t *testing.T) {
	c, host, key := recoveryFixture(t)
	ctx := server.WithEffectFence(t.Context(), server.EffectFence{Operation: "org/ingress/server", Generation: 1})
	_, err := server.EnsureIngress(ctx, c, "org", "server")
	if err != nil {
		t.Fatal(err)
	}
	backends := map[string]server.IngressBackend{}
	for _, name := range []string{"a", "b"} {
		container := recoveryDocker(t, "exec", host, "docker", "run", "-d", "--name", "app-"+name, "--label", "loomplane.org=org", "--label", "loomplane.project="+name, "--label", "loomplane.environment=env", "--label", "loomplane.instance=instance-"+name, "--label", "loomplane.revision=release", "--label", "loomplane.service=web", server.LegacyIngressImage, "sh", "-c", "mkdir -p /tmp/www && echo "+name+" > /tmp/www/index.html && exec caddy file-server --listen :8080 --root /tmp/www")
		backend, connectErr := server.ConnectIngressBackend(ctx, c, "org", "server", server.IngressBackend{OrgID: "org", ProjectID: name, EnvironmentID: "env", Service: "web", InstanceID: "instance-" + name, ReleaseID: "release", ContainerID: container, Port: 8080})
		if connectErr != nil {
			t.Fatal(connectErr)
		}
		backends[name] = backend
	}
	routeA := server.IngressRoute{ID: "route-a", Hostname: "a.ingress.test", Backend: backends["a"]}
	routeB := server.IngressRoute{ID: "route-b", Hostname: "b.ingress.test", Backend: backends["b"]}
	route := Route{ID: "route-a", OrgID: "org", ProjectID: "a", Input: RouteInput{Hostname: routeA.Hostname, EnvironmentID: "env", Service: "web", Port: 8080}, Binding: RouteBinding{ServerID: "server", ConnectionID: "runtime", ConnectionGeneration: 1, RuntimeID: c.Config.Details["docker_id"], InstanceID: routeA.Backend.InstanceID, ReleaseID: "release", ContainerID: routeA.Backend.ContainerID}}
	for _, scenario := range []string{"initial", "existing", "newer-known", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			prior, readErr := server.ReadIngress(ctx, c, "org", "server")
			if readErr != nil {
				t.Fatal(readErr)
			}
			base := server.IngressConfig{OrgID: "org", ServerID: "server", Version: "initial"}
			if scenario != "initial" {
				base.Version = "base-" + scenario
				base.InternalCA = true
				base.Routes = []server.IngressRoute{routeB}
			}
			prior, readErr = server.ApplyIngress(ctx, c, prior, base)
			if readErr != nil {
				t.Fatal(readErr)
			}
			persisted := IngressState{Generation: 1, Config: base, Current: prior}
			if scenario == "initial" {
				persisted.Current = nil
			}
			desired := base
			desired.InternalCA = true
			desired.Version = "apply-" + scenario
			desired.Routes = append([]server.IngressRoute{routeA}, base.Routes...)
			apply := &RouteOperation{ID: "apply-" + scenario, Kind: "apply", Route: route, DesiredConfig: &desired, PreviousConfig: prior}
			st := &recoveryIngressStore{state: persisted, failSave: true}
			svc := NewService(Deps{})
			working := persisted
			if applyErr := svc.reconcileRouteConfig(ctx, st, c, apply, &working, recoveryZone); applyErr == nil {
				t.Fatal("lost SaveIngress not injected")
			}
			removal := &RouteOperation{ID: "remove-" + scenario, Kind: "remove", Route: route, CancelledIngress: &IngressTransition{Desired: *apply.DesiredConfig, Previous: apply.PreviousConfig}}
			restarted := st.state
			if scenario == "newer-known" || scenario == "unknown" {
				latest, latestErr := server.ReadIngress(ctx, c, "org", "server")
				if latestErr != nil {
					t.Fatal(latestErr)
				}
				newer := desired
				newer.Version = "other-operation"
				extra := routeB
				extra.ID = "route-c"
				extra.Hostname = "c.ingress.test"
				newer.Routes = append(append([]server.IngressRoute{}, desired.Routes...), extra)
				latest, latestErr = server.ApplyIngress(ctx, c, latest, newer)
				if latestErr != nil {
					t.Fatal(latestErr)
				}
				if scenario == "newer-known" {
					restarted.Current = latest
					restarted.Config = newer
				}
				recoveryErr := svc.recoverCancelledIngress(ctx, st, c, removal, &restarted)
				if scenario == "unknown" {
					after, afterErr := server.ReadIngress(ctx, c, "org", "server")
					if recoveryErr == nil || afterErr != nil || after.ETag != latest.ETag {
						t.Fatal("unknown remote config overwritten", recoveryErr, afterErr)
					}
					return
				}
				if recoveryErr != nil {
					t.Fatal(recoveryErr)
				}
			} else if recoveryErr := svc.recoverCancelledIngress(ctx, st, c, removal, &restarted); recoveryErr != nil {
				t.Fatal(recoveryErr)
			}

			st.state = restarted // the newer known config is already the database state
			st.failSave = true
			if removeErr := svc.reconcileRouteConfig(ctx, st, c, removal, &restarted, recoveryZone); removeErr == nil {
				t.Fatal("lost removal SaveIngress not injected")
			}
			restarted = st.state
			retry := st.durable
			if recoveryErr := svc.recoverCancelledIngress(ctx, st, c, &retry, &restarted); recoveryErr != nil {
				t.Fatal("removal retry reused obsolete cancelled intent", recoveryErr)
			}
			if removeErr := svc.reconcileRouteConfig(ctx, st, c, &retry, &restarted, recoveryZone); removeErr != nil {
				t.Fatal(removeErr)
			}

			want := 0
			if scenario != "initial" {
				want = 1
			}
			if scenario == "newer-known" {
				want = 2
			}
			if len(restarted.Config.Routes) != want {
				t.Fatal("unrelated routes lost")
			}
			for _, r := range restarted.Config.Routes {
				if r.ID == route.ID {
					t.Fatal("cancelled route remained")
				}
			}
		})
	}
	// New SSH credentials with a newer generation may clean the exact accepted
	// daemon, but cannot authorize fresh publication.

	_, rotatedKey, rotationErr := ed25519.GenerateKey(rand.Reader)
	if rotationErr != nil {
		t.Fatal(rotationErr)
	}
	rotatedSigner, rotationErr := ssh.NewSignerFromKey(rotatedKey)
	if rotationErr != nil {
		t.Fatal(rotationErr)
	}
	rotate := exec.CommandContext(t.Context(), "docker", "exec", "-i", host, "sh", "-c", "cat > /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys")
	rotate.Stdin = bytes.NewReader(ssh.MarshalAuthorizedKey(rotatedSigner.PublicKey()))
	if output, rotateErr := rotate.CombinedOutput(); rotateErr != nil {
		t.Fatalf("rotate fixture SSH credential: %v %s", rotateErr, output)
	}
	der, rotationErr := x509.MarshalPKCS8PrivateKey(rotatedKey)
	if rotationErr != nil {
		t.Fatal(rotationErr)
	}

	oldConfig, oldErr := server.ParseConfig(key, c.Config.Details)
	if oldErr != nil {
		t.Fatal(oldErr)
	}
	if oldClient, dialErr := server.Dial(t.Context(), oldConfig); dialErr == nil {
		_ = oldClient.Close()
		t.Fatal("retired SSH key still authenticates")
	}
	key = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	conn := &connections.Connection{OrgID: "org", Cloud: "server", Generation: 2, Details: c.Config.Details}
	svc := NewService(Deps{Connections: rotatedRouteCredential{conn: conn, raw: key}})
	if _, freshErr := svc.routeClient(ctx, route); freshErr == nil {
		t.Fatal("rotated generation authorized new work")
	}
	cleanup, cleanupErr := svc.routeCleanupClient(ctx, route)
	if cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	defer cleanup.Close()
	fence := server.EffectFence{Operation: "org/ingress/server", Generation: 2}
	if cleanupErr = cleanup.ArmEffectFence(ctx, fence); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	ctx = server.WithEffectFence(ctx, fence)
	// Retired A connectivity is the only resource removed. Active B and C remain.
	current, currentErr := server.ReadIngress(ctx, c, "org", "server")
	if currentErr != nil {
		t.Fatal(currentErr)
	}
	active := server.IngressConfig{OrgID: "org", ServerID: "server", Version: "active", InternalCA: true, Routes: []server.IngressRoute{routeB}}
	extra := routeB
	extra.ID = "route-c"
	extra.Hostname = "c.ingress.test"
	active.Routes = append(active.Routes, extra)

	if staleErr := server.DisconnectIngressBackend(ctx, c, active, backends["a"]); staleErr == nil {
		t.Fatal("stale config authorized bridge removal")
	}
	recoveryDocker(t, "exec", host, "docker", "network", "inspect", server.IngressNetwork(backends["a"]))
	current, currentErr = server.ApplyIngress(ctx, c, current, active)
	if currentErr != nil {
		t.Fatal(currentErr)
	}
	activeRoute := route
	activeRoute.ID = "route-b"
	activeRoute.ProjectID = "b"
	activeRoute.Input.Hostname = routeB.Hostname
	activeRoute.Binding.InstanceID = routeB.Backend.InstanceID
	activeRoute.Binding.ContainerID = routeB.Backend.ContainerID
	st := &recoveryIngressStore{}
	release := &RouteOperation{Kind: "release", Route: activeRoute, Source: &route, Generation: 2}
	if !routeConfigMatches(activeRoute, active, false) || routeConfigMatches(route, active, false) {
		t.Fatal("active backend identity mismatch")
	}
	if probeErr := server.ProbeIngressBackend(ctx, c, active, current, activeRoute.ID); probeErr != nil {
		t.Fatal(probeErr)
	}
	if releaseErr := svc.releaseSourceIngress(ctx, st, release, &IngressState{Config: active, Current: current}); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	after, afterErr := server.ReadIngress(ctx, c, "org", "server")
	if afterErr != nil || after.ETag != current.ETag || !st.completed {
		t.Fatal("release republished active config", afterErr)
	}
	if probeErr := server.ProbeIngressBackend(ctx, c, active, after, "route-c"); probeErr != nil {
		t.Fatal("unrelated backend lost", probeErr)
	}
	if exec.CommandContext(t.Context(), "docker", "exec", host, "docker", "network", "inspect", server.IngressNetwork(backends["a"])).Run() == nil {
		t.Fatal("retired bridge retained")
	}
	// Even matching replaced metadata cannot defeat the actual daemon fence.
	wrong := route
	wrong.Binding.RuntimeID = "wrong-physical-daemon"
	conn.Details = map[string]string{}
	for k, v := range c.Config.Details {
		conn.Details[k] = v
	}
	conn.Details["docker_id"] = wrong.Binding.RuntimeID
	rejected, rejectErr := svc.routeCleanupClient(ctx, wrong)
	if rejectErr != nil {
		t.Fatal(rejectErr)
	}
	defer rejected.Close()
	if rejected.ArmEffectFence(ctx, server.EffectFence{Operation: "org/ingress/server", Generation: 3}) == nil {
		t.Fatal("cleanup mutated different actual daemon")
	}
}

func TestIngressAbandonedSameServerPreparation(t *testing.T) {
	c, host, key := recoveryFixture(t)
	ctx := server.WithEffectFence(t.Context(), server.EffectFence{Operation: "org/ingress/server", Generation: 1})
	initial, err := server.EnsureIngress(ctx, c, "org", "server")
	if err != nil {
		t.Fatal(err)
	}
	backends := map[string]server.IngressBackend{}
	for _, name := range []string{"source", "candidate", "unrelated"} {
		project := "project"
		if name == "unrelated" {
			project = "other"
		}
		container := recoveryDocker(t, "exec", host, "docker", "run", "-d", "--name", name, "--label", "loomplane.org=org", "--label", "loomplane.project="+project, "--label", "loomplane.environment=env", "--label", "loomplane.instance="+name, "--label", "loomplane.revision=release", "--label", "loomplane.service=web", server.LegacyIngressImage, "sh", "-c", "mkdir -p /tmp/www && echo "+name+" > /tmp/www/index.html && exec caddy file-server --listen :8080 --root /tmp/www")
		b, connectErr := server.ConnectIngressBackend(ctx, c, "org", "server", server.IngressBackend{OrgID: "org", ProjectID: project, EnvironmentID: "env", Service: "web", InstanceID: name, ReleaseID: "release", ContainerID: container, Port: 8080})
		if connectErr != nil {
			t.Fatal(connectErr)
		}
		backends[name] = b
	}
	source := Route{ID: "route", OrgID: "org", ProjectID: "project", Input: RouteInput{Hostname: "active.ingress.test", EnvironmentID: "env", Service: "web", Port: 8080}, Binding: RouteBinding{ServerID: "server", ConnectionID: "runtime", ConnectionGeneration: 1, RuntimeID: c.Config.Details["docker_id"], InstanceID: "source", ReleaseID: "release", ContainerID: backends["source"].ContainerID}}
	candidate := source
	candidate.Binding.InstanceID = "candidate"
	candidate.Binding.ContainerID = backends["candidate"].ContainerID
	base := server.IngressConfig{OrgID: "org", ServerID: "server", Version: "base", InternalCA: true, Routes: []server.IngressRoute{{ID: "route", Hostname: source.Input.Hostname, Backend: backends["source"]}, {ID: "other-route", Hostname: "other.ingress.test", Backend: backends["unrelated"]}}}
	_, err = server.ApplyIngress(ctx, c, initial, base)
	if err != nil {
		t.Fatal(err)
	}
	// Read the actual public HTTPS endpoint through the pinned SSH host, trusting
	// only this fixture's Caddy CA. This does not use the private candidate probe.
	roots := x509.NewCertPool()
	root := recoveryDocker(t, "exec", host, "docker", "exec", server.IngressName("org", "server"), "cat", "/data/caddy/pki/authorities/local/root.crt")
	if !roots.AppendCertsFromPEM([]byte(root)) {
		t.Fatal("fixture root missing")
	}
	tunnel, tunnelErr := ssh.Dial("tcp", c.Config.Address, &ssh.ClientConfig{User: c.Config.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(c.Config.Signer)}, HostKeyCallback: ssh.FixedHostKey(c.Config.HostKey), HostKeyAlgorithms: []string{c.Config.HostKey.Type()}, Timeout: 10 * time.Second})
	if tunnelErr != nil {
		t.Fatal(tunnelErr)
	}
	defer tunnel.Close()
	transport := &http.Transport{DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
		return tunnel.DialContext(dialCtx, "tcp", "127.0.0.1:443")
	}, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	public := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	assertPublic := func(t *testing.T) {
		t.Helper()
		var last error
		for range 30 {
			req, requestErr := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+source.Input.Hostname+"/", nil)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			resp, getErr := public.Do(req)
			if getErr == nil {
				body, readErr := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if readErr == nil && strings.TrimSpace(string(body)) == "source" {
					return
				}
				t.Fatalf("public backend changed: %s %v", body, readErr)
			}
			last = getErr
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal(last)
	}
	assertPublic(t)
	for _, status := range []string{"failed", "succeeded", "unknown-config"} {
		t.Run(status, func(t *testing.T) {
			recoveryDocker(t, "exec", host, "docker", "exec", "candidate", "sh", "-c", "echo candidate > /tmp/www/index.html")
			candidateBackend := backends["candidate"]
			candidateBackend.Address = ""
			b, connectErr := server.ConnectIngressBackend(ctx, c, "org", "server", candidateBackend)
			if connectErr != nil {
				t.Fatal(connectErr)
			}
			prepared := base
			prepared.Version = "prepare-" + status
			prepared.Routes = append([]server.IngressRoute{}, base.Routes...)
			prepared.Routes[0].PreparedBackend = &b
			before, readErr := server.ReadIngress(ctx, c, "org", "server")
			if readErr != nil {
				t.Fatal(readErr)
			}
			applied, applyErr := server.ApplyIngress(ctx, c, before, prepared)
			if applyErr != nil {
				t.Fatal(applyErr)
			}
			if status == "failed" {
				recoveryDocker(t, "exec", host, "docker", "exec", "candidate", "rm", "/tmp/www/index.html")
				if probeErr := server.ProbeIngressPreparation(ctx, c, prepared, applied, "route"); probeErr == nil {
					t.Fatal("failed candidate passed preparation")
				}
			} else if probeErr := server.ProbeIngressPreparation(ctx, c, prepared, applied, "route"); probeErr != nil {
				t.Fatal(probeErr)
			}
			if probeErr := server.ProbeIngressBackend(ctx, c, prepared, applied, "route"); probeErr == nil {
				t.Fatal("normal probe accepted prepared candidate")
			}
			assertPublic(t)
			state := IngressState{Generation: 1, Config: prepared, Current: applied}
			st := &recoveryIngressStore{state: state, failSave: true}
			svc := NewService(Deps{Connections: rotatedRouteCredential{conn: &connections.Connection{OrgID: "org", Cloud: "server", Generation: 1, Details: c.Config.Details}, raw: key}})
			op := &RouteOperation{ID: "release-" + status, Kind: "release", Route: source, Source: &candidate, Generation: 1, NotBefore: time.Now().Add(time.Minute)}
			if status == "unknown-config" {
				foreign := prepared
				foreign.Version = "unknown"
				foreign.Routes = append([]server.IngressRoute{}, prepared.Routes...)
				foreign.Routes[1].Hostname = "changed.ingress.test"
				actual, foreignErr := server.ApplyIngress(ctx, c, applied, foreign)
				if foreignErr != nil {
					t.Fatal(foreignErr)
				}
				if clearErr := svc.clearInactivePreparation(ctx, st, c, op, &state); clearErr == nil {
					t.Fatal("unknown config accepted")
				}
				after, afterErr := server.ReadIngress(ctx, c, "org", "server")
				if afterErr != nil || after.ETag != actual.ETag {
					t.Fatal("unknown config overwritten", afterErr)
				}
				assertPublic(t)
				return
			}

			wrongSource := candidate
			wrongSource.Binding.ContainerID = "unaccepted-container"
			wrongOp := *op
			wrongOp.Source = &wrongSource
			if clearErr := svc.clearInactivePreparation(ctx, st, c, &wrongOp, &state); !errors.Is(clearErr, ErrConflict) {
				t.Fatal("cleanup accepted wrong inactive instance", clearErr)
			}
			if clearErr := svc.clearInactivePreparation(ctx, st, c, op, &state); clearErr == nil {
				t.Fatal("lost cleanup receipt not injected")
			}
			assertPublic(t)
			restarted := st.state
			retry := st.durable
			if clearErr := svc.clearInactivePreparation(ctx, st, c, &retry, &restarted); clearErr != nil {
				t.Fatal("cleanup receipt recovery failed", clearErr)
			}
			if len(restarted.Config.Routes) != 2 || restarted.Config.Routes[0].PreparedBackend != nil || restarted.Config.Routes[0].Backend != base.Routes[0].Backend || !reflect.DeepEqual(restarted.Config.Routes[1], base.Routes[1]) {
				t.Fatal("cleanup changed public or unrelated route")
			}
			if probeErr := server.ProbeIngressBackend(ctx, c, restarted.Config, restarted.Current, "route"); probeErr != nil {
				t.Fatal(probeErr)
			}
			retry.Route.Verification = RouteVerification{DNS: true, TLS: true, Backend: true}
			if gateErr := svc.releaseVerifiedSource(ctx, st, &retry, &restarted); !errors.Is(gateErr, ErrPending) || st.completed {
				t.Fatal("release skipped drain", gateErr)
			}
			recoveryDocker(t, "exec", host, "docker", "network", "inspect", server.IngressNetwork(b))
			retry.NotBefore = time.Now().Add(-time.Minute)
			if releaseErr := svc.releaseVerifiedSource(ctx, st, &retry, &restarted); releaseErr != nil {
				t.Fatal(releaseErr)
			}
			if !st.completed {
				t.Fatal("handoff hold not released")
			}
			if exec.CommandContext(t.Context(), "docker", "exec", host, "docker", "network", "inspect", server.IngressNetwork(b)).Run() == nil {
				t.Fatal("candidate bridge retained")
			}
			if probeErr := server.ProbeIngressBackend(ctx, c, restarted.Config, restarted.Current, "other-route"); probeErr != nil {
				t.Fatal("unrelated route lost", probeErr)
			}
			assertPublic(t)
		})
	}
}

// Until the DNS-capable image is published, a wildcard route that reaches
// the worker stops before the proxy changes.
func TestWildcardRouteWaitsForPublishedProxy(t *testing.T) {
	c, host, _ := recoveryFixture(t)
	ctx := server.WithEffectFence(t.Context(), server.EffectFence{Operation: "org/ingress/server", Generation: 1})
	snapshot, err := server.EnsureIngress(ctx, c, "org", "server")
	if err != nil {
		t.Fatal(err)
	}
	container := recoveryDocker(t, "exec", host, "docker", "run", "-d", "--name", "app-w", "--label", "loomplane.org=org", "--label", "loomplane.project=w", "--label", "loomplane.environment=env", "--label", "loomplane.instance=instance-w", "--label", "loomplane.revision=release", "--label", "loomplane.service=web", server.LegacyIngressImage, "sh", "-c", "mkdir -p /tmp/www && echo w > /tmp/www/index.html && exec caddy file-server --listen :8080 --root /tmp/www")
	route := Route{ID: "route-w", OrgID: "org", ProjectID: "w", Input: RouteInput{ZoneID: "zone", Hostname: "*.ingress.test", EnvironmentID: "env", Service: "web", Port: 8080}, Binding: RouteBinding{ServerID: "server", ConnectionID: "runtime", ConnectionGeneration: 1, RuntimeID: c.Config.Details["docker_id"], InstanceID: "instance-w", ReleaseID: "release", ContainerID: container}}
	op := &RouteOperation{ID: "apply-w", Kind: "apply", Route: route}
	state := IngressState{Generation: 1, Config: server.IngressConfig{OrgID: "org", ServerID: "server", Version: "initial", Routes: []server.IngressRoute{}}, Current: snapshot}
	st := &recoveryIngressStore{state: state}
	err = NewService(Deps{}).reconcileRouteConfig(ctx, st, c, op, &state, &Zone{ID: "zone", OrgID: "org", Name: "ingress.test", Mode: "managed", Provider: "cloudflare", ChallengeConnectionID: "cf-dns"})
	if !errors.Is(err, ErrWildcardUnsupported) || err.Error() != "Wildcard routes on servers need Loomplane's DNS-capable proxy image, which isn't published yet." {
		t.Fatal(err)
	}
	if op.DesiredConfig == nil || op.DesiredConfig.Routes[0].DNSChallenge == nil || op.DesiredConfig.Routes[0].DNSChallenge.ZoneID != "zone" {
		t.Fatalf("desired route: %+v", op.DesiredConfig)
	}
	after, err := server.ReadIngress(ctx, c, "org", "server")
	if err != nil || after.ContainerID != snapshot.ContainerID || !server.SameIngressConfig(after.Config, snapshot.Config) {
		t.Fatal("the proxy changed", err)
	}
}
