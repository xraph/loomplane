//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/realtime"
)

func infrastructureBoundaries(t *testing.T, cfg config.Config, r *running, c *client, org, project, deployment string) {
	t.Helper()
	var key keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "acceptance read", "access": "read"}, &key)
	reader := &client{t: t, base: c.base, token: key.Secret, key: c.key}
	feed := infrastructureWS(t, r, reader, org, "", deployment)
	defer feed.close()
	c.must(http.StatusNoContent, http.MethodDelete, "/api/v1/api-keys/"+key.Key.ID, nil, nil)
	select {
	case <-feed.done:
	case <-time.After(8 * time.Second):
		t.Fatal("revoked read grant left the real deployment stream open")
	}
	reader.must(http.StatusUnauthorized, http.MethodGet, "/api/v1/deployments/"+deployment+"/logs", nil, nil)
	var other struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Other acceptance organization", "slug": "other-acceptance"}, &other)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": other.ID}, nil)
	refused(t, c, http.MethodGet, "/api/v1/deployments/"+deployment+"/logs", nil)
	c.must(http.StatusConflict, http.MethodGet, "/api/v1/realtime/snapshot?org_id="+org, nil, nil)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org}, nil)
	db, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(t.Context())
	if _, err = db.Exec(t.Context(), `UPDATE loomplane_event_cursors SET floor_seq=next_seq WHERE org_id=$1`, org); err != nil {
		t.Fatal(err)
	}
	gap := infrastructureSSE(t, c, org, org+":0", deployment)
	defer gap.close()
	deadline := time.Now().Add(8 * time.Second)
	for {
		gap.mu.Lock()
		gapSeen, snapshotSeen := false, false
		for _, frame := range gap.frames {
			gapSeen = gapSeen || frame.Event == "forge.gap"
			snapshotSeen = snapshotSeen || (frame.Event == "realtime.snapshot" && json.Valid(frame.Data))
		}
		gap.mu.Unlock()
		if gapSeen && snapshotSeen {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expired cursor did not deliver a gap and current snapshot")
		}
		time.Sleep(20 * time.Millisecond)
	}
	store, err := forge.InjectType[*realtime.PgStore](r.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}
	input := realtime.LogInput{OrgID: org, ProjectID: project, DeploymentID: deployment, ActionID: "service:api", Attempt: "bounded-acceptance", OperationID: "retention", ProviderID: "first", Source: "acceptance", Stream: "stdout", Level: "info", Message: "bounded retention marker", Time: time.Now()}
	if _, err = store.AppendLog(t.Context(), input, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(t.Context(), `UPDATE loomplane_build_log_budgets SET bytes=$4 WHERE org_id=$1 AND deployment_id=$2 AND attempt=$3`, org, deployment, input.Attempt, realtime.MaxAttemptBytes); err != nil {
		t.Fatal(err)
	}
	input.ProviderID = "limit"
	if _, err = store.AppendLog(t.Context(), input, nil); err != nil {
		t.Fatal(err)
	}
	var page realtime.LogPage
	c.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+deployment+"/logs?limit=1000", nil, &page)
	truncated := false
	for _, line := range page.Logs {
		truncated = truncated || line.Truncated
	}
	if !truncated {
		t.Fatal("bounded retained output omitted its truncation marker")
	}
	t.Log("real deployment read revocation, organization isolation, expired cursor recovery and bounded output passed")
}
