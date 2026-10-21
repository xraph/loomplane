//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge"
	"github.com/xraph/trove/driver"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/ctl"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recovery"
	api "github.com/xraph/loomplane/pkg/client"
)

func TestRetirementAPIIsScopedAndVerifiesBytesBeforeRetry(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	running := start(t, cfg)
	defer running.stop()
	c := &client{t: t, base: running.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var contract struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name     string `json:"name"`
				In       string `json:"in"`
				Required bool   `json:"required"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	c.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &contract)
	for path, queryCount := range map[string]int{"/api/v1/projects/{id}/service-backup-retirements": 3, "/api/v1/service-backup-retirements/{id}/history": 2, "/api/v1/service-backups/{id}/retention-policy/history": 2} {
		count := 0
		for _, parameter := range contract.Paths[path]["get"].Parameters {
			if parameter.In == "query" {
				count++
				if parameter.Required {
					t.Fatalf("optional retirement query marked required: %s %s", path, parameter.Name)
				}
			}
		}
		if count != queryCount {
			t.Fatalf("retirement query schema lost fields: %s %d", path, count)
		}
	}
	var session struct {
		OrgID string `json:"org_id"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/session", nil, &session)
	var project struct{ ID string }
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Retirement API"}, &project)
	waitReady(t, c, project.ID)
	listPath := "/api/v1/projects/" + project.ID + "/service-backup-retirements"
	var empty struct {
		Retirements []recovery.Retirement `json:"retirements"`
	}
	c.must(http.StatusOK, http.MethodGet, listPath, nil, &empty)
	if empty.Retirements == nil || len(empty.Retirements) != 0 {
		t.Fatalf("empty retirement inventory: %+v", empty)
	}
	db, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	files, err := forge.InjectType[*backupfiles.Store](running.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}
	var environment string
	if err = db.QueryRow(t.Context(), `SELECT id FROM loomplane_environments WHERE org_id=$1 AND project_id=$2 AND name='production'`, session.OrgID, project.ID).Scan(&environment); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(t.Context(), `INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('retirement-connection',$1,'server','Retirement fixture','fixture','ready','fixture',now(),now())`, session.OrgID); err != nil {
		t.Fatal(err)
	}
	recipe, err := recipes.Lookup("postgres", "17", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	resource, err := json.Marshal(planning.ResourceConfig{Provider: "server", Kind: "postgres", Target: "retirement-connection", Catalog: recipe})
	if err != nil {
		t.Fatal(err)
	}
	source, err := json.Marshal(planning.Binding{ID: "binding", OrgID: session.OrgID, ProjectID: project.ID, EnvironmentID: environment, Name: "database", Subject: planning.ResourceSubject, ExternalID: "instance", Provider: "server", ConnectionID: "retirement-connection", Config: resource})
	if err != nil {
		t.Fatal(err)
	}
	objects := map[string]backupfiles.Object{}
	for _, id := range []string{"candidate-a", "candidate-b", "witness"} {
		ref, putErr := files.Put(t.Context(), session.OrgID+"/"+project.ID+"/"+environment+"/"+id+"/attempt.dump", strings.NewReader(id+" database bytes"))
		if putErr != nil {
			t.Fatal(putErr)
		}
		objects[id] = ref
		raw, marshalErr := json.Marshal(ref)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		age := 40
		if id == "witness" {
			age = 1
		}
		if _, err = db.Exec(t.Context(), `INSERT INTO loomplane_service_backups(id,org_id,project_id,environment_id,connection_id,name,status,request_id,source,object,token,created_by,created_at,completed_at,restored_at)
 VALUES($1,$2,$3,$4,'retirement-connection','database','succeeded',$1,$5,$6,'attempt','fixture',now()-$7*interval '1 day'-interval '1 minute',now()-$7*interval '1 day',CASE WHEN $1='witness' THEN now() END)`, id, session.OrgID, project.ID, environment, source, raw, age); err != nil {
			t.Fatal(err)
		}
	}
	var capability recovery.RetentionPreview
	c.must(http.StatusOK, http.MethodGet, "/api/v1/service-backups/candidate-a/retention-preview?older_than_days=7&keep_last=1", nil, &capability)
	for _, point := range capability.Points {
		if !point.DeletionSupported {
			t.Fatalf("local captured object deletion unavailable: %+v", point)
		}
	}
	var readKey struct {
		Secret string `json:"secret"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "Retirement reader", "access": "read"}, &readKey)
	reader := &client{t: t, base: c.base, key: c.key, token: readKey.Secret}
	body := map[string]any{"request_id": "retirement-api-request", "policy": map[string]int{"older_than_days": 7, "keep_last": 1}}
	reader.must(http.StatusForbidden, http.MethodPost, "/api/v1/service-backups/candidate-a/retire", body, nil)
	if err = files.Trove.Delete(t.Context(), files.Bucket, objects["witness"].Key); err != nil {
		t.Fatal(err)
	}
	var retirement, replayed recovery.Retirement
	c.must(http.StatusAccepted, http.MethodPost, "/api/v1/service-backups/candidate-a/retire", body, &retirement)
	c.must(http.StatusAccepted, http.MethodPost, "/api/v1/service-backups/candidate-a/retire", body, &replayed)
	if retirement.ID != replayed.ID {
		t.Fatal("lost retirement idempotency")
	}
	retirement = waitRetirementAPI(t, c, retirement.ID, "failed")
	if _, err = files.Trove.Head(t.Context(), files.Bucket, objects["candidate-a"].Key); err != nil {
		t.Fatalf("unverified witness allowed deletion: %v", err)
	}
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/service-backups/candidate-a/restore", map[string]string{"request_id": "blocked-restore"}, nil)
	retryPath := "/api/v1/service-backup-retirements/" + retirement.ID + "/retry"
	reader.must(http.StatusForbidden, http.MethodPost, retryPath, map[string]string{"request_id": "read-only-retry"}, nil)
	if _, err = files.Put(t.Context(), objects["witness"].Key, strings.NewReader("witness database bytes")); err != nil {
		t.Fatal(err)
	}
	c.must(http.StatusAccepted, http.MethodPost, retryPath, map[string]string{"request_id": "repair-and-retry"}, &retirement)
	retirement = waitRetirementAPI(t, c, retirement.ID, "succeeded")
	if _, err = files.Trove.Head(t.Context(), files.Bucket, objects["candidate-a"].Key); !errors.Is(err, driver.ErrNotFound) {
		t.Fatalf("retired object still present: %v", err)
	}
	if _, err = files.Trove.Head(t.Context(), files.Bucket, objects["witness"].Key); err != nil {
		t.Fatalf("retained witness lost: %v", err)
	}
	getPath := "/api/v1/service-backup-retirements/" + retirement.ID
	raw := reader.raw(getPath)
	for _, hidden := range []string{"\"object\"", "\"token\"", "\"request_id\"", "\"source_key\"", "\"witness_object\""} {
		if strings.Contains(raw, hidden) {
			t.Fatalf("retirement disclosed internal field %s", hidden)
		}
	}
	var history recovery.RetirementHistory
	reader.must(http.StatusOK, http.MethodGet, getPath+"/history?limit=2", nil, &history)
	if len(history.Events) != 2 || history.Events[0].Kind != "succeeded" || history.NextBefore == "" {
		t.Fatalf("retirement history page: %+v", history)
	}
	reader.must(http.StatusOK, http.MethodGet, getPath+"/history?before="+history.NextBefore+"&limit=100", nil, &history)
	if len(history.Events) != 4 || history.Events[0].Kind != "retry" {
		t.Fatalf("retirement history continuation: %+v", history)
	}
	c.must(http.StatusBadRequest, http.MethodGet, listPath+"?limit=oops", nil, nil)
	c.must(http.StatusUnprocessableEntity, http.MethodGet, getPath+"/history?before=not-a-cursor", nil, nil)
	c.must(http.StatusUnprocessableEntity, http.MethodGet, listPath+"?limit=101", nil, nil)
	c.must(http.StatusUnprocessableEntity, http.MethodGet, listPath+"?before=not-a-cursor", nil, nil)
	var writeKey struct {
		Secret string `json:"secret"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "Retirement CLI owner", "access": "write"}, &writeKey)
	runCLI := func(key string, args ...string) ([]byte, error) {
		var output bytes.Buffer
		app := ctl.New(ctl.Options{Out: &output, ConfigPath: t.TempDir() + "/loomctl.json", Getenv: func(name string) string {
			if name == ctl.EnvURL {
				return c.base
			}
			if name == ctl.EnvKey {
				return key
			}
			return ""
		}})
		runErr := app.Run(args)
		return output.Bytes(), runErr
	}
	output, err := runCLI(writeKey.Secret, "backups", "retire", "candidate-b", "--yes", "--older-than-days", "7", "--keep-last", "1", "--request-id", "cli-retirement-request", "--wait", "--timeout", "15s", "--json")
	if err != nil {
		t.Fatalf("real retirement CLI: %v output=%s", err, output)
	}
	var second recovery.Retirement
	if err = json.Unmarshal(output, &second); err != nil || second.Status != "succeeded" {
		t.Fatalf("CLI retirement result: %+v %v", second, err)
	}
	var resolved recovery.RetirementPage
	reader.must(http.StatusOK, http.MethodGet, listPath+"?backup_id=candidate-a", nil, &resolved)
	if len(resolved.Retirements) != 1 || resolved.Retirements[0].ID != retirement.ID {
		t.Fatalf("point-scoped API lookup: %+v", resolved)
	}
	var page recovery.RetirementPage
	reader.must(http.StatusOK, http.MethodGet, listPath+"?limit=1", nil, &page)
	if len(page.Retirements) != 1 || page.Retirements[0].ID != second.ID || page.NextBefore == "" {
		t.Fatalf("API retirement first page: %+v", page)
	}
	nextCursor := page.NextBefore
	page = recovery.RetirementPage{}
	reader.must(http.StatusOK, http.MethodGet, listPath+"?limit=1&before="+url.QueryEscape(nextCursor), nil, &page)
	if len(page.Retirements) != 1 || page.Retirements[0].ID != retirement.ID || page.NextBefore != "" {
		t.Fatalf("API retirement continuation: %+v", page)
	}
	output, err = runCLI(readKey.Secret, "retirements", project.ID, "--limit", "1", "--json")
	if err != nil || json.Unmarshal(output, &page) != nil || len(page.Retirements) != 1 {
		t.Fatalf("read-only CLI list: %v %s", err, output)
	}
	output, err = runCLI(readKey.Secret, "retirements", "history", second.ID, "--limit", "1", "--json")
	if err != nil || json.Unmarshal(output, &history) != nil || len(history.Events) != 1 || history.Events[0].Kind != "succeeded" {
		t.Fatalf("read-only CLI history: %v %s", err, output)
	}
	if _, err = runCLI(readKey.Secret, "retirements", "retry", retirement.ID, "--yes", "--request-id", "reader-retry-key"); !errors.Is(err, api.ErrForbidden) {
		t.Fatalf("read-only CLI retried deletion: %v", err)
	}
	verifyRetentionPolicyControls(t, c, reader, runCLI, writeKey.Secret, readKey.Secret)
	var invitation struct {
		AcceptPath string `json:"accept_path"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations", map[string]string{"email": "retirement-admin@example.test", "role": "admin"}, &invitation)
	inviteURL, err := url.Parse(invitation.AcceptPath)
	if err != nil {
		t.Fatal(err)
	}
	admin := &client{t: t, base: c.base, key: c.key}
	var signed struct {
		SessionToken string `json:"session_token"`
	}
	admin.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "retirement-admin@example.test", "password": "Correct-horse-battery-9"}, &signed)
	admin.token = signed.SessionToken
	status := admin.do(http.MethodPost, "/authsome/v1/orgs/invitations/accept", map[string]string{"token": inviteURL.Query().Get("token")}, nil)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("admin invitation: %d", status)
	}
	admin.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": session.OrgID}, nil)
	admin.must(http.StatusOK, http.MethodGet, listPath, nil, nil)
	admin.must(http.StatusOK, http.MethodGet, "/api/v1/service-backups/witness/retention-policy", nil, nil)
	admin.must(http.StatusForbidden, http.MethodPut, "/api/v1/service-backups/witness/retention-policy", recovery.RetentionPolicyInput{Revision: 4, Policy: recovery.RetentionInput{OlderThanDays: 30, KeepLast: 2}}, nil)
	admin.must(http.StatusForbidden, http.MethodPost, "/api/v1/service-backups/witness/retire", body, nil)
	admin.must(http.StatusForbidden, http.MethodPost, retryPath, map[string]string{"request_id": "admin-retry-key"}, nil)

	var other struct{ ID string }
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Other retirement", "slug": "other-retirement"}, &other)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": other.ID}, nil)
	for _, path := range []string{getPath, getPath + "/history", listPath, "/api/v1/service-backups/witness/retention-policy", "/api/v1/service-backups/witness/retention-policy/history"} {
		c.must(http.StatusNotFound, http.MethodGet, path, nil, nil)
	}
	c.must(http.StatusNotFound, http.MethodPost, retryPath, map[string]string{"request_id": "foreign-retry-key"}, nil)
	c.must(http.StatusNotFound, http.MethodPut, "/api/v1/service-backups/witness/retention-policy", recovery.RetentionPolicyInput{Revision: 4, Policy: recovery.RetentionInput{OlderThanDays: 30, KeepLast: 2}}, nil)
	holdCatalogBrowser(t, running.srv.URL, project.ID, "retirement-connection")
}

func waitRetirementAPI(t *testing.T, c *client, id, status string) recovery.Retirement {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var r recovery.Retirement
		c.must(http.StatusOK, http.MethodGet, "/api/v1/service-backup-retirements/"+id, nil, &r)
		if r.Status == status {
			return r
		}
		if r.Status == "succeeded" || time.Now().After(deadline) {
			t.Fatalf("retirement did not reach %s: %+v", status, r)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
