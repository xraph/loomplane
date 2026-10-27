package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// A deployment checks a database it just started by running its native tool
// with docker exec, under the deployment's effect fence. Docker answers an
// attached exec start with a close-delimited stream, so the helper has to
// frame it and wait for the exec to finish. Before, it treated the stream
// like any other unframed response, left the daemon journal dirty, and every
// later deployment to that daemon failed to establish quiescence.
func TestEffectHelperCompletesFencedDatabaseExec(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 helper")
	}
	for _, tc := range []struct {
		name                 string
		frame                string
		running, detach, tty bool
		// exitPending answers the first inspect after the stream with the
		// exec still running and no exit code, as Docker does until it has
		// processed the exit event.
		exitPending              bool
		clean, completesResponse bool
	}{
		{name: "complete", frame: "\x01\x00\x00\x00\x00\x00\x00\x02ok", clean: true, completesResponse: true},
		{name: "not_ready_exit_completed", frame: "\x02\x00\x00\x00\x00\x00\x00\x02no", clean: true, completesResponse: true},
		{name: "truncated_header", frame: "\x01\x00"},
		{name: "truncated_payload", frame: "\x01\x00\x00\x00\x00\x00\x00\x05no"},
		{name: "still_running", running: true},
		{name: "exit_code_pending", frame: "\x01\x00\x00\x00\x00\x00\x00\x02ok", exitPending: true, clean: true, completesResponse: true},
		{name: "detached", detach: true, clean: true},
		{name: "tty", tty: true, clean: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "lp-exec-fence-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			daemon := dir
			root := fmt.Sprintf("/var/tmp/loomplane-fence-%x", sha256.Sum256([]byte(daemon)))
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			socket := filepath.Join(dir, "d.sock")
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			container, execution := strings.Repeat("a", 64), strings.Repeat("b", 64)
			var starts, receipts atomic.Int32
			api := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/info":
					_ = json.NewEncoder(w).Encode(map[string]string{"ID": daemon})
				case "/exec/" + execution + "/json":
					running, exitCode := starts.Load() > 0 && tc.running, any(0)
					if starts.Load() > 0 && tc.exitPending && receipts.Add(1) == 1 {
						running, exitCode = true, nil
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"ContainerID": container, "Running": running, "ExitCode": exitCode, "ProcessConfig": map[string]any{"tty": tc.tty, "privileged": false, "entrypoint": "pg_isready", "arguments": []string{"-U", "postgres"}}})
				case "/exec/" + execution + "/start":
					if _, readErr := io.Copy(io.Discard, r.Body); readErr != nil {
						t.Error(readErr)
						return
					}
					starts.Add(1)
					conn, buf, hijackErr := w.(http.Hijacker).Hijack()
					if hijackErr != nil {
						t.Error(hijackErr)
						return
					}
					defer conn.Close()
					_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/vnd.docker.raw-stream\r\n\r\n" + tc.frame)
					_ = buf.Flush()
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})}
			go func() { _ = api.Serve(listener) }()
			t.Cleanup(func() { _ = api.Close() })
			var diagnostics bytes.Buffer
			run := func(mode string, generation int) string {
				q, _ := json.Marshal(map[string]any{"Headers": map[string][]string{}, "Mode": mode, "Daemon": daemon, "Socket": socket, "Method": "POST", "Path": "/exec/" + execution + "/start", "Fence": EffectFence{Operation: "org/plan/app--postgres", Generation: int64(generation)}})
				body, _ := json.Marshal(map[string]bool{"Detach": tc.detach, "Tty": false})
				cmd := exec.CommandContext(t.Context(), python, "-c", strings.Replace(strings.Replace(effectFenceScript, "range(120)", "range(2)", 1), "except Exception: fail()", "except Exception:\n import traceback;traceback.print_exc();fail()", 1))
				cmd.Stderr = &diagnostics
				cmd.Stdin = bytes.NewReader(append(append(q, '\n'), body...))
				out, runErr := cmd.Output()
				if runErr != nil {
					t.Fatal(runErr)
				}
				return string(out)
			}
			if out := run("advance", 1); !strings.Contains(out, "200 OK") {
				t.Fatal(out)
			}
			out := run("exec", 1)
			raw, err := os.ReadFile(filepath.Join(root, "state"))
			if err != nil {
				t.Fatal(err)
			}
			var state struct{ Dirty bool }
			if err = json.Unmarshal(raw, &state); err != nil {
				t.Fatal(err)
			}
			if state.Dirty == tc.clean {
				t.Fatalf("journal dirty %v, want %v: %s", state.Dirty, !tc.clean, diagnostics.String())
			}
			if (tc.detach || tc.tty) && starts.Load() != 0 {
				t.Fatal("an exec the helper can't frame reached Docker")
			}
			if strings.HasSuffix(out, "0\r\n\r\n") != tc.completesResponse {
				t.Fatalf("response completion %v, expected %v: %q", strings.HasSuffix(out, "0\r\n\r\n"), tc.completesResponse, out)
			}
			// A clean journal lets the next generation take over the daemon.
			if next := run("advance", 2); strings.Contains(next, "200 OK") != tc.clean {
				t.Fatal("takeover", next)
			}
		})
	}
}

// Every exec start under an effect fence goes through the helper's exec
// mode, which frames Docker's stream. The generic request mode would leave
// the daemon journal dirty.
func TestFencedExecStartUsesExecMode(t *testing.T) {
	fenced := WithEffectFence(t.Context(), EffectFence{Operation: "org/plan/app--postgres", Generation: 1})
	for _, tc := range []struct {
		name    string
		fenced  bool
		ingress bool
		mode    string
		err     bool
	}{
		{name: "deployment", fenced: true, mode: "exec"},
		{name: "ingress", fenced: true, ingress: true, mode: "ingress-exec"},
		{name: "unfenced"},
		{name: "unfenced_ingress", ingress: true, err: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.fenced {
				ctx = fenced
			}
			mode, _, err := execStartMode(ctx, tc.ingress)
			if (err != nil) != tc.err || mode != tc.mode {
				t.Fatalf("mode %q err %v", mode, err)
			}
		})
	}
}
