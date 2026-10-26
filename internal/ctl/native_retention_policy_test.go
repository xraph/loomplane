package ctl

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestNativeRetentionPolicyCLIConfirmsAndPreservesPausedRule(t *testing.T) {
	writes := 0
	revision, enabled := 0, false
	app, _ := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/native-recovery-sources/source/retention-policy/history" {
			if r.URL.Query().Get("before") != "2" || r.URL.Query().Get("limit") != "1" {
				t.Error("native policy history lost cursor", r.URL)
			}
			fmt.Fprint(w, `{"events":[]}`)
			return
		}
		if r.URL.Path != "/api/v1/native-recovery-sources/source/retention-policy" {
			t.Error("wrong native policy source", r.URL)
		}
		if r.Method == http.MethodPut {
			writes++
			var in struct {
				Revision int  `json:"revision"`
				Enabled  bool `json:"enabled"`
				Policy   struct {
					Days int `json:"older_than_days"`
					Keep int `json:"keep_last"`
				} `json:"policy"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.Revision != revision || in.Policy.Days != 7 || in.Policy.Keep != 2 || in.Enabled != (writes == 1) {
				t.Error("native policy changed scope, revision or paused settings", in)
			}
			revision++
			enabled = in.Enabled
			fmt.Fprintf(w, `{"id":"policy","revision":%d,"enabled":%t,"policy":{"older_than_days":7,"keep_last":2}}`, revision, enabled)
			return
		}
		fmt.Fprintf(w, `{"configured":true,"policy":{"id":"policy","revision":%d,"enabled":%t,"policy":{"older_than_days":7,"keep_last":2}}}`, revision, enabled)
	})
	if err := app.Run([]string{"backups", "native-retention", "policy-set", "source"}); err == nil || writes != 0 {
		t.Fatal("native automatic deletion skipped confirmation", err, writes)
	}
	if err := app.Run([]string{"backups", "native-retention", "policy-set", "source", "--yes", "--revision", "0", "--older-than-days", "7", "--keep-last", "2", "--json"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run([]string{"backups", "native-retention", "policy", "source", "--json"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run([]string{"backups", "native-retention", "policy-pause", "source", "--yes", "--json"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run([]string{"backups", "native-retention", "policy-history", "source", "--before", "2", "--limit", "1", "--json"}); err != nil {
		t.Fatal(err)
	}
	if writes != 2 || revision != 2 || enabled {
		t.Fatal("native pause did not preserve its rule", writes, revision, enabled)
	}
}
