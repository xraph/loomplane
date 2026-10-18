//go:build integration

package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

// Opt in with an explicit disposable Docker daemon socket. All resources are
// labelled and unique; cleanup removes only resources created by this test.
func TestDockerPostgresPersistentLifecycle(t *testing.T) {
	socket := os.Getenv("LOOMPLANE_TEST_DOCKER_SOCKET")
	image := os.Getenv("LOOMPLANE_TEST_POSTGRES_IMAGE")
	if socket == "" || image == "" {
		t.Skip("set LOOMPLANE_TEST_DOCKER_SOCKET and LOOMPLANE_TEST_POSTGRES_IMAGE")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &Client{Config: &Config{Details: map[string]string{"docker_id": "integration"}}, HTTP: &http.Client{Transport: transport}}
	iid := id.New(id.PrefixInstance)
	labels := map[string]string{owned.LabelOrg: "test", owned.LabelProject: iid.String(), owned.LabelEnvironment: iid.String(), owned.LabelConnection: "test", owned.LabelRevision: "first"}
	runtime := Runtime{Session: func(context.Context, map[string]string) (*Client, error) { return client, nil }, Wait: time.Minute}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := runtime.Remove(cleanup, iid, labels); err != nil {
			t.Error(err)
		}
		if err := dockerJSON(cleanup, client, http.MethodDelete, "/volumes/"+VolumeName(iid, "data"), nil, nil); err != nil {
			t.Error(err)
		}
	})
	spec := cpprovider.ServiceSpec{Name: "postgres", Image: image, Env: map[string]string{"POSTGRES_PASSWORD": "acceptance-only-password"}, Volumes: []cpprovider.VolumeSpec{{Name: "data", MountPath: "/var/lib/postgresql/data", Type: "persistent"}}}
	runSQL := func(sql string) string {
		t.Helper()
		var output []byte
		var err error
		for attempt := 0; attempt < 30; attempt++ {
			output, err = exec.CommandContext(ctx, "docker", "--host", "unix://"+socket, "exec", ContainerName(iid), "psql", "-U", "postgres", "-At", "-v", "ON_ERROR_STOP=1", "-c", sql).CombinedOutput()
			if err == nil {
				return strings.TrimSpace(string(output))
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Second):
			}
		}
		t.Fatalf("postgres: %v %s", err, output)
		return ""
	}
	if _, err := runtime.Apply(ctx, iid, spec, labels); err != nil {
		t.Fatal(err)
	}
	runSQL("CREATE TABLE persistence_probe (value text PRIMARY KEY); INSERT INTO persistence_probe VALUES ('retained')")
	labels[owned.LabelRevision] = "replacement"
	if _, err := runtime.Apply(ctx, iid, spec, labels); err != nil {
		t.Fatal(err)
	}
	if value := runSQL("SELECT value FROM persistence_probe"); value != "retained" {
		t.Fatal(value)
	}
	if err := runtime.Remove(ctx, iid, labels); err != nil {
		t.Fatal(err)
	}
	var retained struct{ Name string }
	if err := dockerJSON(ctx, client, http.MethodGet, "/volumes/"+VolumeName(iid, "data"), nil, &retained); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Apply(ctx, iid, spec, labels); err != nil {
		t.Fatal(err)
	}
	if value := runSQL("SELECT value FROM persistence_probe"); value != "retained" {
		t.Fatal(value)
	}
	t.Log(fmt.Sprintf("PostgreSQL row survived replacement, container removal and reattachment to %s", retained.Name))
}
