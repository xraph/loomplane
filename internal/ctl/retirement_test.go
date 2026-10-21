package ctl

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/client"
)

func TestRetirementCommandsRequireConfirmationBeforeCallingAPI(t *testing.T) {
	calls := 0
	app, _ := recoveryCLI(t, func(http.ResponseWriter, *http.Request) { calls++ })
	for _, args := range [][]string{{"backups", "retire", "candidate"}, {"retirements", "retry", "retirement"}} {
		if err := app.Run(args); err == nil || !strings.Contains(err.Error(), "--yes") || calls != 0 {
			t.Fatalf("unconfirmed deletion reached API: %v calls=%d err=%v", args, calls, err)
		}
	}
	for _, args := range [][]string{
		{"backups", "retire", "candidate", "--yes", "--older-than-days", "0"},
		{"backups", "retire", "candidate", "--yes", "--keep-last", "0"},
		{"backups", "retire", "candidate", "--yes", "--wait", "--timeout", "0"},
		{"retirements", "retry", "retirement", "--yes", "--wait", "--timeout", "0"},
		{"retirements", "history", "retirement", "--limit", "101"},
	} {
		if err := app.Run(args); err == nil || calls != 0 {
			t.Fatalf("invalid retirement reached API: %v calls=%d err=%v", args, calls, err)
		}
	}
}

func TestRetirementCommandPreservesRequestAndFollowsResult(t *testing.T) {
	previous := client.Every
	client.Every = time.Millisecond
	t.Cleanup(func() { client.Every = previous })
	posts := 0
	app, out := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/service-backups/candidate/retire":
			var body struct {
				RequestID string `json:"request_id"`
				Policy    struct {
					Days int `json:"older_than_days"`
					Keep int `json:"keep_last"`
				} `json:"policy"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.RequestID != "retirement-request" || body.Policy.Days != 7 || body.Policy.Keep != 1 {
				t.Errorf("lost selection settings: %+v", body)
			}
			posts++
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"retirement","status":"queued","backup_id":"candidate"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/service-backup-retirements/retirement":
			_, _ = w.Write([]byte(`{"id":"retirement","status":"succeeded","backup_id":"candidate"}`))
		default:
			t.Errorf("unexpected retirement request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	err := app.Run([]string{"backups", "retire", "candidate", "--yes", "--older-than-days", "7", "--keep-last", "1", "--request-id", "retirement-request", "--wait", "--json"})
	if err != nil || posts != 1 || !strings.Contains(out.String(), `"status": "succeeded"`) {
		t.Fatalf("retirement result: posts=%d err=%v output=%s", posts, err, out.String())
	}
}

func TestRetirementWaitTimeoutKeepsAcceptedRecord(t *testing.T) {
	previous := client.Every
	client.Every = time.Millisecond
	t.Cleanup(func() { client.Every = previous })
	for _, args := range [][]string{
		{"backups", "retire", "candidate", "--yes", "--older-than-days", "7", "--keep-last", "1"},
		{"retirements", "retry", "retirement", "--yes"},
	} {
		app, out := recoveryCLI(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"id":"accepted_retirement","status":"running"}`))
		})
		err := app.Run(append(args, "--request-id", "stable-request-key", "--wait", "--timeout", "20ms", "--json"))
		var result struct {
			ID string `json:"id"`
		}
		if err == nil || !strings.Contains(err.Error(), "retirements show") || json.Unmarshal(out.Bytes(), &result) != nil || result.ID != "accepted_retirement" {
			t.Fatalf("timeout lost accepted retirement: %v output=%s", err, out.String())
		}
	}
}
