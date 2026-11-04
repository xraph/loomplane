package client

import (
	"context"
	"testing"
)

const releaseReportJSON = `{"id":"rrep_1","project_id":"proj_1","from_environment":"staging","to_environment":"production","state":"planning",
"gates":[{"number":4,"name":"staging soak","passed":false,"detail":"staging has run it for 5 minutes"}],
"evidence":{"soak":{"running":true,"duration":300000000000,"healthy":true},"sources":[{"alias":"api","direction":"ahead","total_commits":2}]},
"concerns":[{"kind":"ask","text":"a note"}]}`

func TestClientReleaseRoutes(t *testing.T) {
	c, calls := agentServer(t, `{"reports":[`+releaseReportJSON+`]}`)
	ctx := context.Background()

	list, err := c.ReleaseReports(ctx, "proj 1", 5)
	if err != nil || len(list) != 1 || list[0].ID != "rrep_1" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, err := c.ReleaseReports(ctx, "proj_1", 0); err != nil {
		t.Fatal(err)
	}

	c2, calls2 := agentServer(t, releaseReportJSON)
	r, err := c2.PrepareRelease(ctx, "proj_1", PrepareRelease{From: "staging", To: "production", Scope: []string{"api"}, Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c2.ReleaseReport(ctx, "rrep_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c2.CancelReleaseReport(ctx, "rrep_1"); err != nil {
		t.Fatal(err)
	}

	want := []agentCall{
		{"GET", "/api/v1/projects/proj%201/release-reports", "limit=5", ""},
		{"GET", "/api/v1/projects/proj_1/release-reports", "", ""},
	}
	for i, w := range want {
		got := calls()[i]
		if got.method != w.method || got.query != w.query {
			t.Errorf("call %d = %+v, want %+v", i, got, w)
		}
	}
	want2 := []agentCall{
		{"POST", "/api/v1/projects/proj_1/release-reports", "", `{"from":"staging","to":"production","scope":["api"],"replace":true}`},
		{"GET", "/api/v1/release-reports/rrep_1", "", ""},
		{"POST", "/api/v1/release-reports/rrep_1/cancel", "", ""},
	}
	got2 := calls2()
	for i, w := range want2 {
		if got2[i] != w {
			t.Errorf("call %d = %+v, want %+v", i, got2[i], w)
		}
	}

	ev, err := r.ReadinessEvidence()
	if err != nil || ev == nil || !ev.Soak.Running || ev.Soak.Duration.Minutes() != 5 || ev.Sources[0].Alias != "api" {
		t.Fatalf("evidence = %+v, %v", ev, err)
	}
	if r.Gates[0].Number != 4 || r.Concerns[0].Kind != "ask" {
		t.Fatalf("report = %+v", r)
	}
	if ev, err := (&ReleaseReport{}).ReadinessEvidence(); ev != nil || err != nil {
		t.Fatalf("no evidence = %+v, %v", ev, err)
	}
}
