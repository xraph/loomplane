//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
)

func TestAcceptanceCatalogPostgres(t *testing.T) {
	socket := os.Getenv("LOOMPLANE_TEST_DOCKER_SOCKET")
	if socket == "" {
		t.Skip("set LOOMPLANE_TEST_DOCKER_SOCKET for real catalog deployment")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	proxy := &httputil.ReverseProxy{Director: func(r *http.Request) { r.URL.Scheme = "http"; r.URL.Host = "docker" }, Transport: transport, FlushInterval: -1}
	docker := httptest.NewServer(proxy)
	defer docker.Close()
	address, _ := url.Parse(docker.URL)
	_, port, _ := net.SplitHostPort(address.Host)
	number, _ := strconv.Atoi(port)
	credential, settings := fixtureSSH(t, infrastructureState{DaemonPort: number})
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.Storage.URL = "file://" + t.TempDir()
	running := start(t, cfg)
	defer func() { running.stop() }()
	c := &client{t: t, base: running.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var connection struct{ ID, Status string }
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "server", "name": "Catalog acceptance", "credential": credential, "settings": settings}, &connection)
	if connection.Status != "ready" {
		t.Fatal(connection.Status)
	}
	var project struct{ ID string }
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Standalone databases"}, &project)
	t.Cleanup(func() {
		// Only this test's project is eligible, even if deployment failed before binding.
		ids := strings.Fields(fixtureCommand(t, "docker", "--host", "unix://"+socket, "ps", "-aq", "--filter", "label=loomplane.project="+project.ID))
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
		volumes := strings.Fields(fixtureCommand(t, "docker", "--host", "unix://"+socket, "volume", "ls", "-q", "--filter", "label=loomplane.project="+project.ID))
		for _, volume := range volumes {
			fixtureCommand(t, "docker", "--host", "unix://"+socket, "volume", "rm", volume)
		}
		for network := range networks {
			fixtureCommand(t, "docker", "--host", "unix://"+socket, "network", "rm", network)
		}
	})
	waitReady(t, c, project.ID)
	base := "/api/v1/projects/" + project.ID
	yaml := fmt.Sprintf("version: 1\nresources:\n  database: {kind: postgres, provider: server, version: '17', target: %s}\n", connection.ID)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": yaml}, nil)
	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]any{"environment": "production", "catalog": true}, &plan)
	waitPlanned(t, c, &plan)
	if !plan.Executable || len(plan.Sources) != 0 {
		t.Fatalf("catalog plan: %+v", plan)
	}
	c.must(http.StatusOK, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": plan.Revision}, nil)
	deadline := time.Now().Add(3 * time.Minute)
	var deployment deploymentBody
	for {
		status := c.do(http.MethodGet, "/api/v1/plans/"+plan.ID+"/deployment", nil, &deployment)
		if status == http.StatusOK && deployment.Status != "running" && deployment.Status != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("deployment did not settle: %+v", deployment)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if deployment.Status != "succeeded" {
		t.Fatalf("catalog deployment: %+v", deployment)
	}
	var bindings struct {
		Bindings []planning.Binding `json:"bindings"`
	}
	c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &bindings)
	if len(bindings.Bindings) != 1 {
		t.Fatalf("bindings: %+v", bindings)
	}
	binding := bindings.Bindings[0]
	iid, err := id.Parse(binding.ExternalID)
	if err != nil {
		t.Fatal(err)
	}

	output := fixtureCommand(t, "docker", "--host", "unix://"+socket, "exec", server.ContainerName(iid), "psql", "-U", "app", "-d", "database", "-At", "-v", "ON_ERROR_STOP=1", "-c", "CREATE TABLE catalog_probe(value text); INSERT INTO catalog_probe VALUES ('from-api'); SELECT value FROM catalog_probe")
	if !strings.Contains(output, "from-api") {
		t.Fatal(output)
	}
	privileges := fixtureCommand(t, "docker", "--host", "unix://"+socket, "exec", server.ContainerName(iid), "psql", "-U", "postgres", "-d", "database", "-At", "-c", "SELECT rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication FROM pg_roles WHERE rolname='app'")
	if privileges != "f" {
		t.Fatalf("application role is privileged: %s", privileges)
	}

	var backup, replayed recovery.Backup
	request := map[string]string{"environment": "production", "resource": "database", "request_id": "acceptance-backup-1"}
	c.must(http.StatusAccepted, http.MethodPost, base+"/service-backups", request, &backup)
	c.must(http.StatusAccepted, http.MethodPost, base+"/service-backups", request, &replayed)
	if backup.ID != replayed.ID {
		t.Fatal("duplicate request created another recovery point")
	}
	deadline = time.Now().Add(time.Minute)
	for backup.Status == "queued" || backup.Status == "running" {
		if time.Now().After(deadline) {
			t.Fatalf("backup did not finish: %+v", backup)
		}
		time.Sleep(100 * time.Millisecond)
		c.must(http.StatusOK, http.MethodGet, "/api/v1/service-backups/"+backup.ID, nil, &backup)
	}
	if backup.Status != "succeeded" || backup.Bytes == 0 || backup.RestoredAt != nil {
		t.Fatalf("backup evidence: %+v", backup)
	}
	var policy recovery.Policy
	c.must(http.StatusOK, http.MethodPut, base+"/backup-policies", map[string]any{"environment": "production", "resource": "database", "every_hours": 24, "enabled": true}, &policy)
	db, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var destinationID string
	if err = db.QueryRow(t.Context(), "SELECT object->>'destination_id' FROM loomplane_service_backups WHERE id=$1 AND project_id=$2", backup.ID, project.ID).Scan(&destinationID); err != nil || len(destinationID) != 64 {
		t.Fatalf("backup destination was not persisted: %q %v", destinationID, err)
	}
	if _, err = db.Exec(t.Context(), "UPDATE loomplane_backup_policies SET next_at=now()-interval '1 second' WHERE id=$1", policy.ID); err != nil {
		t.Fatal(err)
	}
	running.stop()
	running = start(t, cfg)
	c.base = running.srv.URL
	var afterRestart recovery.Backup
	c.must(http.StatusOK, http.MethodGet, "/api/v1/service-backups/"+backup.ID, nil, &afterRestart)
	if afterRestart.Status != "succeeded" || afterRestart.SHA256 != backup.SHA256 {
		t.Fatalf("lost recovery point after restart: %+v", afterRestart)
	}
	var persistedDestination string
	if err = db.QueryRow(t.Context(), "SELECT object->>'destination_id' FROM loomplane_service_backups WHERE id=$1 AND project_id=$2", backup.ID, project.ID).Scan(&persistedDestination); err != nil || persistedDestination != destinationID {
		t.Fatalf("restart lost the backup destination: %q %v", persistedDestination, err)
	}

	// Delete the source row after backup. The recovered copy must contain it,
	// while the live source must remain unchanged by the restore workflow.
	fixtureCommand(t, "docker", "--host", "unix://"+socket, "exec", server.ContainerName(iid), "psql", "-U", "app", "-d", "database", "-c", "DELETE FROM catalog_probe")
	var restored, repeated recovery.Restore
	restoreRequest := map[string]string{"request_id": "acceptance-restore-1"}
	c.must(http.StatusAccepted, http.MethodPost, "/api/v1/service-backups/"+backup.ID+"/restore", restoreRequest, &restored)
	c.must(http.StatusAccepted, http.MethodPost, "/api/v1/service-backups/"+backup.ID+"/restore", restoreRequest, &repeated)
	if restored.ID != repeated.ID {
		t.Fatal("restore request is not idempotent")
	}
	deadline = time.Now().Add(3 * time.Minute)
	for restored.Status == "queued" || restored.Status == "running" {
		if time.Now().After(deadline) {
			t.Fatalf("restore did not settle: %+v", restored)
		}
		time.Sleep(100 * time.Millisecond)
		c.must(http.StatusOK, http.MethodGet, "/api/v1/service-restores/"+restored.ID, nil, &restored)
	}
	if restored.Status != "succeeded" || restored.VerifiedAt == nil {
		t.Fatalf("restore evidence: %+v", restored)
	}
	cloneID, err := id.Parse(restored.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	recovered := fixtureCommand(t, "docker", "--host", "unix://"+socket, "exec", server.ContainerName(cloneID), "psql", "-U", "app", "-d", "database", "-At", "-c", "SELECT value FROM catalog_probe")
	if recovered != "from-api" {
		t.Fatalf("restored row: %s", recovered)
	}
	live := fixtureCommand(t, "docker", "--host", "unix://"+socket, "exec", server.ContainerName(iid), "psql", "-U", "app", "-d", "database", "-At", "-c", "SELECT count(*) FROM catalog_probe")
	if live != "0" {
		t.Fatalf("restore modified the live source: %s", live)
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/service-backups/"+backup.ID, nil, &afterRestart)
	if afterRestart.RestoredAt == nil {
		t.Fatal("successful restore did not record backup evidence")
	}
	c.must(http.StatusNoContent, http.MethodDelete, "/api/v1/service-restores/"+restored.ID, nil, nil)
	c.must(http.StatusNoContent, http.MethodDelete, "/api/v1/service-restores/"+restored.ID, nil, nil)
	c.must(http.StatusOK, http.MethodGet, "/api/v1/service-restores/"+restored.ID, nil, &restored)
	if restored.Status != "deleted" || restored.VerifiedAt == nil {
		t.Fatalf("cleanup lost restore evidence: %+v", restored)
	}
	remaining := fixtureCommand(t, "docker", "--host", "unix://"+socket, "volume", "ls", "-q", "--filter", "label=loomplane.instance="+cloneID.String())
	if remaining != "" {
		t.Fatalf("recovery volume survived cleanup: %s", remaining)
	}

	// Let the real persisted Dispatch cron request the due backup after restart.
	var scheduled struct {
		Backups []recovery.Backup `json:"backups"`
	}
	deadline = time.Now().Add(90 * time.Second)
	for {
		c.must(http.StatusOK, http.MethodGet, base+"/service-backups", nil, &scheduled)
		completed := false
		for _, point := range scheduled.Backups {
			if point.ID != backup.ID && point.Status == "succeeded" {
				completed = true
			}
		}
		if completed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("persisted schedule did not create a backup: %+v", scheduled)
		}
		time.Sleep(200 * time.Millisecond)
	}
	var policies struct {
		Policies []recovery.Policy `json:"policies"`
	}
	c.must(http.StatusOK, http.MethodGet, base+"/backup-policies", nil, &policies)
	if len(policies.Policies) != 1 || policies.Policies[0].LastError != "" || !policies.Policies[0].NextAt.After(time.Now()) {
		t.Fatalf("schedule advancement: %+v", policies)
	}
	c.must(http.StatusOK, http.MethodPut, base+"/backup-policies", map[string]any{"environment": "production", "resource": "database", "every_hours": 24, "enabled": false}, &policy)
	if policy.Enabled {
		t.Fatal("schedule remained enabled")
	}

	var retirementCandidate string
	for _, point := range scheduled.Backups {
		if point.ID != backup.ID && point.Status == "succeeded" {
			retirementCandidate = point.ID
			break
		}
	}
	if retirementCandidate == "" {
		t.Fatal("no scheduled native dump available for retirement")
	}
	catalogRetirement(t, c, running, db, project.ID, retirementCandidate, backup.ID, socket)
	catalogAutomaticRetention(t, c, db, project.ID, backup.ID, socket, func() forge.App {
		running.stop()
		running = start(t, cfg)
		c.base = running.srv.URL
		return running.app.(forge.App)
	})

	outsider := &client{t: t, base: running.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	outsider.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "other-backup@example.test", "password": "Correct-horse-battery-9"}, &auth)
	outsider.token = auth.SessionToken
	var otherOrg struct {
		ID string `json:"id"`
	}
	outsider.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Other backups", "slug": "other-backups"}, &otherOrg)
	outsider.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": otherOrg.ID}, nil)
	outsider.must(http.StatusNotFound, http.MethodGet, "/api/v1/service-backups/"+backup.ID, nil, nil)
	outsider.must(http.StatusNotFound, http.MethodPost, base+"/service-backups", request, nil)
	outsider.must(http.StatusNotFound, http.MethodGet, "/api/v1/service-restores/"+restored.ID, nil, nil)
	outsider.must(http.StatusNotFound, http.MethodPost, "/api/v1/service-backups/"+backup.ID+"/restore", restoreRequest, nil)
	t.Logf("backup %s survived application restart with %d verified bytes", backup.ID, backup.Bytes)
	t.Logf("API plan %s deployed standalone PostgreSQL through checked SSH target; application writes and restricted role verified", plan.ID)
	verifyBrowserRetirements := prepareRetirementBrowser(t, c, running, db, project.ID, backup.ID)
	holdCatalogBrowser(t, running.srv.URL, project.ID, connection.ID)
	verifyBrowserRetirements()
}
