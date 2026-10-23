package ctl

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestNativeInventoryCLIKeepsReportedAndUnknownExpiry(t *testing.T) {
	for _, expiry := range []string{"", "2026-10-21T03:00:00Z"} {
		for _, jsonOutput := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", expiry, jsonOutput), func(t *testing.T) {
				app, out := recoveryCLI(t, func(w http.ResponseWriter, _ *http.Request) {
					field := ""
					if expiry != "" {
						field = fmt.Sprintf(`,"expires_at":%q`, expiry)
					}
					fmt.Fprintf(w, `{"source":{"id":"source","state":"ready"},"points":[{"id":"point","name":"daily","status":"SUCCESSFUL","created_at":"2026-10-21T02:00:00Z"%s}]}`, field)
				})
				args := []string{"backups", "native-inventory", "source"}
				if jsonOutput {
					args = append(args, "--json")
				}
				if err := app.Run(args); err != nil {
					t.Fatal(err)
				}
				wanted := "not reported"
				if expiry != "" {
					wanted = "2026-10-21 03:00Z"
					if jsonOutput {
						wanted = expiry
					}
				}
				if (!jsonOutput || expiry != "") && !strings.Contains(out.String(), wanted) {
					t.Fatalf("native expiry lost: %s", out.String())
				}
				if jsonOutput && expiry == "" && !strings.Contains(out.String(), `"expires_at": null`) {
					t.Fatal("unknown expiry cannot clear earlier cached metadata")
				}
			})
		}
	}
}

func TestNativeRecoveryCLIUsesScopedPagesAndExplicitRefresh(t *testing.T) {
	for _, command := range []string{"native-sources", "native-inventory", "native-refresh"} {
		t.Run(command, func(t *testing.T) {
			requests := 0
			app, out := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/projects" {
					_, _ = w.Write([]byte(`{"projects":[{"id":"project","slug":"database"}]}`))
					return
				}
				requests++
				if command == "native-sources" {
					if r.URL.Path != "/api/v1/projects/project/native-recovery-sources" || r.Method != http.MethodGet {
						t.Error("unscoped source request")
					}
					_, _ = w.Write([]byte(`{"sources":[],"next_before":"source-next"}`))
					return
				}
				if r.URL.Path != "/api/v1/native-recovery-sources/source"+map[string]string{"native-refresh": "/refresh"}[command] {
					t.Error("wrong inventory request", r.URL.Path)
				}
				if command == "native-refresh" && r.Method != http.MethodPost {
					t.Error("refresh not explicit")
				}
				if command == "native-inventory" && (r.Method != http.MethodGet || r.URL.Query().Get("before") != "point" || r.URL.Query().Get("limit") != "1") {
					t.Error("pagination lost")
				}
				_, _ = w.Write([]byte(`{"source":{"id":"source","state":"ready","complete":false},"points":[],"next_before":"point-next"}`))
			})
			argument := "source"
			if command == "native-sources" {
				argument = "database"
			}
			args := []string{"backups", command, argument, "--json"}
			if command == "native-inventory" {
				args = append(args, "--before", "point", "--limit", "1")
			}
			if err := app.Run(args); err != nil {
				t.Fatal(err)
			}
			if requests != 1 || !strings.Contains(out.String(), "next_before") {
				t.Fatalf("pagination metadata lost: %s", out.String())
			}
		})
	}
}
