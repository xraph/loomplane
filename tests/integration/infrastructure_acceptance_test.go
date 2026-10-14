//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/realtime"
	mf "github.com/xraph/loomplane/pkg/manifest"
)

type infrastructureState struct {
	RunID               string `json:"run_id"`
	Cluster             string `json:"cluster"`
	Namespace           string `json:"namespace"`
	Builder             string `json:"builder"`
	Registry            string `json:"registry"`
	Kubeconfig          string `json:"kubeconfig"`
	DaemonPort          int    `json:"daemon_port"`
	RegistryPort        int    `json:"registry_port"`
	PrivateRegistryPort int    `json:"private_registry_port"`
	HostAddress         string `json:"host_address"`
	ServicePort         int    `json:"service_port"`
}

func fixtureCommand(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 3*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("fixture command %s: %v\n%s", args[0], err, output)
	}
	return strings.TrimSpace(string(output))
}

// fixtureSSH forwards authenticated SSH channels to the disposable real Docker
// daemon. Neither its Engine API nor its build/rollout responses are mocked.
func fixtureSSH(t *testing.T, state infrastructureState) (string, map[string]string) {
	t.Helper()
	_, userKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	user, err := ssh.NewSignerFromKey(userKey)
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(userKey)
	if err != nil {
		t.Fatal(err)
	}
	private := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}))
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if !bytes.Equal(user.PublicKey().Marshal(), key.Marshal()) {
			return nil, errors.New("fixture key refused")
		}
		return nil, nil
	}}
	server.AddHostKey(host)
	var mu sync.Mutex
	var connections []net.Conn
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, connection := range connections {
			_ = connection.Close()
		}
	})
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			mu.Lock()
			connections = append(connections, connection)
			mu.Unlock()
			go func() {
				remote, channels, requests, handshakeErr := ssh.NewServerConn(connection, server)
				if handshakeErr != nil {
					_ = connection.Close()
					return
				}
				defer remote.Close()
				go ssh.DiscardRequests(requests)
				for request := range channels {
					address := ""
					switch request.ChannelType() {
					case "direct-streamlocal@openssh.com":
						var payload struct {
							Path, Reserved string
							Reserved2      uint32
						}
						if ssh.Unmarshal(request.ExtraData(), &payload) == nil && payload.Path == "/var/run/docker.sock" {
							address = net.JoinHostPort("127.0.0.1", strconv.Itoa(state.DaemonPort))
						}
					case "direct-tcpip":
						var payload struct {
							Host       string
							Port       uint32
							Origin     string
							OriginPort uint32
						}
						if ssh.Unmarshal(request.ExtraData(), &payload) == nil && payload.Host == "127.0.0.1" && payload.Port > 0 && payload.Port <= 65535 {
							channel, requests, channelErr := request.Accept()
							if channelErr != nil {
								continue
							}
							go ssh.DiscardRequests(requests)
							go func() {
								defer channel.Close()
								ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
								defer cancel()
								command := exec.CommandContext(ctx, "docker", "exec", "-i", state.Builder, "nc", "-w", "5", payload.Host, strconv.FormatUint(uint64(payload.Port), 10))
								command.Stdin = channel
								command.Stdout = channel
								command.Stderr = io.Discard
								_ = command.Run()
							}()
							continue
						}
					}
					if address == "" {
						_ = request.Reject(ssh.Prohibited, "fixture destination refused")
						continue
					}
					target, dialErr := (&net.Dialer{}).DialContext(t.Context(), "tcp", address)
					if dialErr != nil {
						_ = request.Reject(ssh.ConnectionFailed, "fixture unavailable")
						continue
					}
					channel, requests, channelErr := request.Accept()
					if channelErr != nil {
						_ = target.Close()
						continue
					}
					go ssh.DiscardRequests(requests)
					go func() { _, _ = io.Copy(channel, target); _ = target.Close(); _ = channel.Close() }()
					go func() { _, _ = io.Copy(target, channel); _ = channel.Close(); _ = target.Close() }()
				}
			}()
		}
	}()
	address, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return private, map[string]string{"host": address, "port": port, "user": "deploy", "host_key": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host.PublicKey()))), "docker_socket": "/var/run/docker.sock", "advertise_host": "127.0.0.1"}
}

func infrastructureSource(t *testing.T, buildGate string) []byte {
	t.Helper()
	root := filepath.Join("..", "acceptance", "infrastructure", "fixture")
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web", "kube", "fail"} {
		files["cmd/"+name+"/main.go"] = files["cmd/api/main.go"]
	}
	for _, name := range []string{"api", "web", "kube", "worker", "fail"} {
		files["cmd/"+name+"/Dockerfile"] = fmt.Sprintf("FROM golang:1.26 AS build\nWORKDIR /src\nCOPY . .\nRUN echo LOOMPLANE_BUILD_MARKER_%s && echo run-%d && echo repeated-output && echo repeated-output && sleep 8\nRUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go mod tidy && CGO_ENABLED=0 go build -p 2 -trimpath -o /out/service ./cmd/%s\nFROM alpine:3.23\nCOPY --from=build /out/service /service\nEXPOSE 8080\nENTRYPOINT [\"/service\"]\n", name, time.Now().UnixNano(), name)
	}
	files["cmd/fail/Dockerfile"] = "FROM alpine:3.23 AS gate\nRUN echo LOOMPLANE_BUILD_MARKER_fail && wget -q -O- " + buildGate + "\n" + strings.Replace(files["cmd/fail/Dockerfile"], "COPY . .", "COPY . .\nCOPY --from=gate /etc/alpine-release /gate-ok", 1)
	return tarball(t, "acceptance-"+exampleSHA, files)
}

func waitInfrastructureDeployment(t *testing.T, c *client, id string) deploymentBody {
	t.Helper()
	deadline := time.Now().Add(20 * time.Minute)
	for {
		var deployment deploymentBody
		c.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+id, nil, &deployment)
		if deployment.Status == "succeeded" || deployment.Status == "failed" || deployment.Status == "canceled" {
			return deployment
		}
		if time.Now().After(deadline) {
			t.Fatalf("acceptance deployment did not finish: %+v", deployment)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func TestAcceptanceInfrastructure(t *testing.T) {
	path := os.Getenv("LOOMPLANE_INFRASTRUCTURE_STATE")
	if path == "" {
		t.Skip("run make acceptance-infrastructure to create the isolated real targets")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state infrastructureState
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(state.RunID, "loomplane-e2e-") || state.Namespace != state.RunID || net.ParseIP(state.HostAddress) == nil {
		t.Fatal("invalid acceptance ownership record")
	}
	credential, settings := fixtureSSH(t, state)
	kubeconfig, err := os.ReadFile(state.Kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Storage.URL = "file://" + t.TempDir()
	var buildAllowed atomic.Bool
	gate := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !buildAllowed.Load() {
			http.Error(w, "acceptance build dependency unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "acceptance build dependency ready")
	}))
	_ = gate.Listener.Close()
	gate.Listener, err = (&net.ListenConfig{}).Listen(t.Context(), "tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	gate.Start()
	defer gate.Close()
	_, gatePort, err := net.SplitHostPort(gate.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	cfg.GitHub.APIURL = fakeGitHubRepos(t, map[string][]byte{"acme/acceptance": infrastructureSource(t, "http://"+net.JoinHostPort(state.HostAddress, gatePort)+"/build-gate")}).URL
	r := start(t, cfg)
	defer func() { r.stop() }()
	browser := startInfrastructureBrowser(t, r.srv.URL, path)
	defer browser.close()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "acceptance@example.test", "password": "Acceptance-Build-logs-42"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Infrastructure acceptance", "slug": "infrastructure-acceptance"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	t.Cleanup(func() {
		ids := fixtureCommand(t, "docker", "exec", state.Builder, "docker", "ps", "-aq", "--filter", "label=loomplane.org="+org.ID)
		if ids != "" {
			fixtureCommand(t, append([]string{"docker", "exec", state.Builder, "docker", "rm", "-f"}, strings.Fields(ids)...)...)
		}
		fixtureCommand(t, "kubectl", "--kubeconfig", state.Kubeconfig, "--context", "kind-"+state.Cluster, "-n", state.Namespace, "delete", "deployment,service,secret,ingress,hpa", "--selector=loomplane.org="+org.ID, "--ignore-not-found")
	})
	var server, cluster, registry struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "server", "name": "Acceptance Docker", "credential": credential, "settings": settings}, &server)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "kubernetes", "name": "Acceptance cluster", "credential": string(kubeconfig), "settings": map[string]string{"context": "kind-" + state.Cluster, "namespace": state.Namespace}}, &cluster)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "registry", "name": "Acceptance registry", "credential": "{}", "settings": map[string]string{"endpoint": "http://localhost:" + strconv.Itoa(state.RegistryPort), "repository": "loomplane"}}, &registry)
	if server.Status != "ready" || cluster.Status != "ready" || registry.Status != "ready" {
		t.Fatalf("real connections are not ready: %s %s %s", server.Status, cluster.Status, registry.Status)
	}
	var project struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Forge infrastructure acceptance", "repository_url": "https://github.com/acme/acceptance"}, &project)
	analyze(t, cfg, c, project.ID)
	base := "/api/v1/projects/" + project.ID
	c.must(http.StatusCreated, http.MethodPost, base+"/variables", map[string]any{"key": "API_TOKEN", "value": "acceptance-runtime-secret", "secret": true, "environment": "production"}, nil)
	manifest := fmt.Sprintf(`version: 1
services:
  api:
    source: go:cmd/api
    provider: server
    target: %s
    public: false
    port: 8080
    health_path: /ready
    build: {context: .}
  web:
    source: go:cmd/web
    provider: server
    target: %s
    port: 8080
    health_path: /ready
    endpoint: {host_port: %d}
    build: {context: .}
  worker:
    source: go:cmd/worker
    provider: server
    target: %s
    build: {context: .}
    needs: [api]
  kube:
    source: go:cmd/kube
    provider: kubernetes
    target: %s
    public: false
    port: 8080
    health_path: /ready
    scale: {min: 2, max: 2}
    build: {context: ., builder: %s, registry: %s}
`, server.ID, server.ID, state.ServicePort, server.ID, cluster.ID, server.ID, registry.ID)
	if _, parseErr := mf.Parse([]byte(manifest)); parseErr != nil {
		t.Fatal(parseErr)
	}
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": manifest}, nil)
	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if !plan.Executable || plan.Status != "ready" {
		t.Fatalf("owned infrastructure plan: %+v", plan)
	}
	browser.checkpoint(t, "targets", map[string]string{"project_id": project.ID, "org_id": org.ID, "plan_id": plan.ID})
	var snapshot struct {
		Cursor string `json:"cursor"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/realtime/snapshot?org_id="+org.ID, nil, &snapshot)
	var deployment deploymentBody
	c.must(http.StatusOK, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": plan.Revision}, nil)
	for deadline := time.Now().Add(time.Minute); deployment.ID == ""; {
		if time.Now().After(deadline) {
			t.Fatal("approved plan did not create a deployment")
		}
		c.do(http.MethodGet, "/api/v1/plans/"+plan.ID+"/deployment", nil, &deployment)
		time.Sleep(100 * time.Millisecond)
	}
	wsFeed := infrastructureWS(t, r, c, org.ID, snapshot.Cursor, deployment.ID)
	sseFeed := infrastructureSSE(t, c, org.ID, snapshot.Cursor, deployment.ID)
	defer wsFeed.close()
	defer sseFeed.close()
	wsLine, replayCursor := waitInfrastructureMarker(t, c, wsFeed, deployment.ID)
	sseLine, _ := waitInfrastructureMarker(t, c, sseFeed, deployment.ID)
	if wsLine.ID != sseLine.ID {
		t.Fatal("WS and SSE delivered different first build records")
	}
	wsFeed.close()
	<-wsFeed.done
	sseFeed.close()
	<-sseFeed.done
	t.Logf("WS and SSE delivered real build output while deployment was running, cursor=%s", replayCursor)
	r2 := start(t, cfg)
	browser.setTarget(t, r2.srv.URL)
	r.stop()
	r = r2
	c.base = r.srv.URL
	replayed := infrastructureSSE(t, c, org.ID, replayCursor, deployment.ID)
	defer replayed.close()
	t.Log("stopped the first API and its deployment worker; second API resumes committed build output")
	deployment = waitInfrastructureDeployment(t, c, deployment.ID)
	if deployment.Status != "succeeded" {
		t.Fatalf("real infrastructure deployment: %+v", deployment)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(state.ServicePort)+"/ready", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("real Forge server readiness: %d", response.StatusCode)
	}
	var logs realtime.LogPage
	c.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+deployment.ID+"/logs?limit=1000", nil, &logs)
	marker := false
	for _, line := range logs.Logs {
		marker = marker || strings.Contains(line.Message, "LOOMPLANE_BUILD_MARKER")
		if strings.Contains(line.Message, "acceptance-runtime-secret") {
			t.Fatal("runtime secret reached retained output")
		}
	}
	if !marker {
		t.Fatal("real Docker build markers were not retained")
	}
	replayed.mu.Lock()
	seen := map[string]bool{}
	for _, frame := range replayed.frames {
		if frame.Event != "build.log" {
			continue
		}
		var line realtime.Log
		if err = json.Unmarshal(frame.Data, &line); err != nil {
			replayed.mu.Unlock()
			t.Fatal(err)
		}
		if seen[line.ID] {
			replayed.mu.Unlock()
			t.Fatalf("replay repeated log %s", line.ID)
		}
		seen[line.ID] = true
	}
	replayed.mu.Unlock()
	if seen[wsLine.ID] || len(seen) == 0 {
		t.Fatal("resume did not continue strictly after its committed cursor")
	}
	t.Logf("second API replay retained %d distinct build lines without replaying the acknowledged marker", len(seen))
	bindings := func() []*planning.Binding {
		var list struct {
			Bindings []*planning.Binding `json:"bindings"`
		}
		c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &list)
		return list.Bindings
	}
	if len(bindings()) != 4 {
		t.Fatalf("want four real bindings, got %d", len(bindings()))
	}
	kubectl := []string{"kubectl", "--kubeconfig", state.Kubeconfig, "--context", "kind-" + state.Cluster, "-n", state.Namespace}
	var workloads struct {
		Items []struct {
			Status struct {
				Ready int `json:"readyReplicas"`
			} `json:"status"`
		} `json:"items"`
	}
	if err = json.Unmarshal([]byte(fixtureCommand(t, append(kubectl, "get", "deployment", "-l", "loomplane.org="+org.ID, "-o", "json")...)), &workloads); err != nil {
		t.Fatal(err)
	}
	if len(workloads.Items) != 1 || workloads.Items[0].Status.Ready != 2 {
		t.Fatalf("real replicas: %+v", workloads)
	}
	for _, binding := range bindings() {
		if binding.Name == "kube" {
			name := "lp-" + strings.ReplaceAll(binding.ExternalID, "_", "-")
			answer := fixtureCommand(t, append(kubectl, "exec", "deployment/"+name, "--", "wget", "-q", "-O-", "http://"+name+":8080/ready")...)
			if !strings.Contains(answer, `"secret_present":true`) {
				t.Fatal("cluster Service did not answer with its runtime secret")
			}
		}
		if binding.Name == "worker" {
			container := "lp-" + strings.ReplaceAll(binding.ExternalID, "_", "-")
			deadline := time.Now().Add(20 * time.Second)
			for {
				output := fixtureCommand(t, "docker", "exec", state.Builder, "docker", "logs", container)
				if strings.Contains(output, "PRIVATE_DEPENDENCY_READY") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("worker could not reach its real private dependency")
				}
				time.Sleep(time.Second)
			}
		}
	}
	t.Log("real private dependency, two ready Kubernetes replicas and four bindings verified")
	browser.checkpoint(t, "rollout", map[string]string{"project_id": project.ID, "org_id": org.ID, "deployment_id": deployment.ID, "plan_id": plan.ID})
	planByDeployment := map[string]string{}
	apply := func(yaml string) deploymentBody {
		c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": yaml}, nil)
		var next planBody
		c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &next)
		waitPlanned(t, c, &next)
		if !next.Executable {
			t.Fatalf("acceptance plan: %+v", next)
		}
		c.must(http.StatusOK, http.MethodPost, "/api/v1/plans/"+next.ID+"/approve", map[string]string{"revision": next.Revision}, nil)
		var d deploymentBody
		for deadline := time.Now().Add(time.Minute); d.ID == ""; {
			if time.Now().After(deadline) {
				t.Fatal("acceptance approval did not start")
			}
			c.do(http.MethodGet, "/api/v1/plans/"+next.ID+"/deployment", nil, &d)
			time.Sleep(100 * time.Millisecond)
		}
		planByDeployment[d.ID] = next.ID
		return waitInfrastructureDeployment(t, c, d.ID)
	}
	desired, parseErr := mf.Parse([]byte(manifest))
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	encode := func() string {
		body, marshalErr := mf.Marshal(desired)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return string(body)
	}
	var kubeImage string
	for _, binding := range bindings() {
		if binding.Name == "kube" {
			kubeImage = binding.Revision
		}
	}
	if kubeImage == "" {
		t.Fatal("Kubernetes source build did not record its image")
	}
	privateTarget, privateImage, privateSecretUID := privateInfrastructureImage(t, c, state, kubeconfig, kubeImage)
	kubeService := desired.Services["kube"]
	kubeService.Target = privateTarget
	kubeService.Build = &mf.Build{Image: privateImage}
	desired.Services["kube"] = kubeService
	privateRollout := apply(encode())
	if privateRollout.Status != "succeeded" {
		t.Fatalf("private cold-node rollout: %+v", privateRollout)
	}
	assertPrivateInfrastructurePull(t, state, privateSecretUID)
	t.Log("cold node pulled a real authenticated image using the reviewed external namespace Secret")
	var secondServer struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "server", "name": "Replacement Docker target", "credential": credential, "settings": settings}, &secondServer)
	var oldWeb *planning.Binding
	for _, binding := range bindings() {
		if binding.Name == "web" {
			oldWeb = binding
		}
	}
	if oldWeb == nil {
		t.Fatal("missing original web binding")
	}
	var webConfig planning.ServiceConfig
	if err = json.Unmarshal(oldWeb.Config, &webConfig); err != nil {
		t.Fatal(err)
	}
	var readiness struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, base+"/variables", map[string]any{"key": "FAIL_READY", "value": "1", "service": webConfig.Service, "environment": "production"}, &readiness)
	web := desired.Services["web"]
	web.Target = secondServer.ID
	private := false
	web.Public, web.Endpoint = &private, nil
	desired.Services["web"] = web
	failedReplacement := apply(encode())
	if failedReplacement.Status != "failed" || !strings.Contains(failedReplacement.Error, "did not pass its readiness check") {
		t.Fatalf("expected real readiness failure: %+v", failedReplacement)
	}
	for _, binding := range bindings() {
		if binding.Name == "web" && binding.ExternalID != oldWeb.ExternalID {
			t.Fatal("failed replacement changed the live binding")
		}
	}
	oldName := "lp-" + strings.ReplaceAll(oldWeb.ExternalID, "_", "-")
	if live := fixtureCommand(t, "docker", "exec", state.Builder, "docker", "inspect", "--format", "{{.State.Running}}", oldName); live != "true" {
		t.Fatal("failed replacement removed the old container")
	}
	t.Log("real failed replacement preserved the old running container and binding")
	browser.checkpoint(t, "replacement-failed", map[string]string{"project_id": project.ID, "org_id": org.ID, "deployment_id": failedReplacement.ID, "plan_id": planByDeployment[failedReplacement.ID]})
	c.must(http.StatusOK, http.MethodPatch, "/api/v1/variables/"+readiness.ID, map[string]string{"value": "0"}, nil)
	c.must(http.StatusOK, http.MethodPost, "/api/v1/deployments/"+failedReplacement.ID+"/retry", nil, nil)
	replacement := waitInfrastructureDeployment(t, c, failedReplacement.ID)
	if replacement.Status != "succeeded" {
		t.Fatalf("replacement retry: %+v", replacement)
	}
	allContainers := fixtureCommand(t, "docker", "exec", state.Builder, "docker", "ps", "-a", "--format", "{{.Names}}")
	if strings.Contains(allContainers, oldName) {
		t.Fatal("ready replacement left its old container behind")
	}
	t.Log("replacement retry became ready before removing the original container")
	desired.Services["fail"] = mf.Service{Source: "go:cmd/fail", Settings: mf.Settings{Provider: "server", Target: server.ID, Public: &private, Port: 8080, HealthPath: "/ready", Build: &mf.Build{Context: "."}}}
	failedBuild := apply(encode())
	if failedBuild.Status != "failed" {
		t.Fatalf("expected real Docker build failure: %+v", failedBuild)
	}
	var failedLogs realtime.LogPage
	c.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+failedBuild.ID+"/logs?limit=1000", nil, &failedLogs)
	if len(failedLogs.Logs) == 0 {
		t.Fatal("failed build did not retain output")
	}
	browser.checkpoint(t, "build-failed", map[string]string{"project_id": project.ID, "org_id": org.ID, "deployment_id": failedBuild.ID, "plan_id": planByDeployment[failedBuild.ID]})
	buildAllowed.Store(true)
	c.must(http.StatusOK, http.MethodPost, "/api/v1/deployments/"+failedBuild.ID+"/retry", nil, nil)
	retried := waitInfrastructureDeployment(t, c, failedBuild.ID)
	if retried.Status != "succeeded" {
		t.Fatalf("real build retry: %+v", retried)
	}
	var retryLogs realtime.LogPage
	c.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+failedBuild.ID+"/logs?limit=1000", nil, &retryLogs)
	attempts := map[string]bool{}
	for _, line := range retryLogs.Logs {
		attempts[line.Attempt] = true
	}
	if len(attempts) < 2 {
		t.Fatal("retry overwrote failed build history")
	}
	t.Logf("real failed build retry succeeded with %d retained attempts", len(attempts))
	infrastructureBoundaries(t, cfg, r, c, org.ID, project.ID, retried.ID)
	removed := apply("version: 1\n")
	if removed.Status != "succeeded" || len(bindings()) != 0 {
		t.Fatalf("product removal: %+v", removed)
	}
	if containers := fixtureCommand(t, "docker", "exec", state.Builder, "docker", "ps", "-aq", "--filter", "label=loomplane.org="+org.ID); containers != "" {
		t.Fatalf("owned containers remain: %s", containers)
	}
	if objects := fixtureCommand(t, append(kubectl, "get", "deployment,service,secret,ingress,hpa", "-l", "loomplane.org="+org.ID, "-o", "name")...); objects != "" {
		t.Fatalf("owned Kubernetes objects remain: %s", objects)
	}
	assertPrivateInfrastructurePull(t, state, privateSecretUID)
	t.Log("product removal deleted all owned containers, workloads, services, secrets and bindings")
	browser.checkpoint(t, "cleanup", map[string]string{"project_id": project.ID, "org_id": org.ID, "deployment_id": removed.ID, "plan_id": planByDeployment[removed.ID]})
	if sentinel := fixtureCommand(t, "docker", "exec", state.Builder, "docker", "inspect", "--format", "{{.State.Running}}", state.Registry); sentinel != "true" {
		t.Fatal("product removal damaged the unrelated registry")
	}
	fixtureCommand(t, append(kubectl, "get", "configmap", "kube-root-ca.crt", "-o", "name")...)
	t.Logf("real SSH/Docker, OCI and Kubernetes rollout passed: run=%s deployment=%s; source fixture replaces GitHub only", state.RunID, deployment.ID)
}
