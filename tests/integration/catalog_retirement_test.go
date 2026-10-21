//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/forge"
	"github.com/xraph/trove/driver"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/ctl"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
	api "github.com/xraph/loomplane/pkg/client"
)

// The catalog drill supplies actual native dumps and a SQL-tested witness. Only
// the candidate's age is advanced so the age policy can be exercised today.
func catalogRetirement(t *testing.T, c *client, running *running, db *pgx.Conn, project, candidate, witness, socket string) {
	t.Helper()
	files, err := forge.InjectType[*backupfiles.Store](running.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}
	var candidateRaw, witnessRaw, sourceRaw []byte
	if err = db.QueryRow(t.Context(), `SELECT object,source FROM loomplane_service_backups WHERE id=$1`, candidate).Scan(&candidateRaw, &sourceRaw); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(t.Context(), `SELECT object FROM loomplane_service_backups WHERE id=$1`, witness).Scan(&witnessRaw); err != nil {
		t.Fatal(err)
	}
	var candidateObject, witnessObject backupfiles.Object
	if err = json.Unmarshal(candidateRaw, &candidateObject); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(witnessRaw, &witnessObject); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(t.Context(), `UPDATE loomplane_service_backups SET created_at=now()-interval '40 days 1 minute',completed_at=now()-interval '40 days' WHERE id=$1`, candidate); err != nil {
		t.Fatal(err)
	}
	var key struct {
		Secret string `json:"secret"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "Catalog retirement CLI", "access": "write"}, &key)
	run := func(args ...string) []byte {
		t.Helper()
		var output bytes.Buffer
		app := ctl.New(ctl.Options{Out: &output, ConfigPath: t.TempDir() + "/loomctl.json", Getenv: func(name string) string {
			if name == ctl.EnvURL {
				return c.base
			}
			if name == ctl.EnvKey {
				return key.Secret
			}
			return ""
		}})
		if checkErr := app.Run(args); checkErr != nil {
			t.Fatalf("retirement CLI %v: %v output=%s", args, checkErr, output.String())
		}
		return output.Bytes()
	}
	var retired api.ServiceRetirement
	if err = json.Unmarshal(run("backups", "retire", candidate, "--yes", "--older-than-days", "7", "--keep-last", "1", "--request-id", "catalog-retirement-request", "--wait", "--timeout", "45s", "--json"), &retired); err != nil {
		t.Fatal(err)
	}
	if retired.Status != "succeeded" || retired.BackupID != candidate || retired.WitnessID != witness {
		t.Fatalf("native retirement result: %+v", retired)
	}
	if _, err = files.Trove.Head(t.Context(), files.Bucket, candidateObject.Key); !errors.Is(err, driver.ErrNotFound) {
		t.Fatalf("native retired dump remains: %v", err)
	}
	if _, err = files.Trove.Head(t.Context(), files.Bucket, witnessObject.Key); err != nil {
		t.Fatalf("SQL-tested witness disappeared: %v", err)
	}
	var page api.RetirementPage
	if err = json.Unmarshal(run("retirements", project, "--limit", "1", "--json"), &page); err != nil || len(page.Retirements) != 1 || page.Retirements[0].ID != retired.ID {
		t.Fatalf("native retirement list: %+v %v", page, err)
	}
	var history api.RetirementHistory
	if err = json.Unmarshal(run("retirements", "history", retired.ID, "--limit", "1", "--json"), &history); err != nil || len(history.Events) != 1 || history.Events[0].Kind != "succeeded" || history.NextBefore == "" {
		t.Fatalf("native retirement history: %+v %v", history, err)
	}
	var repeated api.ServiceRetirement
	if err = json.Unmarshal(run("backups", "retire", candidate, "--yes", "--older-than-days", "7", "--keep-last", "1", "--request-id", "catalog-retirement-request", "--wait", "--json"), &repeated); err != nil || repeated.ID != retired.ID {
		t.Fatalf("native retirement idempotency: %+v %v", repeated, err)
	}
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/service-backups/"+candidate+"/restore", map[string]string{"request_id": "retired-dump-restore"}, nil)
	// Restore the retained dump again after deletion. It must still contain the
	// original row, even though the source row was deleted earlier in the drill.
	var restored recovery.Restore
	c.must(http.StatusAccepted, http.MethodPost, "/api/v1/service-backups/"+witness+"/restore", map[string]string{"request_id": "post-retirement-restore"}, &restored)
	deadline := time.Now().Add(time.Minute)
	for restored.Status == "queued" || restored.Status == "running" {
		if time.Now().After(deadline) {
			t.Fatalf("retained dump restore did not settle: %+v", restored)
		}
		time.Sleep(100 * time.Millisecond)
		c.must(http.StatusOK, http.MethodGet, "/api/v1/service-restores/"+restored.ID, nil, &restored)
	}
	if restored.Status != "succeeded" || restored.VerifiedAt == nil {
		t.Fatalf("retained SQL restore: %+v", restored)
	}
	clone, err := id.Parse(restored.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if value := fixtureCommand(t, "docker", "--host", "unix://"+socket, "exec", server.ContainerName(clone), "psql", "-U", "app", "-d", "database", "-At", "-c", "SELECT value FROM catalog_probe"); value != "from-api" {
		t.Fatalf("retained dump lost original data: %s", value)
	}
	c.must(http.StatusNoContent, http.MethodDelete, "/api/v1/service-restores/"+restored.ID, nil, nil)
	if remaining := fixtureCommand(t, "docker", "--host", "unix://"+socket, "volume", "ls", "-q", "--filter", "label=loomplane.instance="+clone.String()); remaining != "" {
		t.Fatalf("post-retirement restore volume survived cleanup: %s", remaining)
	}
	var source planning.Binding
	if err = json.Unmarshal(sourceRaw, &source); err != nil {
		t.Fatal(err)
	}
	sourceID, err := id.Parse(source.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if count := fixtureCommand(t, "docker", "--host", "unix://"+socket, "exec", server.ContainerName(sourceID), "psql", "-U", "app", "-d", "database", "-At", "-c", "SELECT count(*) FROM catalog_probe"); count != "0" {
		t.Fatalf("retirement or retained restore changed source data: %s", count)
	}
	t.Logf("retirement %s deleted native dump %s; retained dump %s restored original SQL row", retired.ID, candidate, witness)
}
