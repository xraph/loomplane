package server

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/enginebackup"
	"github.com/xraph/loomplane/internal/clouds/owned"
)

func TestNativeRepositoryCopyBoundsAndTruncation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  int64
		data  string
		valid bool
	}{{"valid", 6, "native", true}, {"oversized", enginebackup.MaxPayload + 1, "", false}, {"truncated", 10, "short", false}} {
		t.Run(tc.name, func(t *testing.T) {
			var raw bytes.Buffer
			tw := tar.NewWriter(&raw)
			if err := tw.WriteHeader(&tar.Header{Name: "repo/index-0", Typeflag: tar.TypeReg, Size: tc.size}); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(tw, tc.data); err != nil {
				t.Fatal(err)
			}
			_ = tw.Close() // Deliberately incomplete archives exercise the copy boundary.
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(raw.Bytes())
			}))
			defer api.Close()
			client := &Client{Config: &Config{}, HTTP: api.Client()}
			client.HTTP.Transport = redirectDocker{base: api.URL, next: api.Client().Transport}
			err := downloadEngineDirectory(t.Context(), client, "source", "/snapshots/repo", io.Discard)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t: %v", tc.valid, err)
			}
			if tc.name == "oversized" && !strings.Contains(err.Error(), "size limit") {
				t.Fatalf("oversized entry reached copy: %v", err)
			}
		})
	}
}

func TestCheckpointBackupRestartsSourceAfterFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"success", "copy-failure", "cancelled", "unclean-shutdown"} {
		t.Run(mode, func(t *testing.T) {
			iid := id.New(id.PrefixInstance)
			labels := map[string]string{owned.LabelOrg: "org", owned.LabelProject: "project", owned.LabelEnvironment: "env"}
			running := true
			starts := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/containers/create":
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"Id":"lock"}`))
				case r.URL.Path == "/containers/lock" && r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusNoContent)
				case r.URL.Path == "/containers/lock/json":
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "lock", "Config": map[string]any{"Labels": map[string]string{owned.LabelInstance: iid.String(), owned.LabelOrg: "org", owned.LabelProject: "project", owned.LabelEnvironment: "env", "loomplane.engine.checkpoint-lock": "true"}}, "State": map[string]any{"Running": false}})
				case strings.HasSuffix(r.URL.Path, "/stop"):
					running = false
					w.WriteHeader(http.StatusNoContent)
				case strings.HasSuffix(r.URL.Path, "/start"):
					running = true
					starts++
					w.WriteHeader(http.StatusNoContent)
				case strings.HasSuffix(r.URL.Path, "/json"):
					exit := 0
					if mode == "unclean-shutdown" {
						exit = 137
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "owned", "Config": map[string]any{"Labels": map[string]string{owned.LabelInstance: iid.String(), owned.LabelOrg: "org", owned.LabelProject: "project", owned.LabelEnvironment: "env"}}, "State": map[string]any{"Running": running, "ExitCode": exit}})
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer api.Close()
			client := &Client{Config: &Config{}, HTTP: api.Client()}
			client.HTTP.Transport = redirectDocker{base: api.URL, next: api.Client().Transport}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			called := false
			runtime := Runtime{Wait: time.Second}
			err := runtime.stoppedEngine(ctx, client, iid, labels, func(context.Context) error {
				called = true
				if running {
					t.Fatal("copied live database")
				}
				if mode == "cancelled" {
					cancel()
					return context.Canceled
				}
				if mode == "copy-failure" {
					return errors.New("storage unavailable")
				}
				return nil
			})
			if !running || starts != 1 {
				t.Fatalf("source not resumed: running=%t starts=%d", running, starts)
			}
			if mode == "unclean-shutdown" && called {
				t.Fatal("copied database after forced shutdown")
			}
			if (mode == "success") != (err == nil) {
				t.Fatalf("wrong result: %v", err)
			}
		})
	}
}

type redirectDocker struct {
	base string
	next http.RoundTripper
}

func (r redirectDocker) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	parsed := *req.URL
	cloned.URL = &parsed
	cloned.URL.Scheme = "http"
	cloned.URL.Host = strings.TrimPrefix(r.base, "http://")
	next := r.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(cloned)
}
