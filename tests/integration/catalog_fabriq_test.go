//go:build integration

package integration

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/catalogproxy"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/deployments"
	lpclient "github.com/xraph/loomplane/pkg/client"

	"github.com/xraph/loomplane/internal/fabriqloom"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/testfixtures/cluster"
)

// TestAcceptanceFabriqCatalog runs the real pinned stack through the product's
// planner and Docker runtime. All resources belong to a disposable project.
func TestAcceptanceFabriqCatalog(t *testing.T) {
	runFabriqCatalog(t, catalogServer(t), "server")
}

func TestAcceptanceFabriqKubernetes(t *testing.T) {
	path, _, admin, _, _ := cluster.New(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	t.Cleanup(r.stop)
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var connection struct{ ID, Status string }
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{
		"cloud": "kubernetes", "name": "Fabriq disposable cluster", "credential": string(raw),
		"settings": map[string]string{"namespace": admin.Config.Namespace, "storage_class": admin.Config.Details["storage_class"], "architecture": runtime.GOARCH},
	}, &connection)
	if connection.Status != "ready" {
		t.Fatalf("cluster target: %s", connection.Status)
	}
	runFabriqCatalog(t, catalogFixture{running: r, client: c, connection: connection.ID}, "kubernetes")
}

func runFabriqCatalog(t *testing.T, f catalogFixture, provider string) {
	t.Helper()
	for _, kind := range []string{"fabriq", "fabriq-library"} {
		t.Run(kind, func(t *testing.T) {
			f := f
			f.cli.t = t
			c := &client{t: t, base: f.client.base, token: f.client.token, key: f.client.key}
			var project struct{ ID string }
			c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Fabriq acceptance " + kind}, &project)
			if provider == "server" {
				t.Cleanup(func() { cleanupCatalogProject(t, f.socket, project.ID) })
			}
			waitReady(t, c, project.ID)
			base := "/api/v1/projects/" + project.ID
			yaml := fmt.Sprintf("version: 1\nresources:\n  fabriq: {kind: %s, provider: %s, target: %s, uses: {falkordb: {enabled: true}}}\n", kind, provider, f.connection)
			c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": yaml}, nil)
			var plan planBody
			c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]any{"environment": "production", "catalog": true}, &plan)
			waitPlanned(t, c, &plan)
			if !plan.Executable {
				t.Fatalf("Fabriq plan refused: %+v", plan.Problems)
			}
			c.must(http.StatusOK, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": plan.Revision}, nil)
			var deployment deploymentBody
			for deadline := time.Now().Add(time.Minute); deployment.ID == ""; {
				if time.Now().After(deadline) {
					t.Fatal("deployment wasn't created")
				}
				c.do(http.MethodGet, "/api/v1/plans/"+plan.ID+"/deployment", nil, &deployment)
				time.Sleep(100 * time.Millisecond)
			}
			deployment = waitInfrastructureDeployment(t, c, deployment.ID)
			if deployment.Status != "succeeded" {
				raw, _ := json.Marshal(deployment.Steps)
				t.Fatalf("Fabriq deployment %s: %s", deployment.Status, raw)
			}
			var bindings struct{ Bindings []planning.Binding }
			c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &bindings)
			var parent *planning.Binding
			for i := range bindings.Bindings {
				if bindings.Bindings[i].Name == "fabriq" {
					parent = &bindings.Bindings[i]
				}
			}
			if parent == nil {
				t.Fatal("Fabriq binding missing")
			}
			route := base + "/environments/" + parent.EnvironmentID + "/fabriq/fabriq"
			var status fabriqloom.Status
			c.must(http.StatusOK, http.MethodGet, route, nil, &status)
			if !status.Reachable || status.Kind != kind {
				t.Fatalf("Fabriq status: %+v", status)
			}
			if kind == "fabriq-library" {
				c.must(http.StatusUnprocessableEntity, http.MethodGet, route+"/tenants", nil, nil)
				if provider == "server" {
					fabriqLibraryRoundTrip(t, f, parent, bindings.Bindings)
				}
			} else {
				c.must(http.StatusCreated, http.MethodPost, route+"/tenants", map[string]string{"tenant_id": "acme"}, nil)
				c.must(http.StatusOK, http.MethodGet, route+"/tenants", nil, nil)
			}
			if kind == "fabriq" && provider == "server" {
				fabriqHostedRoundTrip(t, f, c, parent, bindings.Bindings, route)
			}
			var rebuild fabriqloom.Rebuild
			c.must(http.StatusAccepted, http.MethodPost, route+"/rebuilds", map[string]string{"projection": "graph", "request_id": kind + "-acceptance-rebuild"}, &rebuild)
			for deadline := time.Now().Add(3 * time.Minute); ; {
				var list struct{ Rebuilds []fabriqloom.Rebuild }
				c.must(http.StatusOK, http.MethodGet, route+"/rebuilds", nil, &list)
				for _, job := range list.Rebuilds {
					if job.ID == rebuild.ID {
						rebuild = job
					}
				}
				if rebuild.Status == "succeeded" {
					break
				}
				if rebuild.Status == "failed" || time.Now().After(deadline) {
					t.Fatalf("graph rebuild: %+v", rebuild)
				}
				time.Sleep(200 * time.Millisecond)
			}
			t.Log("pinned stack deployed, ready and reachable through the scoped API; graph rebuild passed")
		})
	}
}

//go:embed testdata/fabriq-library/main.go.txt
var fabriqLibraryConsumer []byte

// Build a separate pinned consumer module so the control plane never imports
// the Fabriq server's dependencies. Only the published loopback addresses are
// rewritten; all credentials come from the real composition outputs.
func fabriqLibraryRoundTrip(t *testing.T, f catalogFixture, parent *planning.Binding, bindings []planning.Binding) {
	t.Helper()
	dir := t.TempDir()
	for name, data := range map[string][]byte{
		"go.mod":  []byte("module loomplane.test/fabriq-consumer\n\ngo 1.26.0\n\nrequire github.com/xraph/fabriq v1.9.0\n"),
		"main.go": fabriqLibraryConsumer,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	replacements := make([]string, 0, 2*len(bindings))
	for _, binding := range bindings {
		port := ""
		switch binding.Name {
		case "fabriq--postgres":
			port = "5432"
		case "fabriq--redis":
			port = "6379"
		default:
			continue
		}
		iid, err := id.Parse(binding.ExternalID)
		if err != nil {
			t.Fatal(err)
		}
		container := server.ContainerName(iid)
		address := strings.TrimSpace(fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "port", container, port+"/tcp"))
		replacements = append(replacements, container+":"+port, address)
	}
	env := append(os.Environ(), "GOWORK=off", "GOMAXPROCS=2")
	var secrets []string
	for key, field := range map[string]string{"FABRIQ_POSTGRES_DSN": "url", "FABRIQ_REDIS_ADDR": "output:redis-addr", "FABRIQ_REDIS_USERNAME": "output:redis-username", "FABRIQ_REDIS_PASSWORD": "output:redis-password"} {
		value, err := f.resources.Get(t.Context(), deployments.ResourceKey(parent.OrgID, parent.EnvironmentID, parent.Name, field))
		if err != nil {
			t.Fatal(err)
		}
		text := strings.NewReplacer(replacements...).Replace(string(value))
		env = append(env, key+"="+text)
		if text != "" {
			secrets = append(secrets, text, "[redacted]")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", "-mod=mod", ".")
	cmd.Dir, cmd.Env = dir, env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("library consumer: %v: %s", err, strings.NewReplacer(secrets...).Replace(string(output)))
	}
	if !bytes.Contains(output, []byte("library schema and entity survived reopen")) {
		t.Fatal("library consumer did not verify its data")
	}
	t.Log("pinned Go consumer opened issued credentials, defined a dynamic entity and read it after reopening")
}

// fabriqHostedRoundTrip exercises the real key hooks against the deployed server,
// with the product's durable key store and Vault. Release notification is invoked
// explicitly so this test needs no external source repository or image builder.
func fabriqHostedRoundTrip(t *testing.T, f catalogFixture, c *client, parent *planning.Binding, bindings []planning.Binding, route string) {
	t.Helper()
	access, err := forge.InjectType[*deployments.CatalogAccess](f.running.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}
	db, err := forge.InjectType[*grove.DB](f.running.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}
	deps := fabriqloom.HookDeps{Keys: fabriqloom.NewPgStore(pgdriver.Unwrap(db)), Secrets: f.resources, Access: access}
	hooks := fabriqloom.NewHooks(deps)
	ref := deployments.ResourceRef{ProjectID: parent.ProjectID, EnvironmentID: parent.EnvironmentID, Resource: "fabriq"}
	var source, postgres string
	for _, b := range bindings {
		if b.Name != "fabriq--server" && b.Name != "fabriq--postgres" {
			continue
		}
		iid, parseErr := id.Parse(b.ExternalID)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if b.Name == "fabriq--server" {
			source = server.ContainerName(iid)
		} else {
			postgres = server.ContainerName(iid)
		}
	}
	address := strings.TrimSpace(fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "port", source, "8081/tcp"))
	consumer := deployments.Consumer{OrgID: parent.OrgID, ProjectID: parent.ProjectID, EnvironmentID: parent.EnvironmentID, Environment: "production", Resource: "fabriq", Kind: "fabriq", Output: "url", Service: "probe", Options: map[string]string{"tenant": "acme"},
		Channel: func(ctx context.Context, component, port string) (*deployments.ComponentChannel, error) {
			return access.InternalChannel(ctx, parent.OrgID, deployments.ComponentRef{ResourceRef: ref, Component: component, Port: port})
		},
		Address: func(_, _ string) (*url.URL, error) { return url.Parse("http://" + source + ":8081") },
	}
	dsn, err := hooks.BindConsumer(t.Context(), consumer)
	if err != nil {
		t.Fatal(err)
	}
	keyOf := func(dsn string) string {
		u, parseErr := url.Parse(dsn)
		if parseErr != nil {
			t.Fatal("invalid consumer DSN")
		}
		return u.User.Username()
	}
	key := keyOf(dsn)
	request := func(key, method, path string, body any, want int) []byte {
		t.Helper()
		raw, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		req, reqErr := http.NewRequestWithContext(t.Context(), method, "http://"+address+"/admin"+path, bytes.NewReader(raw))
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("X-Tenant-ID", "acme")
		req.Header.Set("X-Fabriq-Api-Version", "1")
		req.Header.Set("Content-Type", "application/json")
		response, doErr := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if doErr != nil {
			t.Fatal(doErr)
		}
		defer response.Body.Close()
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != want {
			t.Fatalf("Fabriq %s %s: HTTP %d, want %d: %s", method, path, response.StatusCode, want, data)
		}
		return data
	}
	request(key, http.MethodPost, "/schema", map[string]any{"type": "acceptance_note", "columns": []map[string]any{{"name": "title", "kind": "string", "required": true}}}, http.StatusCreated)
	request(key, http.MethodPost, "/entities", map[string]any{"type": "acceptance_note", "data": map[string]string{"title": "round-trip"}}, http.StatusCreated)
	if !bytes.Contains(request(key, http.MethodGet, "/entities?type=acceptance_note", nil, http.StatusOK), []byte("round-trip")) {
		t.Fatal("consumer entity missing")
	}
	request(key, http.MethodGet, "/keys", nil, http.StatusForbidden)
	c.must(http.StatusOK, http.MethodPost, route+"/keys/probe/rotate", nil, nil)
	request(key, http.MethodGet, "/entities?type=acceptance_note", nil, http.StatusOK)
	rotated, err := hooks.BindConsumer(t.Context(), consumer)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(planning.ServiceConfig{Env: []planning.EnvLink{{From: "fabriq", Subject: planning.ResourceSubject, Field: "consumer:url"}}})
	released := deployments.Release{OrgID: parent.OrgID, ProjectID: parent.ProjectID, EnvironmentID: parent.EnvironmentID, Binding: &planning.Binding{Name: "probe", Subject: planning.ServiceSubject, Config: raw}}
	if err = fabriqloom.NewObserver(deps).Released(t.Context(), released); err != nil {
		t.Fatal(err)
	}
	request(key, http.MethodGet, "/entities?type=acceptance_note", nil, http.StatusUnauthorized)
	request(keyOf(rotated), http.MethodGet, "/entities?type=acceptance_note", nil, http.StatusOK)
	c.must(http.StatusOK, http.MethodPost, route+"/keys/probe/revoke", nil, nil)
	request(keyOf(rotated), http.MethodGet, "/entities?type=acceptance_note", nil, http.StatusUnauthorized)
	var session catalogproxy.Session
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+parent.ProjectID+"/environments/"+parent.EnvironmentID+"/catalog/fabriq/admin-sessions", nil, &session)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.running.srv.URL+session.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	html, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !bytes.Contains(html, []byte("<html")) {
		t.Fatal("admin proxy didn't return the console")
	}
	// Back up actual Fabriq data and restore it into a separately owned copy.
	var backup lpclient.ServiceBackup
	if err = json.Unmarshal([]byte(f.cli.must("backups", "create", parent.ProjectID, "--resource", "fabriq--postgres", "--request-id", "fabriq-backup", "--wait", "--timeout", "5m", "--json")), &backup); err != nil {
		t.Fatal(err)
	}
	query := func(container, sql string) string {
		t.Helper()
		return strings.TrimSpace(fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "exec", container,
			"psql", "-U", "postgres", "-d", "fabriq__postgres", "-At", "-v", "ON_ERROR_STOP=1", "-c", sql))
	}
	query(postgres, "UPDATE ds_acceptance_note SET title='after-backup'")
	var restored lpclient.ServiceRestore
	if err = json.Unmarshal([]byte(f.cli.must("restores", "create", backup.ID, "--request-id", "fabriq-restore", "--wait", "--timeout", "5m", "--json")), &restored); err != nil || restored.VerifiedAt == nil {
		t.Fatalf("restore wasn't verified: %v", err)
	}
	iid, err := id.Parse(restored.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	restoredContainer := server.ContainerName(iid)
	if restoredContainer == postgres {
		t.Fatal("restore reused the live database")
	}
	if got := query(restoredContainer, "SELECT title FROM ds_acceptance_note"); got != "round-trip" {
		t.Fatalf("restored entity = %q", got)
	}
	if got := query(postgres, "SELECT title FROM ds_acceptance_note"); got != "after-backup" {
		t.Fatalf("restore changed the live entity: %q", got)
	}
	f.cli.must("restores", "delete", restored.ID, "--yes")
	t.Log("consumer write/read, key rotation and revocation, console proxy and isolated Postgres recovery passed")
}
