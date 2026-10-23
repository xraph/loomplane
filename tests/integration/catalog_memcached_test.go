//go:build integration

package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	lpclient "github.com/xraph/loomplane/pkg/client"
)

func TestAcceptanceMemcachedCatalog(t *testing.T) {
	f := catalogServer(t)
	var project struct{ ID string }
	if err := json.Unmarshal([]byte(f.cli.must("projects", "create", "Memcached acceptance", "--json")), &project); err != nil || project.ID == "" {
		t.Fatalf("project: %v", err)
	}
	t.Cleanup(func() { cleanupCatalogProject(t, f.socket, project.ID) })
	manifestPath := filepath.Join(t.TempDir(), "loomplane.yaml")
	if err := os.WriteFile(manifestPath, []byte("version: 1\nresources: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.cli.must("manifest", "save", project.ID, "-f", manifestPath)
	var plan lpclient.Plan
	if err := json.Unmarshal([]byte(f.cli.must("services", "add", project.ID, "--kind", "memcached", "--name", "cache", "--target", f.connection, "--json")), &plan); err != nil || !plan.Executable {
		t.Fatalf("Memcached plan: %+v %v", plan, err)
	}
	f.cli.must("approve", plan.ID, "--yes")
	var bindings struct{ Bindings []planning.Binding }
	f.client.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project.ID+"/bindings?environment=production", nil, &bindings)
	var container, outputKey, componentURL string
	for _, b := range bindings.Bindings {
		if b.Name == "cache" {
			if b.URL != "" {
				t.Fatal("composition output leaked into its public binding")
			}
			outputKey = deployments.ResourceKey(b.OrgID, b.EnvironmentID, b.Name, deployments.ResourceURL)
		}
		if b.Name == "cache--cache" {
			iid, err := id.Parse(b.ExternalID)
			if err != nil {
				t.Fatal(err)
			}
			container = server.ContainerName(iid)
			componentURL = b.URL
		}
	}
	if container == "" {
		t.Fatalf("component not bound: %+v", bindings)
	}
	if componentURL != "memcached://"+container+":11211" {
		t.Fatalf("native cache became HTTP: %q", componentURL)
	}
	endpoint, err := f.resources.Get(t.Context(), outputKey)
	if err != nil || string(endpoint) != container+":11211" {
		t.Fatalf("stored client endpoint is not private host:port: %q: %v", endpoint, err)
	}
	cacheAddress := func() string {
		t.Helper()
		return fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "inspect", container, "--format", `{{(index (index .NetworkSettings.Ports "11211/tcp") 0).HostIp}}:{{(index (index .NetworkSettings.Ports "11211/tcp") 0).HostPort}}`)
	}
	address := cacheAddress()
	if !strings.HasPrefix(address, "127.0.0.1:") {
		t.Fatalf("cache published publicly: %s", address)
	}
	if volumes := fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "inspect", container, "--format", `{{len .Mounts}}`); volumes != "0" {
		t.Fatal("disposable cache has persistent mounts")
	}
	query := func(command string, lines int) string {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		var conn net.Conn
		var err error
		for {
			conn, err = (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", address)
			if err == nil {
				break
			}
			if time.Now().After(deadline) || t.Context().Err() != nil {
				t.Fatal(err)
			}
			time.Sleep(50 * time.Millisecond)
		}
		defer conn.Close()
		if err = conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err = fmt.Fprint(conn, command); err != nil {
			t.Fatal(err)
		}
		r := bufio.NewReader(conn)
		var out strings.Builder
		for range lines {
			line, readErr := r.ReadString('\n')
			if readErr != nil {
				t.Fatal(readErr)
			}
			out.WriteString(line)
		}
		return out.String()
	}
	if query("set probe 0 0 6\r\nsource\r\n", 1) != "STORED\r\n" || query("get probe\r\n", 3) != "VALUE probe 0 6\r\nsource\r\nEND\r\n" {
		t.Fatal("cache client could not store/read source value")
	}
	fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "restart", container)
	address = cacheAddress()
	if query("get probe\r\n", 1) != "END\r\n" {
		t.Fatal("cache did not empty on restart")
	}
	if query("set probe 0 0 7\r\nrebuilt\r\n", 1) != "STORED\r\n" || query("get probe\r\n", 3) != "VALUE probe 0 7\r\nrebuilt\r\nEND\r\n" {
		t.Fatal("application cannot rebuild cache")
	}
	for _, args := range [][]string{
		{"backups", "create", project.ID, "--resource", "cache", "--request-id", "unsupported"},
		{"backups", "policy", project.ID, "--resource", "cache", "--every-hours", "24"},
	} {
		if _, err := f.cli.run("", args...); err == nil || !strings.Contains(err.Error(), "keeps no data") {
			t.Fatalf("cache must refuse durability for the correct reason: %v: %v", args, err)
		}
	}
	t.Log("Memcached CLI/API/native set, get, restart, rebuild and backup denial passed")
	holdCatalogBrowser(t, f.running.srv.URL, project.ID, f.connection)
}
