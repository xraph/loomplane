package owned

import (
	"strings"
	"testing"
)

// leaks reports a piece of a secret, eight bytes or longer, that output
// shows.
func leaks(output string, secrets map[string]string) string {
	for _, secret := range secrets {
		for start := 0; start+8 <= len(secret); start++ {
			if piece := secret[start : start+8]; strings.Contains(output, piece) {
				return piece
			}
		}
	}
	return ""
}

// straddle writes a stream whose kept end starts inside long: in tails
// sized for secrets, long begins skip bytes before the start of what's
// kept, and after are the bytes that follow it.
func straddle(t *testing.T, secrets map[string]string, long string, skip int, after string) string {
	t.Helper()
	tail := NewJobTail(secrets)
	keep := tail.keep
	pad := keep - len(after) - len(long) + skip
	if pad < 0 {
		t.Fatalf("%d bytes after the secret are more than the tail keeps", len(after))
	}
	after = strings.Repeat(".", pad) + after
	_, _ = tail.Write([]byte(strings.Repeat("x", 3*keep) + "\n"))
	_, _ = tail.Write([]byte(long))
	_, _ = tail.Write([]byte(after))
	if got := string(tail.data[len(tail.data)-keep:]); got[:len(long)-skip] != long[skip:] {
		t.Fatalf("the kept output doesn't start inside the secret: %q", got[:20])
	}
	return tail.Output(secrets)
}

func TestJobOutputHidesASecretTheTailCuts(t *testing.T) {
	long := strings.Repeat("Z9q", 24) + "k4"
	secrets := map[string]string{"TOKEN": long}
	output := straddle(t, secrets, long, 10, "\nmigration 0003 failed\n")
	if piece := leaks(output, secrets); piece != "" {
		t.Fatalf("output shows %q of the secret", piece)
	}
	if !strings.HasSuffix(output, "migration 0003 failed\n") || len(output) > JobOutputLimit {
		t.Fatalf("output: %d bytes ending %q", len(output), output[max(0, len(output)-40):])
	}
}

func TestJobOutputHidesACutSecretAmongShrinkingRedactions(t *testing.T) {
	long := strings.Repeat("Lm7", 24) + "w2"
	short := "pw-" + strings.Repeat("R5", 9) + "x"
	secrets := map[string]string{"TOKEN": long, "PASSWORD": short}
	// Each short secret redacted shrinks the text by twelve bytes, which
	// would pull a cut made after redacting back into the cut secret.
	after := " tail\n" + strings.Repeat("login "+short+"\n", 400)
	output := straddle(t, secrets, long, 1, after)
	if piece := leaks(output, secrets); piece != "" {
		t.Fatalf("output shows %q of a secret", piece)
	}
	if !strings.Contains(output, "login [redacted]\n") || len(output) > JobOutputLimit {
		t.Fatalf("output: %d bytes", len(output))
	}
}

func TestJobOutputHidesAMultiLineSecretTheLineTailCuts(t *testing.T) {
	key := "-----BEGIN PRIVATE KEY-----\r\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\r\nBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj\r\nMzEfYyjiWA4R4/M2bS1GB4t7NXp98C3S\r\n-----END PRIVATE KEY-----"
	secrets := map[string]string{"TLS_KEY": key}
	// The daemon's line tail started two lines into the key.
	lines := strings.Split(key, "\n")
	cut := strings.Join(lines[2:], "\n") + "\nloaded the key\n"
	output := JobOutput([]byte(cut), secrets)
	if piece := leaks(output, secrets); piece != "" {
		t.Fatalf("output shows %q of the key", piece)
	}
	if !strings.HasSuffix(output, "loaded the key\n") {
		t.Fatalf("output: %q", output)
	}
	// Whole, it's redacted as one.
	if output = JobOutput([]byte("key: "+key+"\n"), secrets); output != "key: [redacted]\n" {
		t.Fatalf("whole key: %q", output)
	}
}

func TestJobOutputDropsTheFirstLineOfATailedStream(t *testing.T) {
	// A daemon's tail counts log entries, and a long line is several, so
	// what it returns can start inside a secret even when JobTail cut
	// nothing.
	secret := strings.Repeat("Qx4", 24)
	secrets := map[string]string{"TOKEN": secret}
	tail := NewJobTail(secrets)
	_, _ = tail.Write([]byte(secret[4:] + "\n"))
	_, _ = tail.Write([]byte("ok\nok\n"))
	if output := tail.Output(secrets); output != "ok\nok\n" {
		t.Fatalf("output: %q", output)
	}
	if piece := leaks(tail.Output(secrets), secrets); piece != "" {
		t.Fatalf("output shows %q of the secret", piece)
	}
}

func TestJobTailKeepsEachStreamsLinesWhole(t *testing.T) {
	secret := "pw-" + strings.Repeat("T8", 12)
	secrets := map[string]string{"PASSWORD": secret}
	tail := NewJobTail(secrets)
	// Each stream's first line can start anywhere, so it's dropped.
	tail.WriteStream(1, []byte(secret[6:]+"\n"))
	tail.WriteStream(2, []byte("tail of an earlier warning\n"))
	// A secret written to stdout in two frames, with stderr between them,
	// is still found whole.
	tail.WriteStream(1, []byte("login "+secret[:9]))
	tail.WriteStream(2, []byte("warning: slow\n"))
	tail.WriteStream(1, []byte(secret[9:]+"\n"))
	// A line too long to keep isn't shown, rather than shown from its
	// middle.
	tail.WriteStream(2, []byte(strings.Repeat("a", tail.keep+1)))
	tail.WriteStream(2, []byte(secret+"\nlast\n"))
	// The end of a stream with no line break is still the end of a line.
	tail.WriteStream(1, []byte("exiting"))
	want := "warning: slow\nlogin [redacted]\n[a line too long to show]\nlast\nexiting"
	if output := tail.Output(secrets); output != want {
		t.Fatalf("output: %q", output)
	}
}

func TestJobOutputRedactsAcrossInvalidBytes(t *testing.T) {
	secrets := map[string]string{"PASSWORD": "pw-" + strings.Repeat("V3", 10)}
	output := JobOutput([]byte("login pw-V3V3V3\xffV3V3V3V3V3V3V3\n"), secrets)
	if output != "login [redacted]\n" {
		t.Fatalf("output: %q", output)
	}
}
