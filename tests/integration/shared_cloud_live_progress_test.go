//go:build integration

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// liveProgress persists fixed diagnostic codes without request bodies, keys,
// provider errors or application logs. The supervisor separately records exit status.
type liveProgress struct {
	path       string
	Stage      string    `json:"stage"`
	Status     string    `json:"status"`
	Code       string    `json:"code,omitempty"`
	At         time.Time `json:"at"`
	HTTPStatus int       `json:"http_status,omitempty"`
}

func liveStageAllowed(stage string) bool {
	switch stage {
	case "plan_create", "plan_wait", "plan_approve", "deployment_wait", "binding_read", "route_preview", "route_apply", "route_wait", "public_https", "migration_preview", "migration_apply", "migration_wait", "credentials", "ledger", "preflight", "configuration", "repository_fixture", "database", "application_start", "signup", "organization_create", "organization_switch", "projects", "connections", "hosts", "host_preview", "host_apply", "host_wait", "host_runtime", "host_pool", "dns_proofs", "dns_namespace_create", "dns_proof_publish", "dns_proof_verify", "routes", "migration", "future_release", "complete":
		return true
	default:
		return false
	}
}

func (p *liveProgress) write(stage, status, code string) error {
	if !liveStageAllowed(stage) || (status != "running" && status != "failed" && status != "passed") || !liveFailureAllowed(code) {
		return fmt.Errorf("invalid diagnostic code")
	}
	p.Stage, p.Status, p.Code, p.At = stage, status, code, time.Now().UTC()
	return liveWrite(p.path, p)
}

func (p *liveProgress) step(t *testing.T, stage string) {
	t.Helper()
	p.HTTPStatus = 0
	if err := p.write(stage, "running", ""); err != nil {
		t.Fatal("persist live stage failed")
	}
}

func newLiveProgress(t *testing.T, directory string) *liveProgress {
	t.Helper()
	p := &liveProgress{path: filepath.Join(directory, "run-progress.json")}
	receipt, err := os.OpenFile(p.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal("live diagnostics already exist or cannot be created")
	}
	if err = receipt.Close(); err != nil {
		t.Fatal("close live diagnostics failed")
	}
	p.step(t, "credentials")
	t.Cleanup(func() {
		status, code := "passed", ""
		if t.Failed() {
			status, code = "failed", "test_failed"
			if p.Code != "" {
				code = p.Code
			}
		}
		if err := p.write(p.Stage, status, code); err != nil {
			t.Error("persist final live stage failed")
		}
	})
	return p
}

func (p *liveProgress) observeHTTP(t *testing.T, status int) {
	t.Helper()
	if status < 100 || status > 599 {
		t.Fatal("invalid HTTP diagnostic status")
	}
	p.HTTPStatus = status
	code := ""
	if status >= 400 {
		code = "http_request_failed"
	}
	if err := p.write(p.Stage, "running", code); err != nil {
		t.Fatal("persist HTTP diagnostic failed")
	}
}

func liveFailureAllowed(code string) bool {
	switch code {
	case "", "assignment_failed", "test_failed", "http_request_failed", "plan_not_executable", "deployment_failed", "deployment_canceled", "deployment_deadline", "binding_missing", "route_retry_failed", "route_failed", "route_deadline", "https_failed", "dns_backend_mismatch", "migration_failed", "migration_deadline", "release_binding_mismatch", "release_route_mismatch", "migration_scope_mismatch", "migration_retention_failed", "migration_inputs_changed", "migration_dns_mismatch":
		return true
	default:
		return false
	}
}

func (p *liveProgress) failure(t *testing.T, code string) {
	t.Helper()
	if err := p.write(p.Stage, "running", code); err != nil {
		t.Log("failure diagnostic unavailable")
	}
}
