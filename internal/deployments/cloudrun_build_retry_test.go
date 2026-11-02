package deployments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/gcp"
)

func TestCloudRunExplicitRetryRestartsOnlyTerminalFailedBuild(t *testing.T) {
	for _, status := range []string{"FAILURE", "INTERNAL_ERROR", "TIMEOUT", "CANCELLED", "EXPIRED", "SUCCESS", "WORKING", "denied", "missing", "same-retry", "retry-fails"} {
		t.Run(status, func(t *testing.T) {
			var starts, reads atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/token":
					_, _ = io.WriteString(w, `{"access_token":"fixture","token_type":"Bearer","expires_in":3600}`)
				case strings.HasSuffix(r.URL.Path, "/builds/original"):
					count := reads.Add(1)
					if status == "denied" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					if status == "missing" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					current := status
					if status == "same-retry" || status == "retry-fails" {
						current = "FAILURE"
					}
					if status == "WORKING" && count > 1 {
						current = "SUCCESS"
					}
					_, _ = fmt.Fprintf(w, `{"id":"original","status":%q}`, current)
				case strings.HasSuffix(r.URL.Path, "/builds/retried"):
					current := "SUCCESS"
					if status == "retry-fails" {
						current = "FAILURE"
					}
					_, _ = fmt.Fprintf(w, `{"id":"retried","status":%q}`, current)
				case strings.HasSuffix(r.URL.Path, "/builds") && r.Method == http.MethodPost:
					starts.Add(1)
					_, _ = io.WriteString(w, `{"metadata":{"build":{"id":"retried"}}}`)
				case strings.Contains(r.URL.Path, "/tags/"):
					// Artifact Registry names the digest the build pushed.
					_, _ = io.WriteString(w, `{"version":"x/versions/sha256:`+strings.Repeat("ab", 32)+`"}`)
				default:
					_, _ = io.WriteString(w, `{}`)
				}
			}))
			defer srv.Close()
			job := cloudRunPSCJob(t)
			job.Deployment.Retries = 1
			if status == "same-retry" {
				job.Deployment.Retries = 0
			}
			job.Attempt = "2.1"
			job.SourceID = "0123456789abcdef"
			job.Action.Service.Image = "services/api/Dockerfile"
			source := monoTarball(t)
			job.Source = func(context.Context) (io.ReadCloser, int64, error) {
				return io.NopCloser(bytes.NewReader(source)), int64(len(source)), nil
			}
			job.Progress = func(string, string) {}
			google := gcp.New(srv.URL)
			key, err := gcp.ParseKey(serviceAccountKey(t, srv.URL+"/token"))
			if err != nil {
				t.Fatal(err)
			}
			session, err := google.Session(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			witness := &cloudRunPSCState{}
			state := cloudRunState{BuildID: "original", LogAttempt: "1.1", PSC: witness}
			var saved []byte
			save := func() {
				var encodeErr error
				saved, encodeErr = json.Marshal(state)
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
			}
			_, err = (&CloudRun{Google: google}).build(t.Context(), session, job, &state, save)
			if status == "denied" || status == "missing" || status == "same-retry" {
				if err == nil || starts.Load() != 0 || state.BuildID != "original" {
					t.Fatal("uncertain or same-attempt build was restarted", err, starts.Load(), state.BuildID)
				}
				return
			}
			if status == "retry-fails" {
				if err == nil || starts.Load() != 1 {
					t.Fatal("retry did not reach one new failed build", err, starts.Load())
				}
				if _, againErr := (&CloudRun{Google: google}).build(t.Context(), session, job, &state, save); againErr == nil || starts.Load() != 1 {
					t.Fatal("activity replay restarted a terminal build again", againErr, starts.Load())
				}
				return
			}
			wantStarts := int32(1)
			if status == "SUCCESS" || status == "WORKING" {
				wantStarts = 0
			}
			if err != nil || starts.Load() != wantStarts || state.PSC != witness {
				t.Fatal("explicit build retry lost progress or route", err, starts.Load(), wantStarts)
			}
			if wantStarts == 1 && (state.BuildID != "retried" || state.LogAttempt != "2.1" || len(saved) == 0) {
				t.Fatal("new build checkpoint was not captured", state.BuildID, state.LogAttempt)
			}
		})
	}
}
