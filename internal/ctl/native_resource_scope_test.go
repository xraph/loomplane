package ctl

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestNativeRecoveryListsCarryResourceScopeBeforePagination(t *testing.T) {
	for _, command := range []string{"native-sources", "native-copies"} {
		t.Run(command, func(t *testing.T) {
			reads := 0
			app, out := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/projects" {
					fmt.Fprint(w, `{"projects":[{"id":"project","slug":"database"}]}`)
					return
				}
				reads++
				path := "/api/v1/projects/project/native-recovery-" + map[string]string{"native-sources": "sources", "native-copies": "copies"}[command]
				if r.Method != http.MethodGet || r.URL.Path != path || r.URL.Query().Get("resource") != "orders & reports" || r.URL.Query().Get("environment_id") != "env" || r.URL.Query().Get("before") != "cursor/one" || r.URL.Query().Get("limit") != "1" {
					t.Error("lost scoped page", r.URL)
				}
				fmt.Fprint(w, `{"sources":[],"copies":[],"next_before":"next"}`)
			})
			if err := app.Run([]string{"backups", command, "database", "--resource", "orders & reports", "--environment-id", "env", "--before", "cursor/one", "--limit", "1", "--json"}); err != nil {
				t.Fatal(err)
			}
			if reads != 1 || !strings.Contains(out.String(), "next") {
				t.Fatal("lost next page", out)
			}
		})
	}
}
