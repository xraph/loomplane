package deployments

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestImageBuildOutput_preservesRepeatsAndReportsDaemonErrors(t *testing.T) {
	var lines []buildLine
	_, err := readBuildOutput(strings.NewReader("{\"stream\":\"same line\\n\"}\n{\"stream\":\"same line\\n\"}\n{\"errorDetail\":{\"message\":\"compile failed\"},\"error\":\"compile failed\"}\n"), func(line buildLine) error { lines = append(lines, line); return nil })
	if err == nil || !strings.Contains(err.Error(), "compile failed") {
		t.Fatalf("daemon error: %v", err)
	}
	if len(lines) < 2 || lines[0].Message != lines[1].Message || lines[0].ID == lines[1].ID {
		t.Fatalf("repeated output: %+v", lines)
	}
}

func TestImageBuildOutput_logPersistenceFailureDoesNotAbortACompletedBuild(t *testing.T) {
	id, err := readBuildOutput(strings.NewReader("{\"stream\":\"line\\n\"}\n{\"id\":\"moby.image.id\",\"aux\":{\"ID\":\"sha256:fixture\"}}\n"), func(buildLine) error { return errors.New("log store unavailable") })
	if err != nil || id != "sha256:fixture" {
		t.Fatalf("output failure aborted build: %q %v", id, err)
	}
}

func TestImageBuildOutput_decodesRecordedBuildKitStdoutAndStderr(t *testing.T) {
	file, err := os.Open("testdata/buildkit-output.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var lines []buildLine
	id, err := readBuildOutput(file, func(line buildLine) error { lines = append(lines, line); return nil })
	if err != nil || id == "" {
		t.Fatalf("recorded build: %q %v", id, err)
	}
	stdout, stderr := false, false
	for _, line := range lines {
		stdout = stdout || (line.Stream == "stdout" && strings.Contains(line.Message, "loomplane-build-stdout"))
		stderr = stderr || (line.Stream == "stderr" && strings.Contains(line.Message, "loomplane-build-stderr"))
	}
	if !stdout || !stderr {
		t.Fatalf("BuildKit streams missing: %+v", lines)
	}
}
