//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/security"
)

type catalogFixture struct {
	running    *running
	client     *client
	connection string
	cli        loomctl
	socket     string
	resources  deployments.ResourceSecrets
}

func catalogServer(t *testing.T) catalogFixture {
	t.Helper()
	socket := os.Getenv("LOOMPLANE_TEST_DOCKER_SOCKET")
	if socket == "" {
		t.Skip("set LOOMPLANE_TEST_DOCKER_SOCKET for real CLI deployment and recovery")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	docker := httptest.NewServer(&httputil.ReverseProxy{Director: func(r *http.Request) { r.URL.Scheme = "http"; r.URL.Host = "docker" }, Transport: transport})
	t.Cleanup(docker.Close)
	address, _ := url.Parse(docker.URL)
	_, port, _ := net.SplitHostPort(address.Host)
	number, _ := strconv.Atoi(port)
	// Keep the host effect-fence journal isolated from other local deployments.
	builder := "lp-catalog-host-" + strings.ToLower(rand.Text())
	fixtureCommand(t, "docker", "--host", "unix://"+socket, "run", "-d", "--rm", "--init", "--name", builder,
		"--label", "loomplane.test=catalog-host", "--mount", "type=bind,src="+socket+",dst=/var/run/docker.sock",
		"docker.io/library/python@sha256:bf12ecc0bea3f171d55145dd02c78b5eeb09f30ee2ac9cc2b4c9e4e8f5e30741", "sleep", "3600")
	t.Cleanup(func() { fixtureCommand(t, "docker", "--host", "unix://"+socket, "rm", "-f", builder) })
	credential, settings := fixtureSSH(t, infrastructureState{DaemonPort: number, Builder: builder, HostLoopback: true, DockerHost: "unix://" + socket})
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.Storage.URL = "file://" + t.TempDir()
	running := start(t, cfg)
	t.Cleanup(running.stop)
	c := &client{t: t, base: running.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var connection struct{ ID string }
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "server", "name": "CLI recovery acceptance", "credential": credential, "settings": settings}, &connection)
	var key keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "CLI recovery acceptance", "access": "write"}, &key)
	lc := loomctl{t: t, url: running.srv.URL, key: key.Secret, config: filepath.Join(t.TempDir(), "loomctl.json")}
	db, err := forge.InjectType[*grove.DB](running.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}
	keyBytes, err := security.DeriveKey(cfg.SecretKey, deployments.ResourceVaultKeyLabel)
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEncryptor(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	resources := deployments.NewVaultResourceSecrets(secret.NewService(vaultpg.New(db), encryptor, secret.WithAppID(deployments.ResourceVaultAppID)))
	return catalogFixture{running: running, client: c, connection: connection.ID, cli: lc, socket: socket, resources: resources}
}

func cleanupCatalogProject(t *testing.T, socket, project string) {
	t.Helper()
	// Only this test's newly created project is eligible for cleanup.
	ids := strings.Fields(fixtureCommand(t, "docker", "--host", "unix://"+socket, "ps", "-aq", "--filter", "label=loomplane.project="+project))
	networks := map[string]bool{}
	for _, container := range ids {
		raw := fixtureCommand(t, "docker", "--host", "unix://"+socket, "inspect", container, "--format", `{{json .NetworkSettings.Networks}}`)
		var attached map[string]any
		if err := json.Unmarshal([]byte(raw), &attached); err != nil {
			t.Error(err)
		}
		for network := range attached {
			networks[network] = true
		}
		fixtureCommand(t, "docker", "--host", "unix://"+socket, "rm", "-f", container)
	}
	for _, volume := range strings.Fields(fixtureCommand(t, "docker", "--host", "unix://"+socket, "volume", "ls", "-q", "--filter", "label=loomplane.project="+project)) {
		fixtureCommand(t, "docker", "--host", "unix://"+socket, "volume", "rm", volume)
	}
	for network := range networks {
		fixtureCommand(t, "docker", "--host", "unix://"+socket, "network", "rm", network)
	}
}
