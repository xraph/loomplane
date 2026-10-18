//go:build integration

package server

import (
	"context"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/xraph/trove"
	"github.com/xraph/trove/drivers/localdriver"
	"github.com/xraph/trove/drivers/s3driver"

	"github.com/xraph/loomplane/internal/backupfiles"

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
	labels := map[string]string{owned.LabelOrg: "test", owned.LabelProject: iid.String(), owned.LabelEnvironment: iid.String(), owned.LabelConnection: "test", owned.LabelRevision: "first", LabelDatabase: "postgres"}
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

	var files *trove.Trove
	var err error
	if dsn := os.Getenv("LOOMPLANE_TEST_BACKUP_S3_URL"); dsn != "" {
		drv := s3driver.New()
		if err = drv.Open(ctx, dsn); err != nil {
			t.Fatal(err)
		}
		files, err = trove.Open(drv)
	} else {
		drv := localdriver.New()
		if err = drv.Open(ctx, "file://"+t.TempDir()); err != nil {
			t.Fatal(err)
		}
		files, err = trove.Open(drv)
	}
	if err != nil {
		t.Fatal(err)
	}
	bucket := "lp-postgres-acceptance"
	if err = files.CreateBucket(ctx, bucket); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { files.DeleteBucket(context.Background(), bucket); files.Close(context.Background()) })
	store := &backupfiles.Store{Trove: files, Bucket: bucket}
	object, err := runtime.BackupPostgres(ctx, iid, labels, store, iid.String()+"/database.dump")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { files.Delete(context.Background(), bucket, object.Key) })
	fresh := id.New(id.PrefixInstance)
	isolated := maps.Clone(labels)
	isolated[LabelRecovery] = "acceptance"
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if removeErr := runtime.Remove(cleanup, fresh, isolated); removeErr != nil {
			t.Error(removeErr)
		}
		if removeErr := dockerJSON(cleanup, client, http.MethodDelete, "/volumes/"+VolumeName(fresh, "data"), nil, nil); removeErr != nil {
			t.Error(removeErr)
		}
	})
	if result, applyErr := runtime.Apply(ctx, fresh, spec, isolated); applyErr != nil || len(result.Endpoints) != 0 {
		t.Fatalf("recovery publication: %+v %v", result, applyErr)
	}
	var isolatedNetwork struct{ Internal bool }
	if err = dockerJSON(ctx, client, http.MethodGet, "/networks/"+runtimeNetwork(isolated), nil, &isolatedNetwork); err != nil || !isolatedNetwork.Internal || runtimeNetwork(isolated) == runtimeNetwork(labels) {
		t.Fatalf("recovery network is not isolated: %v", err)
	}
	if err = runtime.ConfigurePostgres(ctx, fresh, isolated, "acceptance-app-password-with-entropy"); err != nil {
		t.Fatal(err)
	}
	bad := object
	bad.SHA256 = strings.Repeat("0", 64)
	if err = runtime.RestorePostgres(ctx, fresh, isolated, store, bad); err == nil {
		t.Fatal("restored corrupt backup")
	}
	if err = runtime.RestorePostgres(ctx, fresh, isolated, store, object); err != nil {
		t.Fatal(err)
	}
	if err = runtime.RestoreProbe(ctx, fresh, isolated); err != nil {
		t.Fatal(err)
	}
	var restored strings.Builder
	if err = ExecOutput(ctx, client, ContainerName(fresh), []string{"psql", "-U", "postgres", "-At", "-v", "ON_ERROR_STOP=1", "-c", "SELECT value FROM persistence_probe"}, &restored); err != nil || strings.TrimSpace(restored.String()) != "retained" {
		t.Fatalf("restore contents: %q %v", restored.String(), err)
	}
	if err = ExecOutput(ctx, client, ContainerName(fresh), []string{"psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1", "-c", "SELECT missing_column FROM persistence_probe"}, io.Discard); err == nil {
		t.Fatal("ignored native tool exit failure")
	}
	if err = runtime.RestorePostgres(ctx, iid, labels, store, object); err == nil {
		t.Fatal("restored over a live service")
	}
	t.Logf("native pg_dump/Trove/pg_restore round trip verified: %d bytes, SHA-256 %s", object.Size, object.SHA256)
	t.Logf("PostgreSQL row survived replacement, container removal and reattachment to %s", retained.Name)
}
