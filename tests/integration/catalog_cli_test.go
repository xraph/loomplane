//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/planning"
	lpclient "github.com/xraph/loomplane/pkg/client"
)

func TestAcceptanceCatalogCLIRecovery(t *testing.T)        { catalogCLIRecovery(t, "postgres") }
func TestAcceptanceMySQLCatalogCLIRecovery(t *testing.T)   { catalogCLIRecovery(t, "mysql") }
func TestAcceptanceMariaDBCatalogCLIRecovery(t *testing.T) { catalogCLIRecovery(t, "mariadb") }
func TestAcceptanceRedisCatalogCLIRecovery(t *testing.T)   { catalogCLIRecovery(t, "redis") }
func TestAcceptanceValkeyCatalogCLIRecovery(t *testing.T)  { catalogCLIRecovery(t, "valkey") }

func catalogCLIRecovery(t *testing.T, kind string) {
	fixture := catalogServer(t)
	running, c, lc, socket := fixture.running, fixture.client, fixture.cli, fixture.socket
	previous := lpclient.Every
	lpclient.Every = 100 * time.Millisecond
	t.Cleanup(func() { lpclient.Every = previous })
	var plan lpclient.Plan
	if err := json.Unmarshal([]byte(lc.must("services", "create", "CLI acceptance", "--kind", kind, "--target", fixture.connection, "--json")), &plan); err != nil || !plan.Executable || plan.ProjectID == "" {
		t.Fatalf("catalog CLI plan: %+v %v", plan, err)
	}
	t.Cleanup(func() { cleanupCatalogProject(t, socket, plan.ProjectID) })
	lc.must("approve", plan.ID, "--yes")
	var bindings struct{ Bindings []planning.Binding }
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+plan.ProjectID+"/bindings?environment=production", nil, &bindings)
	if len(bindings.Bindings) != 1 {
		t.Fatalf("database binding: %+v", bindings)
	}
	sourceID, err := id.Parse(bindings.Bindings[0].ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	data := func(instance id.ID, operation string) string {
		t.Helper()
		if kind == "redis" || kind == "valkey" {
			args := map[string][]string{
				"seed":   {"SET", "cli_probe", "restored-through-cli"},
				"remove": {"DEL", "cli_probe"},
				"value":  {"GET", "cli_probe"},
				"count":  {"EXISTS", "cli_probe"},
			}[operation]
			command := make([]string, 0, 15+len(args))
			command = append(command, "docker", "--host", "unix://"+socket, "exec", server.ContainerName(instance), "sh", "-c", `export REDISCLI_AUTH="$LOOMPLANE_APP_PASSWORD"; exec "$@"`, "loomplane-probe", kind+"-cli", "--no-auth-warning", "-e", "--raw", "--user", "app")
			return fixtureCommand(t, append(command, args...)...)
		}
		query := map[string]string{
			"seed":   "CREATE TABLE cli_probe(value text); INSERT INTO cli_probe VALUES ('restored-through-cli')",
			"remove": "DELETE FROM cli_probe",
			"value":  "SELECT value FROM cli_probe",
			"count":  "SELECT count(*) FROM cli_probe",
		}[operation]
		if kind == "postgres" {
			return fixtureCommand(t, "docker", "--host", "unix://"+socket, "exec", server.ContainerName(instance), "psql", "-U", "app", "-d", "database", "-At", "-v", "ON_ERROR_STOP=1", "-c", query)
		}
		tool := "mysql"
		if kind == "mariadb" {
			tool = "mariadb"
		}
		return fixtureCommand(t, "docker", "--host", "unix://"+socket, "exec", server.ContainerName(instance), "sh", "-c", `export MYSQL_PWD="$LOOMPLANE_APP_PASSWORD"; exec "$@"`, "loomplane-probe", tool, "--protocol=tcp", "--host=127.0.0.1", "--user=app", "--database=database", "--batch", "--skip-column-names", "--execute", query)
	}
	followAfterTimeout := func(kind, subject, request string) string {
		t.Helper()
		if _, rejected := lc.run("", kind, "create", subject, "--request-id", "invalid-timeout", "--wait", "--timeout", "0", "--json"); rejected == nil {
			t.Fatal("invalid wait timeout was accepted")
		}
		var before []json.RawMessage
		if decodeErr := json.Unmarshal([]byte(lc.must(kind, plan.ProjectID, "--json")), &before); decodeErr != nil || len(before) != 0 {
			t.Fatalf("invalid timeout queued %s: %+v %v", kind, before, decodeErr)
		}
		out, expired := lc.run("", kind, "create", subject, "--request-id", request, "--wait", "--timeout", "1ns", "--json")
		var accepted struct{ ID string }
		if expired == nil || json.Unmarshal([]byte(out), &accepted) != nil || accepted.ID == "" {
			t.Fatalf("lost %s operation on wait timeout: %v %s", kind, expired, out)
		}
		return lc.must(kind, "show", accepted.ID, "--wait", "--json")
	}
	data(sourceID, "seed")
	var backup lpclient.ServiceBackup
	if err = json.Unmarshal([]byte(followAfterTimeout("backups", plan.ProjectID, "cli-backup")), &backup); err != nil || backup.Status != "succeeded" || backup.Bytes == 0 {
		t.Fatalf("CLI backup: %+v %v", backup, err)
	}
	var repeated lpclient.ServiceBackup
	if err = json.Unmarshal([]byte(lc.must("backups", "create", plan.ProjectID, "--request-id", "cli-backup", "--json")), &repeated); err != nil || repeated.ID != backup.ID {
		t.Fatalf("backup identity: %+v %v", repeated, err)
	}
	var readKey keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "CLI recovery reader", "access": "read"}, &readKey)
	reader := loomctl{t: t, url: running.srv.URL, key: readKey.Secret, config: filepath.Join(t.TempDir(), "reader.json")}
	reader.must("backups", "show", backup.ID, "--json")
	for _, args := range [][]string{
		{"backups", "create", plan.ProjectID, "--request-id", "reader-denied"},
		{"restores", "create", backup.ID, "--request-id", "reader-denied"},
		{"backups", "policy", plan.ProjectID, "--every-hours", "6"},
	} {
		if _, denied := reader.run("", args...); !errors.Is(denied, lpclient.ErrForbidden) {
			t.Fatalf("read-only CLI accepted %v: %v", args, denied)
		}
	}
	data(sourceID, "remove")
	var restored lpclient.ServiceRestore
	if err = json.Unmarshal([]byte(followAfterTimeout("restores", backup.ID, "cli-restore")), &restored); err != nil || restored.Status != "succeeded" || restored.VerifiedAt == nil {
		t.Fatalf("CLI restore: %+v %v", restored, err)
	}
	copyID, err := id.Parse(restored.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if got := data(copyID, "value"); got != "restored-through-cli" {
		t.Fatalf("copy contents: %s", got)
	}
	if got := data(sourceID, "count"); got != "0" {
		t.Fatalf("restore changed live source: %s", got)
	}
	lc.must("backups", "policy", plan.ProjectID, "--resource", "database", "--every-hours", "6")
	var policies []lpclient.BackupPolicy
	if err = json.Unmarshal([]byte(lc.must("backups", "policies", plan.ProjectID, "--json")), &policies); err != nil || len(policies) != 1 || policies[0].EveryHours != 6 || !policies[0].Enabled {
		t.Fatalf("CLI schedule: %+v %v", policies, err)
	}
	lc.must("backups", "policy", plan.ProjectID, "--resource", "database", "--every-hours", "6", "--disable")
	lc.must("restores", "delete", restored.ID, "--yes")
	if err = json.Unmarshal([]byte(lc.must("restores", "show", restored.ID, "--json")), &restored); err != nil || restored.Status != "deleted" || restored.VerifiedAt == nil {
		t.Fatalf("CLI copy cleanup: %+v %v", restored, err)
	}
	if volumes := fixtureCommand(t, "docker", "--host", "unix://"+socket, "volume", "ls", "-q", "--filter", "label=loomplane.instance="+copyID.String()); volumes != "" {
		t.Fatalf("copy volume remains: %s", volumes)
	}
	t.Logf("CLI catalog plan %s, backup %s and verified restore %s passed, including timeout resumption; temporary copy removed", plan.ID, backup.ID, restored.ID)
	holdCatalogBrowser(t, running.srv.URL, plan.ProjectID, fixture.connection)
}
