//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// privateInfrastructureImage publishes into a disposable registry, then enables
// real Basic authentication. The cluster owns its pull Secret; Loomplane only
// selects that existing reference. No fixture response stands in for a pull.
func privateInfrastructureImage(t *testing.T, c *client, state infrastructureState, kubeconfig []byte, source string) (connection, image, secretUID string) {
	t.Helper()
	port := strconv.Itoa(state.PrivateRegistryPort)
	localRepository := "localhost:" + port + "/private/kube"
	volume := "private-registry-data"
	fixtureCommand(t, "docker", "exec", state.Builder, "docker", "run", "-d", "--name", "private-registry", "-p", port+":5000", "-v", volume+":/var/lib/registry", "registry:3")
	buildCtx, stopBuild := context.WithTimeout(context.WithoutCancel(t.Context()), time.Minute)
	defer stopBuild()
	build := exec.CommandContext(buildCtx, "docker", "exec", "-i", state.Builder, "docker", "build", "-t", localRepository+":acceptance", "-")
	build.Stdin = strings.NewReader("FROM " + source + "\nRUN echo private-pull-verification > /acceptance-private-image\n")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("prepare real cold-node image: %v %s", err, output)
	}
	fixtureCommand(t, "docker", "exec", state.Builder, "docker", "push", localRepository+":acceptance")
	var digests []string
	if err := json.Unmarshal([]byte(fixtureCommand(t, "docker", "exec", state.Builder, "docker", "image", "inspect", "--format", "{{json .RepoDigests}}", localRepository+":acceptance")), &digests); err != nil {
		t.Fatal(err)
	}
	for _, ref := range digests {
		if strings.HasPrefix(ref, localRepository+"@sha256:") {
			image = strings.Replace(ref, "localhost:"+port, state.Builder+":"+port, 1)
		}
	}
	if image == "" {
		t.Fatal("private registry did not record the published digest")
	}
	var passwordBytes [24]byte
	if _, err := rand.Read(passwordBytes[:]); err != nil {
		t.Fatal(err)
	}
	password := base64.RawURLEncoding.EncodeToString(passwordBytes[:])
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	fixtureCommand(t, "docker", "exec", state.Builder, "mkdir", "-p", "/tmp/private-registry-auth")
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Minute)
	defer cancel()
	write := exec.CommandContext(ctx, "docker", "exec", "-i", state.Builder, "sh", "-c", "cat > /tmp/private-registry-auth/htpasswd")
	write.Stdin = strings.NewReader("acceptance:" + string(hash) + "\n")
	if output, err := write.CombinedOutput(); err != nil {
		t.Fatalf("write disposable registry authentication: %v %s", err, output)
	}
	fixtureCommand(t, "docker", "exec", state.Builder, "docker", "rm", "-f", "private-registry")
	fixtureCommand(t, "docker", "exec", state.Builder, "docker", "run", "-d", "--name", "private-registry", "-p", port+":5000", "-v", volume+":/var/lib/registry", "-v", "/tmp/private-registry-auth:/auth:ro", "-e", "REGISTRY_AUTH=htpasswd", "-e", "REGISTRY_AUTH_HTPASSWD_REALM=acceptance", "-e", "REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd", "-e", "REGISTRY_LOG_FORMATTER=json", "registry:3")
	// The image name and node endpoint agree, so auth is checked on a cold pull.
	node := state.RunID + "-control-plane"
	directory := "/etc/containerd/certs.d/" + state.Builder + ":" + port
	fixtureCommand(t, "docker", "exec", node, "mkdir", "-p", directory)
	write = exec.CommandContext(ctx, "docker", "exec", "-i", node, "cp", "/dev/stdin", directory+"/hosts.toml")
	write.Stdin = strings.NewReader(fmt.Sprintf("[host.\"http://%s:%s\"]\n  capabilities = [\"pull\", \"resolve\"]\n", state.Builder, port))
	if output, err := write.CombinedOutput(); err != nil {
		t.Fatalf("configure disposable private mirror: %v %s", err, output)
	}
	kubectl := []string{"kubectl", "--kubeconfig", state.Kubeconfig, "--context", "kind-" + state.Cluster, "-n", state.Namespace}
	fixtureCommand(t, append(kubectl, "create", "secret", "docker-registry", "acceptance-pull", "--docker-server="+state.Builder+":"+port, "--docker-username=acceptance", "--docker-password="+password)...)
	secretUID = fixtureCommand(t, append(kubectl, "get", "secret", "acceptance-pull", "-o", "jsonpath={.metadata.uid}")...)
	deadline := time.Now().Add(30 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost:"+port+"/v2/", http.NoBody)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		req.SetBasicAuth("acceptance", password)
		response, requestErr := (&http.Client{Timeout: time.Second}).Do(req)
		if requestErr == nil {
			_ = response.Body.Close()
			ready = response.StatusCode == 200
		}
		if ready {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("disposable authenticated registry is not ready")
	}
	nodeImages := fixtureCommand(t, "docker", "exec", node, "crictl", "images", "-o", "json")
	_, digest, _ := strings.Cut(image, "@")
	if strings.Contains(nodeImages, digest) {
		t.Fatal("private image was already cached on the node")
	}
	var registry, cluster connectionBody
	raw, _ := json.Marshal(map[string]string{"username": "acceptance", "password": password})
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "registry", "name": "Acceptance private registry", "credential": string(raw), "settings": map[string]string{"endpoint": "http://localhost:" + port, "repository": "private"}}, &registry)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "kubernetes", "name": "Acceptance cluster with private images", "credential": string(kubeconfig), "settings": map[string]string{"context": "kind-" + state.Cluster, "namespace": state.Namespace, "image_pull_secret": "acceptance-pull"}}, &cluster)
	if registry.Status != "ready" || cluster.Status != "ready" {
		t.Fatalf("private image connections: %s %s", registry.Status, cluster.Status)
	}
	return cluster.ID, image, secretUID
}

func assertPrivateInfrastructurePull(t *testing.T, state infrastructureState, secretUID string) {
	t.Helper()
	logs := fixtureCommand(t, "docker", "exec", state.Builder, "docker", "logs", "private-registry")
	authenticated := false
	for _, line := range strings.Split(logs, "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) == nil && record["auth.user.name"] == "acceptance" && (record["http.request.method"] == "GET" || record["http.request.method"] == "HEAD") {
			uri, _ := record["http.request.uri"].(string)
			authenticated = authenticated || strings.Contains(uri, "/private/kube/manifests/sha256:")
		}
	}
	if !authenticated {
		t.Fatal("private registry did not observe an authenticated digest pull")
	}
	uid := fixtureCommand(t, "kubectl", "--kubeconfig", state.Kubeconfig, "--context", "kind-"+state.Cluster, "-n", state.Namespace, "get", "secret", "acceptance-pull", "-o", "jsonpath={.metadata.uid}")
	if uid != secretUID {
		t.Fatal("the external image pull Secret was adopted or replaced")
	}
}
