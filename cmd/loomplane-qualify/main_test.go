package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/models/qualify"
	"github.com/xraph/loomplane/internal/models/registry"
)

// env is a fake environment.
func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

const config = "../../.github/qualification.yaml"

func TestRunNeedsConfirm(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// No token, no account: the refusal comes first, before either is read.
	code := run(t.Context(), []string{"run", "--config", config, "--out", filepath.Join(t.TempDir(), "r.json")}, env(nil), &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "--confirm") {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
}

func TestRunNeedsTheAccountAndToken(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{"run", "--config", config, "--confirm"}, env(nil), &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "QUALIFY_DO_TOKEN") {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
	stderr.Reset()
	code = run(t.Context(), []string{"sweep", "--config", config}, env(map[string]string{"QUALIFY_DO_TOKEN": "x"}), &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "account_uuid is empty") {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
}

func TestRunIDFlag(t *testing.T) {
	for _, cmd := range []string{"run", "sweep"} {
		var stdout, stderr bytes.Buffer
		args := []string{cmd, "--config", config, "--confirm", "--run-id", "gh-1-1; rm -rf /"}
		if cmd == "sweep" {
			args = []string{cmd, "--config", config, "--run-id", "gh-1-1; rm -rf /"}
		}
		if code := run(t.Context(), args, env(nil), &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "isn't a run ID") {
			t.Fatalf("%s: code %d, stderr %q", cmd, code, stderr.String())
		}
	}
	// A good ID gets as far as the token, which the test doesn't have.
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{"run", "--config", config, "--confirm", "--run-id", "gh-123456-2"}, env(nil), &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "QUALIFY_DO_TOKEN") {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
}

func TestPlanSpendsNothing(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), []string{"plan", "--config", config, "--week", "2026-W47"}, env(nil), &stdout, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	out := stdout.String()
	// 4.41 an hour for 4 hours plus half an hour of teardown.
	if !strings.Contains(out, "runs on nvidia-h100-80gb") || !strings.Contains(out, "Worst case: $19.8") || !strings.Contains(out, "for 4.5 hours with teardown") ||
		!strings.Contains(out, "Caps: $150.00 a month, $25.00 a run.") {
		t.Fatalf("plan:\n%s", out)
	}
}

func TestReadSpeech(t *testing.T) {
	dir := t.TempDir()
	if clip, err := readSpeech(filepath.Join(dir, "speech.wav")); err != nil || clip != nil {
		t.Fatalf("a missing clip: %v, %d bytes", err, len(clip))
	}
	bad := filepath.Join(dir, "bad.wav")
	if err := os.WriteFile(bad, []byte("not audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSpeech(bad); err == nil {
		t.Fatal("a clip that isn't a WAV was accepted")
	}
}

func TestMergeCommand(t *testing.T) {
	data := t.TempDir()
	src := "../../internal/models/registry/data"
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(data, e.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r := registry.Default()
	e, _ := r.Entry("BAAI/bge-m3")
	q, _ := e.Quantization("native")
	_, revision := q.WeightsRepo(e)
	good := registry.Result{
		Entry: e.ID, Quantization: q.ID, Revision: revision, GPUKind: registry.CPUKind, MaxModelLen: 8192, Engine: q.Engine,
		EngineVersion: "1.9.4", Outcome: registry.OutcomePassed, Detail: "2 vectors of 1024 dimensions in 40 ms",
		RanAt: time.Date(2026, 11, 9, 7, 0, 0, 0, time.UTC), Run: "https://github.com/xraph/loomplane/actions/runs/9",
	}
	write := func(results ...registry.Result) string {
		path := filepath.Join(t.TempDir(), "results.json")
		raw, _ := json.Marshal(qualify.Report{Kind: "nvidia-l40s", Week: "2026-W46", Results: results})
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	file := filepath.Join(data, "qualification.yaml")
	summary := filepath.Join(t.TempDir(), "summary.md")
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), []string{"merge", "--data", file, "--summary", summary, write(good)}, env(nil), &stdout, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	merged, _ := os.ReadFile(file)
	if !strings.Contains(string(merged), "entry: BAAI/bge-m3") {
		t.Fatalf("merged:\n%s", merged)
	}
	if body, _ := os.ReadFile(summary); !strings.Contains(string(body), "### Passed (1)") {
		t.Fatalf("summary:\n%s", body)
	}

	// A result the registry would refuse is never written.
	bad := good
	bad.Entry = "nobody/nothing"
	stderr.Reset()
	if code := run(t.Context(), []string{"merge", "--data", file, write(bad)}, env(nil), &stdout, &stderr); code != 1 {
		t.Fatalf("code %d", code)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(after, merged) {
		t.Fatal("a bad merge changed the file")
	}
}
