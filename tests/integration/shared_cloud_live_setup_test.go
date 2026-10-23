//go:build integration

package integration

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/config"
)

// Match the supervisor's repository-root cwd and configuration read, then stop
// after signup and organization activation. No provider connection is created.
func TestSharedCloudLiveSetupLocal(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source unavailable")
	}
	t.Chdir(filepath.Join(filepath.Dir(source), "..", ".."))
	if os.Getenv("LOOMPLANE_TEST_DATABASE_URL") == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL")
	}
	t.Setenv("LOOMPLANE_DATABASE_URL", os.Getenv("LOOMPLANE_TEST_DATABASE_URL"))
	t.Setenv("LOOMPLANE_SECRET_KEY", config.Defaults().SecretKey)
	if _, err := config.Load("."); err != nil {
		t.Fatal("primary read configuration invalid")
	}
	progress := newLiveProgress(t, t.TempDir())
	original := http.DefaultTransport
	http.DefaultTransport = &livePatchObserver{delegate: original, journal: filepath.Join(t.TempDir(), "dns-cutover.json")}
	t.Cleanup(func() { http.DefaultTransport = original })
	cfg, running, _, org := liveSetup(t, freshDatabase(t), "https://local-fixture.trycloudflare.com", "lp-live-0123456789abcdef", progress)
	defer running.stop()
	if org == "" {
		t.Fatal("organization missing")
	}
	key, err := base64.StdEncoding.DecodeString(cfg.SecretKey)
	if err != nil || len(key) != 32 {
		t.Fatal("isolated secret key contract violated")
	}
	clear(key)
	if err := cfg.Validate(); err != nil {
		t.Fatal("isolated configuration invalid")
	}
	cfg.SecretKey = strings.Repeat("00", 32)
	if cfg.Validate() == nil {
		t.Fatal("legacy hex key unexpectedly validates")
	}
	progress.step(t, "complete")
}

func TestSharedCloudLiveProgressFailure(t *testing.T) {
	directory := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), executable, "-test.run=^TestSharedCloudLiveProgressFailureChild$")
	child.Env = append(os.Environ(), "LOOMPLANE_PROGRESS_FAILURE_TEST="+directory)
	if child.Run() == nil {
		t.Fatal("failure fixture unexpectedly passed")
	}
	raw, err := os.ReadFile(filepath.Join(directory, "run-progress.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt liveProgress
	if err = json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Stage != "repository_fixture" || receipt.Status != "failed" || receipt.Code != "test_failed" {
		t.Fatalf("incorrect diagnostic codes: %+v", receipt)
	}
	if strings.Contains(string(raw), "secret-response-body") {
		t.Fatal("raw error leaked")
	}
	retry := exec.CommandContext(t.Context(), executable, "-test.run=^TestSharedCloudLiveProgressFailureChild$")
	retry.Env = child.Env
	if retry.Run() == nil {
		t.Fatal("existing diagnostic receipt accepted")
	}
	retained, err := os.ReadFile(filepath.Join(directory, "run-progress.json"))
	if err != nil || string(retained) != string(raw) {
		t.Fatal("failed run diagnostics overwritten")
	}
}

func TestSharedCloudLiveProgressFailureChild(t *testing.T) {
	directory := os.Getenv("LOOMPLANE_PROGRESS_FAILURE_TEST")
	if directory == "" {
		t.Skip("subprocess fixture only")
	}
	progress := newLiveProgress(t, directory)
	progress.step(t, "repository_fixture")
	t.Fatal("secret-response-body")
}

func TestSharedCloudLiveProgressRejectsRawText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.json")
	progress := &liveProgress{path: path}
	for _, values := range [][3]string{{"secret-response-body", "failed", "test_failed"}, {"signup", "secret-response-body", ""}, {"signup", "failed", "secret-response-body"}} {
		if err := progress.write(values[0], values[1], values[2]); err == nil {
			t.Fatal("raw text accepted")
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("untrusted diagnostic persisted")
	}
}
