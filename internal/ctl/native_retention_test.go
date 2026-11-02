package ctl

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestNativeRetentionCLIRequiresExplicitDeletionAndKeepsRequest(t *testing.T) {
	writes, reads := 0, 0
	app, out := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/native-recovery-sources/source/retention-preview":
			reads++
			if r.URL.Query().Get("older_than_days") != "7" || r.URL.Query().Get("keep_last") != "2" {
				t.Error("native retention settings lost", r.URL)
			}
			fmt.Fprint(w, `{"complete":true,"eligible_count":1,"verified_backup_id":"witness","points":[{"backup_id":"old","eligible":true,"reason":"eligible"}]}`)
		case "POST /api/v1/native-recovery-sources/source/points/old/retirements":
			writes++
			var in map[string]any
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in["request_id"] != "stable-native-retire" || in["acknowledge_deletion"] != true {
				t.Error("native deletion lost acknowledgment or request", in)
			}
			policy, ok := in["policy"].(map[string]any)
			if !ok || policy["older_than_days"] != float64(7) || policy["keep_last"] != float64(2) {
				t.Error("native deletion changed retention", in)
			}
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"id":"retirement","status":"queued","witness_id":"witness"}`)
		case "GET /api/v1/native-recovery-retirements/retirement":
			fmt.Fprint(w, `{"id":"retirement","status":"succeeded","witness_id":"witness"}`)
		case "POST /api/v1/native-recovery-retirements/retirement/retry":
			writes++
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"id":"retirement","status":"queued","witness_id":"witness"}`)
		case "GET /api/v1/native-recovery-retirements/retirement/history":
			if r.URL.Query().Get("before") != "12" || r.URL.Query().Get("limit") != "2" {
				t.Error("native history cursor lost", r.URL)
			}
			fmt.Fprint(w, `{"events":[{"kind":"admitted","actor":"owner","status":"queued"}]}`)
		default:
			t.Error("unexpected native retention request", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	if err := app.Run([]string{"backups", "native-retention", "preview", "source", "--older-than-days", "7", "--keep-last", "2", "--json"}); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || !strings.Contains(out.String(), "witness") {
		t.Fatal("native retention preview missing", out.String())
	}
	if err := app.Run([]string{"backups", "native-retention", "delete", "source", "old"}); err == nil || writes != 0 {
		t.Fatal("native deletion skipped explicit confirmation", err, writes)
	}
	if err := app.Run([]string{"backups", "native-retention", "delete", "source", "old", "--yes", "--older-than-days", "7", "--keep-last", "2", "--request-id", "stable-native-retire", "--wait", "--json"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run([]string{"backups", "native-retention", "retry", "retirement", "--yes", "--wait"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run([]string{"backups", "native-retention", "history", "retirement", "--before", "12", "--limit", "2", "--json"}); err != nil {
		t.Fatal(err)
	}
	if writes != 2 {
		t.Fatal("native CLI repeated deletion", writes)
	}
}
