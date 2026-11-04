package client

import (
	"context"
	"testing"
)

const agentReportJSON = `{"id":"arep_1","project_id":"proj_1","environment":"production","status":"open","severity":"high",
"title":"Deployment failed","facts":[{"label":"Error","value":"boom","untrusted":true}],"findings":null,
"occurrences":3,"issue":{"state":"none"},"first_seen_at":"2026-11-02T10:00:00Z","last_seen_at":"2026-11-02T11:00:00Z"}`

func TestClientAgentReportRoutes(t *testing.T) {
	c, calls := agentServer(t, `{"reports":[`+agentReportJSON+`]}`)
	ctx := context.Background()

	list, err := c.AgentReports(ctx, "all", "proj_1")
	if err != nil || len(list) != 1 || list[0].ID != "arep_1" || list[0].Occurrences != 3 || !list[0].Facts[0].Untrusted {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, err = c.AgentReports(ctx, "", ""); err != nil {
		t.Fatal(err)
	}

	c, calls2 := agentServer(t, agentReportJSON)
	r, err := c.AgentReport(ctx, "arep_1")
	if err != nil || r.Title != "Deployment failed" || r.Findings != nil {
		t.Fatalf("report = %+v, %v", r, err)
	}
	if _, err = c.CloseAgentReport(ctx, "arep_1", "fixed upstream"); err != nil {
		t.Fatal(err)
	}

	c, calls3 := agentServer(t, `{"paused":true,"paused_reason":"budget","paused_at":"2026-11-02T10:00:00Z","ticked_at":null,
"issues":{"configured":true,"installations":[{"account":"acme","account_type":"Organization","accepted":false,"manage_url":"https://github.com/x","checked_at":"2026-11-02T10:00:00Z"}]}}`)
	w, err := c.AgentWatcher(ctx)
	if err != nil || !w.Paused || w.PausedReason != "budget" || w.TickedAt != nil || len(w.Issues.Installations) != 1 || w.Issues.Installations[0].Accepted {
		t.Fatalf("watcher = %+v, %v", w, err)
	}

	want := []agentCall{
		{"GET", "/api/v1/agent/reports", "project_id=proj_1&status=all", ""},
		{"GET", "/api/v1/agent/reports", "", ""},
	}
	got := calls()
	for i, w := range want {
		if got[i] != w {
			t.Errorf("call %d = %+v, want %+v", i, got[i], w)
		}
	}
	got2 := calls2()
	if len(got2) != 2 || got2[0] != (agentCall{"GET", "/api/v1/agent/reports/arep_1", "", ""}) ||
		got2[1] != (agentCall{"POST", "/api/v1/agent/reports/arep_1/close", "", `{"reason":"fixed upstream"}`}) {
		t.Errorf("report calls = %+v", got2)
	}
	if got3 := calls3(); len(got3) != 1 || got3[0].method != "GET" || got3[0].path != "/api/v1/agent/watcher" {
		t.Errorf("watcher calls = %+v", got3)
	}
}
