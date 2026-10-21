//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
)

func TestCloudSQLRemovalKeepsCredentialsUntilProtectedCompletion(t *testing.T) {
	for _, state := range []string{"completed", "refused", "backup-failed", "missing"} {
		t.Run(state, func(t *testing.T) {
			google := newFakeGoogle(t)
			google.deploy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != http.MethodDelete || r.URL.Path != "/v1/projects/acme-prod/instances/database" {
					t.Errorf("unexpected cloud request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if r.URL.Query().Get("enableFinalBackup") != "true" || r.URL.Query().Get("finalBackupTtlDays") != "30" {
					t.Error("executor requested removal without the final backup")
				}
				switch state {
				case "refused":
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, `{"error":{"message":"final backup refused"}}`)
				case "backup-failed":
					fmt.Fprint(w, `{"name":"remove-operation","status":"DONE","error":{"errors":[{"message":"final backup failed"}]}}`)
				case "missing":
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprint(w, `{"error":{"message":"already gone"}}`)
				default:
					fmt.Fprint(w, `{"name":"remove-operation","status":"DONE"}`)
				}
			})
			forgotten := false
			job := checkpointSQLJob(t, google)
			job.Checkpoint = func([]byte) error { return nil }
			job.Forget = func(context.Context) error {
				forgotten = true
				return nil
			}
			executor := &deployments.CloudSQL{Google: gcp.New(google.srv.URL)}
			err := executor.Remove(t.Context(), job, &planning.Binding{ExternalID: "acme-prod:us-central1:database"})
			failed := state == "refused" || state == "backup-failed"
			if failed {
				if err == nil || !strings.Contains(err.Error(), "final backup") || forgotten {
					t.Fatalf("backup refusal lost credentials or failure: forgotten=%v err=%v", forgotten, err)
				}
			} else if err != nil || !forgotten {
				t.Fatalf("completed removal did not release credentials: forgotten=%v err=%v", forgotten, err)
			}
		})
	}
}
