package realtime

import (
	"strings"
	"testing"
)

func TestRedactor_removesSecretsAndTerminalCommandsBeforeTruncating(t *testing.T) {
	r := NewRedactor([]string{"private-token", "first-line\nsecond-line"})
	got, truncated := r.Line("\x1b[31mprivate-token\x1b[0m\x1b]8;;https://untrusted.example\x07link\x1b]8;;\x07 first-line\x00", MaxLineBytes)
	if truncated || strings.Contains(got, "private-token") || strings.Contains(got, "first-line") || strings.ContainsAny(got, "\x1b\x00\x07") {
		t.Fatalf("unsafe persisted output: %q", got)
	}
	got, truncated = r.Line(strings.Repeat("x", MaxLineBytes+1), MaxLineBytes)
	if !truncated || len(got) > MaxLineBytes {
		t.Fatalf("line bound: %d %v", len(got), truncated)
	}
}
