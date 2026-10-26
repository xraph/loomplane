package owned

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// leaks reports a sensitive piece of a secret that output shows outside
// its redactions.
func leaks(output string, secrets map[string]string) string {
	shown := strings.Split(output, "[redacted]")
	for _, value := range secrets {
		for _, piece := range sensitivePieces(value) {
			for _, text := range shown {
				if strings.Contains(text, piece) {
					return piece
				}
			}
		}
	}
	return ""
}

// tailed is output as a JobTail keeps it.
func tailed(secrets map[string]string, writes ...string) string {
	tail := NewJobTail(secrets)
	for _, w := range writes {
		_, _ = tail.Write([]byte(w))
	}
	return tail.Output(secrets)
}

const (
	token = "Qx4Lm7Z9qk4wR5T8pw-V3j2Hc6Nb1Yd0Fs9Ga8Kr7Ue6Wi5Oo4Pa3Sd2Df1Gh0Jk9Ll8Mn7Bv"
	dsn   = "postgresql://loomplane:pg-Secret-Value-17x@lp-inst-db:5432/app__postgres?sslmode=disable"
	pem   = "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\nBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj\nMzEfYyjiWA4R4/M2bS1GB4t7NXp98C3S\n-----END PRIVATE KEY-----"
)

func TestJobOutputHidesASecretATailStartsInside(t *testing.T) {
	secrets := map[string]string{"TOKEN": token}
	output := tailed(secrets, token[4:]+"\n", "ok\nok\n")
	if piece := leaks(output, secrets); piece != "" {
		t.Fatalf("output %q shows %q", output, piece)
	}
	if output != "[redacted]\nok\nok\n" {
		t.Fatalf("output: %q", output)
	}
}

func TestJobOutputHidesASecretARunningContainersLogEndsInside(t *testing.T) {
	// Reading a running container's log can stop partway through an
	// entry, and so partway through a secret.
	secrets := map[string]string{"TOKEN": token}
	output := tailed(secrets, strings.Repeat("x", 16384-23)+"\nauth "+token[:23])
	if piece := leaks(output, secrets); piece != "" {
		t.Fatalf("output shows %q", piece)
	}
	if !strings.HasSuffix(output, "\nauth [redacted]") || len(output) > JobOutputLimit {
		t.Fatalf("output ends %q", output[len(output)-20:])
	}
}

func TestJobOutputHidesASecretSplitByAnotherStream(t *testing.T) {
	// Kubernetes merges stdout and stderr: a stderr line can land between
	// two partial stdout entries of one secret.
	secrets := map[string]string{"TOKEN": token}
	output := tailed(secrets, "login "+token[:30], "warn: slow\n", token[30:]+"\n")
	if piece := leaks(output, secrets); piece != "" {
		t.Fatalf("output %q shows %q", output, piece)
	}
	if output != "login [redacted]warn: slow\n[redacted]\n" {
		t.Fatalf("output: %q", output)
	}
}

func TestJobOutputHidesAURLsPasswordAndNotTheRest(t *testing.T) {
	secrets := map[string]string{"DATABASE_URL": dsn}
	// Whole, the address is redacted as one.
	if output := JobOutput([]byte("using "+dsn+"\n"), secrets); output != "using [redacted]\n" {
		t.Fatalf("whole: %q", output)
	}
	// Split, only its password and path are. What of its path its scheme
	// spells too stays, so naming the scheme stays readable.
	at := strings.Index(dsn, "Value")
	output := tailed(secrets, "connecting to postgres://db:5432\n", "dial "+dsn[:at], "warn: slow\n", dsn[at:]+"\n")
	if piece := leaks(output, secrets); piece != "" {
		t.Fatalf("output %q shows %q", output, piece)
	}
	want := "connecting to postgres://db:5432\ndial postgresql://loomplane:[redacted]warn: slow\n[redacted]@lp-inst-db:5432[redacted]tgres?sslmode=disable\n"
	if output != want {
		t.Fatalf("output: %q", output)
	}
}

func TestJobOutputHidesAKeyCutAnywhere(t *testing.T) {
	secrets := map[string]string{"TLS_KEY": pem}
	for cut := range len(pem) {
		for _, output := range []string{
			tailed(secrets, pem[cut:]+"\nloaded\n"),
			tailed(secrets, "key: "+pem[:cut]),
		} {
			if piece := leaks(output, secrets); piece != "" {
				t.Fatalf("cut at %d: output %q shows %q", cut, output, piece)
			}
		}
	}
}

func TestJobOutputShowsAOneLineFailure(t *testing.T) {
	secrets := map[string]string{"TOKEN": token, "DATABASE_URL": dsn, "TLS_KEY": pem}
	const failure = "error: relation \"users\" already exists\n"
	if output := tailed(secrets, failure); output != failure {
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

// TestJobOutputNeverShowsFourBytesOfASecret writes secrets cut at random
// points, in random pieces between other output, and checks that no four
// bytes of any of them are shown, wherever the tail starts.
func TestJobOutputNeverShowsFourBytesOfASecret(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))
	secrets := map[string]string{"TOKEN": token, "DATABASE_URL": dsn, "TLS_KEY": pem}
	values := []string{token, dsn, pem}
	noise := []string{"warn: slow\n", "ok\n", "applying 0003", "\n", "connecting to postgres://db:5432\n", strings.Repeat("y", 4000)}
	for round := range 300 {
		var writes []string
		for range 2 + random.IntN(12) {
			value := values[random.IntN(len(values))]
			start := random.IntN(len(value))
			end := start + 1 + random.IntN(len(value)-start)
			piece := value[start:end]
			for piece != "" {
				n := 1 + random.IntN(len(piece))
				writes = append(writes, piece[:n], noise[random.IntN(len(noise))])
				piece = piece[n:]
			}
		}
		output := tailed(secrets, writes...)
		if piece := leaks(output, secrets); piece != "" {
			t.Fatalf("round %d: output shows %q", round, piece)
		}
	}
}
