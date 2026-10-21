package ctl

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestRetentionPolicyMutationRequiresConfirmationAndValidSettingsBeforeNetwork(t *testing.T) {
	calls := 0
	app, _ := recoveryCLI(t, func(http.ResponseWriter, *http.Request) { calls++ })
	for _, args := range [][]string{{"backups", "retention-set", "b"}, {"backups", "retention-pause", "b"}} {
		if err := app.Run(args); err == nil || !strings.Contains(err.Error(), "--yes") || calls != 0 {
			t.Fatalf("unconfirmed policy reached network: %v calls=%d err=%v", args, calls, err)
		}
	}
	for _, args := range [][]string{{"backups", "retention-history", "b", "--before", "oops"}, {"backups", "retention-history", "b", "--before", "0"}, {"backups", "retention-history", "b", "--before", "01"}, {"backups", "retention-set", "b", "--yes", "--revision", "-1"}, {"backups", "retention-set", "b", "--yes", "--revision", "oops"}, {"backups", "retention-set", "b", "--yes", "--older-than-days", "0"}, {"backups", "retention-history", "b", "--limit", "101"}} {
		if err := app.Run(args); err == nil || calls != 0 {
			t.Fatalf("invalid policy reached network: %v calls=%d err=%v", args, calls, err)
		}
	}
}
func TestRetentionPolicySetKeepsObservedRevisionAndPauseKeepsSettings(t *testing.T) {
	for _, pause := range []bool{false, true} {
		t.Run(map[bool]string{false: "enable", true: "pause"}[pause], func(t *testing.T) {
			calls := 0
			app, out := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/v1/service-backups/b/retention-policy" {
					t.Errorf("unexpected policy path: %s", r.URL)
					http.NotFound(w, r)
					return
				}
				if r.Method == http.MethodGet && pause {
					_, _ = w.Write([]byte(`{"configured":true,"policy":{"id":"policy","revision":3,"policy":{"older_than_days":30,"keep_last":2},"enabled":true}}`))
					return
				}
				var body struct {
					Revision int64 `json:"revision"`
					Enabled  bool  `json:"enabled"`
					Policy   struct {
						Days int `json:"older_than_days"`
						Keep int `json:"keep_last"`
					} `json:"policy"`
				}
				if r.Method != http.MethodPut || json.NewDecoder(r.Body).Decode(&body) != nil || body.Revision != 3 || body.Enabled == pause || body.Policy.Days != 30 || body.Policy.Keep != 2 {
					t.Errorf("lost policy decision: %s %+v", r.Method, body)
				}
				_, _ = w.Write([]byte(`{"id":"policy","revision":4,"enabled":false,"policy":{"older_than_days":30,"keep_last":2}}`))
			})
			args := []string{"backups", "retention-set", "b", "--yes", "--revision", "3", "--older-than-days", "30", "--keep-last", "2", "--json"}
			if pause {
				args = []string{"backups", "retention-pause", "b", "--yes", "--json"}
			}
			err := app.Run(args)
			expected := 1
			if pause {
				expected = 2
			}
			if err != nil || calls != expected || !strings.Contains(out.String(), `"revision": 4`) {
				t.Fatalf("policy save: calls=%d err=%v output=%s", calls, err, out.String())
			}
		})
	}
}

func TestRetentionPolicyPauseDoesNotWriteAnUnsetOrMalformedPolicy(t *testing.T) {
	for _, body := range []string{`{"configured":false}`, `{"configured":true}`} {
		calls := 0
		app, out := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.Method != http.MethodGet {
				t.Errorf("pause wrote without a policy: %s", r.Method)
			}
			_, _ = w.Write([]byte(body))
		})
		err := app.Run([]string{"backups", "retention-pause", "b", "--yes", "--json"})
		if calls != 1 || (err != nil) != (body == `{"configured":true}`) {
			t.Fatalf("unset/malformed pause: calls=%d err=%v output=%s", calls, err, out.String())
		}
	}
}
func TestRetentionPolicyConflictingSavePreservesErrorAndInspectionPath(t *testing.T) {
	calls := 0
	app, _ := recoveryCLI(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, `{"error":"conflict"}`, http.StatusConflict)
	})
	err := app.Run([]string{"backups", "retention-set", "b", "--yes", "--revision", "3"})
	if err == nil || calls != 1 || !strings.Contains(err.Error(), "backups retention-policy b") {
		t.Fatalf("conflict lost recovery path: %d %v", calls, err)
	}
}
