package deployments

import (
	"strings"
	"testing"
)

func TestClusterBuildOutputRedactsSplitCredentialsAndSurvivesLogFailure(t *testing.T) {
	sink := &buildLogFixture{}
	job := &Job{Logs: sink}
	collector := job.Collector("build", "1")
	collector.addCredential([]byte("registry-password-value"))
	output := &clusterBuildOutput{ctx: t.Context(), collector: collector}
	for _, part := range []string{"pushing registry-pass", "word-value\n", strings.Repeat("x", 64<<10), "\n"} {
		if n, err := output.Write([]byte(part)); err != nil || n != len(part) {
			t.Fatalf("logging interrupted build: %v", err)
		}
	}
	if len(sink.logs) != 2 || strings.Contains(sink.logs[0].Message, "registry-password-value") || !sink.logs[1].Truncated {
		t.Fatalf("unsafe or unbounded logs: %d", len(sink.logs))
	}
	sink.fail = true
	if _, err := output.Write([]byte("cannot retain this log\n")); err != nil {
		t.Fatal(err)
	}
	collector.RecordState(t.Context(), "", "complete", "built")
	if sink.states[len(sink.states)-1].State != "error" {
		t.Fatal("log retention failure was hidden")
	}
}
