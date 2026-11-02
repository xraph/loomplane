//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedCloudLiveDNSFailureStages(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"dns_namespace_create", "dns_proof_publish", "dns_proof_verify"} {
		t.Run(stage, func(t *testing.T) {
			directory := t.TempDir()
			child := exec.CommandContext(t.Context(), executable, "-test.run=^TestSharedCloudLiveDNSFailureChild$")
			child.Env = append(os.Environ(), "LOOMPLANE_DNS_FAILURE_DIR="+directory, "LOOMPLANE_DNS_FAILURE_STAGE="+stage)
			if child.Run() == nil {
				t.Fatal("failure child passed")
			}
			raw, readErr := os.ReadFile(filepath.Join(directory, "run-progress.json"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			var progress liveProgress
			if err := json.Unmarshal(raw, &progress); err != nil {
				t.Fatal(err)
			}
			if progress.Stage != stage || progress.Status != "failed" {
				t.Fatalf("wrong failure boundary: %+v", progress)
			}
			wantStatus, wantCode := http.StatusInternalServerError, "http_request_failed"
			if stage == "dns_proof_publish" {
				wantStatus, wantCode = 0, "test_failed"
			}
			if progress.HTTPStatus != wantStatus || progress.Code != wantCode {
				t.Fatalf("wrong fixed diagnostics: %+v", progress)
			}
			if strings.Contains(string(raw), "secret") {
				t.Fatal("raw failure persisted")
			}
		})
	}
}
func TestSharedCloudLiveDNSFailureChild(t *testing.T) {
	directory := os.Getenv("LOOMPLANE_DNS_FAILURE_DIR")
	if directory == "" {
		t.Skip("subprocess fixture only")
	}
	progress := newLiveProgress(t, directory)
	progress.observeHTTP(t, http.StatusCreated)
	stage := os.Getenv("LOOMPLANE_DNS_FAILURE_STAGE")
	progress.step(t, stage)
	if stage == "dns_proof_publish" {
		t.Fatal("secret-provider-error")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "secret-response-body", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := &client{t: t, base: srv.URL, observeStatus: func(status int) { progress.observeHTTP(t, status) }}
	c.do(http.MethodPost, "/fixture", nil, nil)
}
