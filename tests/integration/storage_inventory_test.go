//go:build integration

package integration

import (
	"context"
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

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/recipes"
	lpclient "github.com/xraph/loomplane/pkg/client"
)

func TestAcceptanceStorageInventory(t *testing.T) {
	socket := os.Getenv("LOOMPLANE_TEST_DOCKER_SOCKET")
	if socket == "" {
		t.Skip("set LOOMPLANE_TEST_DOCKER_SOCKET for real persistent storage inventory")
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
	credential, settings := fixtureSSH(t, infrastructureState{DaemonPort: number})
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	running := start(t, cfg)
	t.Cleanup(running.stop)
	c := &client{t: t, base: running.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var connection struct {
		ID      string
		OrgID   string `json:"org_id"`
		Details map[string]string
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "server", "name": "Retained storage acceptance", "credential": credential, "settings": settings}, &connection)
	var project struct{ ID string }
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Retained storage"}, &project)
	waitReady(t, c, project.ID)
	var environments struct{ Environments []struct{ ID, Name string } }
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project.ID+"/environments", nil, &environments)
	environment := ""
	for _, env := range environments.Environments {
		if env.Name == "production" {
			environment = env.ID
		}
	}
	if environment == "" {
		t.Fatal("production environment is missing")
	}
	iid := id.New(id.PrefixInstance)
	names := []string{server.VolumeName(iid, "data"), server.VolumeName(iid, "archive")}
	container := "storage-inventory-" + iid.String()
	dockerCommand := func(args ...string) string {
		return fixtureCommand(t, append([]string{"docker", "--host", "unix://" + socket}, args...)...)
	}
	t.Cleanup(func() {
		dockerCommand("rm", "-f", container)
		for _, name := range names {
			dockerCommand("volume", "rm", name)
		}
	})
	for i, role := range []string{"data", "archive"} {
		args := []string{"volume", "create"}
		for k, v := range map[string]string{owned.LabelOrg: connection.OrgID, owned.LabelConnection: connection.ID, owned.LabelProject: project.ID, owned.LabelEnvironment: environment, owned.LabelInstance: iid.String(), owned.LabelVolumeRole: role} {
			args = append(args, "--label", k+"="+v)
		}
		dockerCommand(append(args, names[i])...)
	}
	recipe, err := recipes.Postgres("17", connection.Details["architecture"])
	if err != nil {
		t.Fatal(err)
	}
	dockerCommand("run", "--rm", "--entrypoint", "sh", "--mount", "type=volume,source="+names[0]+",target=/data", recipe.Image, "-c", "printf retained-row > /data/probe")
	dockerCommand("create", "--name", container, "--entrypoint", "true", "--mount", "type=volume,source="+names[0]+",target=/data", recipe.Image)
	var key keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "storage viewer", "access": "read"}, &key)
	lc := loomctl{t: t, url: running.srv.URL, key: key.Secret, config: filepath.Join(t.TempDir(), "loomctl.json")}
	check := func(attached bool) {
		t.Helper()
		var volumes []lpclient.StoredVolume
		out := lc.must("storage", connection.ID, "--json")
		if err := json.Unmarshal([]byte(out), &volumes); err != nil || len(volumes) != 2 {
			t.Fatalf("inventory: %s %v", out, err)
		}
		for _, volume := range volumes {
			want := "retained"
			if attached && volume.Role == "data" {
				want = "in-use"
			}
			if volume.Status != want || volume.CapacityBytes != 0 || volume.ProjectID != project.ID {
				t.Fatalf("incorrect storage: %+v", volume)
			}
		}
		if strings.Contains(out, "Mountpoint") || strings.Contains(out, "/var/lib/docker") {
			t.Fatal("host storage path leaked")
		}
	}
	check(true)
	dockerCommand("rm", container)
	check(false)
	if got := dockerCommand("run", "--rm", "--entrypoint", "cat", "--mount", "type=volume,source="+names[0]+",target=/data", recipe.Image, "/data/probe"); got != "retained-row" {
		t.Fatalf("retained contents: %s", got)
	}
	// Keep an attached example for browser acceptance. Stopped containers still own mounts.
	dockerCommand("create", "--name", container, "--entrypoint", "true", "--mount", "type=volume,source="+names[0]+",target=/data", recipe.Image)
	var otherOrg struct{ ID string }
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Other storage", "slug": "other-storage"}, &otherOrg)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": otherOrg.ID}, nil)
	c.must(http.StatusNotFound, http.MethodGet, "/api/v1/connections/"+connection.ID+"/storage", nil, nil)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": connection.OrgID}, nil)
	t.Log("CLI and API inventory preserve data after removal, count stopped attachments, and reject cross-organization reads")
	holdCatalogBrowser(t, running.srv.URL, project.ID, connection.ID)
}
