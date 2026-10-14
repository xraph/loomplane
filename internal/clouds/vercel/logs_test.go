package vercel

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/buildlogs"
)

func TestBuildLogsFollowAndReplay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/deployments/dpl_test/events" || r.URL.Query().Get("teamId") != "team_test" || r.URL.Query().Get("builds") != "1" || r.URL.Query().Get("format") != "lines" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unscoped log request: %s", r.URL)
		}
		if r.URL.Query().Get("follow") == "1" {
			fmt.Fprintln(w, `{"type":"stdout","created":10000,"payload":{"id":"a","deploymentId":"dpl_test","text":"same"}}`)
			w.(http.Flusher).Flush()
		}
		fmt.Fprintln(w, `{"type":"stdout","created":10001,"payload":{"id":"b","deploymentId":"dpl_test","text":"same"}}`)
		fmt.Fprintln(w, `{"type":"command","created":10001,"id":"c","deploymentId":"dpl_test","text":"build command"}`)
		fmt.Fprintln(w, `{"type":"stdout","created":10002,"payload":{"id":"foreign","deploymentId":"dpl_other","text":"private"}}`)
	}))
	defer srv.Close()
	var lines []buildlogs.Line
	emit := func(l buildlogs.Line) error { lines = append(lines, l); return nil }
	c := New(srv.URL)
	cursor, _, err := c.BuildLogs(context.Background(), Token{Value: "secret", TeamID: "team_test"}, "dpl_test", "", true, emit)
	if err != nil || cursor != "10001" || len(lines) != 3 || lines[0].ID == lines[1].ID || lines[0].Message != lines[1].Message || lines[2].Message != "build command" {
		t.Fatalf("follow output: %q %+v %v", cursor, lines, err)
	}
	lines = nil
	_, _, err = c.BuildLogs(context.Background(), Token{Value: "secret", TeamID: "team_test"}, "dpl_test", cursor, false, emit)
	if err != nil || len(lines) != 2 || lines[0].ID != "b" {
		t.Fatalf("stable replay: %+v %v", lines, err)
	}
}

func TestBuildLogsPermissionAndRateLimit(t *testing.T) {
	for _, code := range []int{403, 429} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("Retry-After", "2"); w.WriteHeader(code) }))
			defer srv.Close()
			_, _, err := New(srv.URL).BuildLogs(context.Background(), Token{Value: "secret"}, "dpl_test", "", false, func(buildlogs.Line) error { t.Fatal("unexpected output"); return nil })
			if buildlogs.Status(err) != code {
				t.Fatalf("status: %v", err)
			}
		})
	}
}
