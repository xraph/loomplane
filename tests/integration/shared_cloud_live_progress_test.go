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
	path   string
	Stage  string    `json:"stage"`
	Status string    `json:"status"`
	Code   string    `json:"code,omitempty"`
	At     time.Time `json:"at"`
}

func liveStageAllowed(stage string) bool {
	switch stage {
	case "credentials", "ledger", "preflight", "configuration", "repository_fixture", "database", "application_start", "signup", "organization_create", "organization_switch", "projects", "connections", "hosts", "dns_proofs", "routes", "migration", "future_release", "complete":
		return true
	default:
		return false
	}
}

func (p *liveProgress) write(stage, status, code string) error {
	if !liveStageAllowed(stage) || (status != "running" && status != "failed" && status != "passed") || (code != "" && code != "test_failed") {
		return fmt.Errorf("invalid diagnostic code")
	}
	p.Stage, p.Status, p.Code, p.At = stage, status, code, time.Now().UTC()
	return liveWrite(p.path, p)
}

func (p *liveProgress) step(t *testing.T, stage string) {
	t.Helper()
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
		}
		if err := p.write(p.Stage, status, code); err != nil {
			t.Error("persist final live stage failed")
		}
	})
	return p
}
