package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestIngressExecFenceCompletion(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 helper")
	}
	for _, tc := range []struct {
		name                     string
		frame                    string
		running, foreign, detach bool
		lostCaller               bool
		exit                     int
		clean                    bool
	}{
		{name: "lost_caller", frame: "\x01\x00\x00\x00\x00\x00\x00\x02ok", clean: true, lostCaller: true},
		{name: "complete", frame: "\x01\x00\x00\x00\x00\x00\x00\x02ok", clean: true},
		{name: "failed_exit_completed", frame: "\x02\x00\x00\x00\x00\x00\x00\x02no", exit: 1, clean: true},
		{name: "truncated_header", frame: "\x01\x00"},
		{name: "truncated_payload", frame: "\x01\x00\x00\x00\x00\x00\x00\x05no"},
		{name: "still_running", running: true},
		{name: "unrelated_owned_container", foreign: true, clean: true},
		{name: "detached", detach: true, clean: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "lp-ingress-fence-")
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
			var starts atomic.Int32
			api := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/info":
					_ = json.NewEncoder(w).Encode(map[string]string{"ID": daemon})
				case "/exec/" + execution + "/json":
					_ = json.NewEncoder(w).Encode(map[string]any{"ContainerID": container, "Running": starts.Load() > 0 && tc.running, "ExitCode": tc.exit, "ProcessConfig": map[string]any{"tty": false, "privileged": false, "entrypoint": "sh", "arguments": []string{"-c", ingressAdminScript, "ingress", "read", "", ""}}})
				case "/containers/" + container + "/json":
					owner := "server"
					if tc.foreign {
						owner = "another-server"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": container, "State": map[string]any{"Running": true}, "Config": map[string]any{"Image": IngressImage, "Labels": map[string]string{"loomplane.org": "org", "loomplane.ingress": owner}}})
				case "/exec/" + execution + "/start":
					starts.Add(1)
					conn, buf, hijackErr := w.(http.Hijacker).Hijack()
					if hijackErr != nil {
						t.Error(hijackErr)
						return
					}
					defer conn.Close()
					_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n" + tc.frame)
					_ = buf.Flush()
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			})}
			go func() { _ = api.Serve(listener) }()
			t.Cleanup(func() { _ = api.Close() })
			run := func(mode string, generation int) string {
				q, _ := json.Marshal(map[string]any{"Headers": map[string][]string{}, "Mode": mode, "Daemon": daemon, "Socket": socket, "Method": "POST", "Path": "/exec/" + execution + "/start", "Fence": EffectFence{Operation: "org/ingress/server", Generation: int64(generation)}})
				body, _ := json.Marshal(map[string]bool{"Detach": tc.detach, "Tty": false})
				cmd := exec.CommandContext(t.Context(), python, "-c", strings.Replace(effectFenceScript, "range(120)", "range(2)", 1))
				cmd.Stdin = bytes.NewReader(append(append(q, '\n'), body...))
				if tc.lostCaller && mode == "ingress-exec" && generation == 1 && starts.Load() == 0 {
					pipe, pipeErr := cmd.StdoutPipe()
					if pipeErr != nil {
						t.Fatal(pipeErr)
					}
					if startErr := cmd.Start(); startErr != nil {
						t.Fatal(startErr)
					}
					_ = pipe.Close()
					_ = cmd.Wait()
					return ""
				}
				out, runErr := cmd.Output()
				if runErr != nil {
					t.Fatal(runErr)
				}
				return string(out)
			}
			if out := run("advance", 1); !strings.Contains(out, "200 OK") {
				t.Fatal(out)
			}
			out := run("ingress-exec", 1)
			raw, err := os.ReadFile(filepath.Join(root, "state"))
			if err != nil {
				t.Fatal(err)
			}
			var state struct{ Dirty bool }
			if err = json.Unmarshal(raw, &state); err != nil {
				t.Fatal(err)
			}
			if state.Dirty == tc.clean {
				t.Fatalf("dirty %v, clean expected %v", state.Dirty, tc.clean)
			}
			if (tc.foreign || tc.detach) && starts.Load() != 0 {
				t.Fatal("unapproved exec reached Docker")
			}
			if !tc.clean && strings.HasSuffix(out, "0\r\n\r\n") {
				t.Fatal("unknown completion accepted")
			}
			next := run("advance", 2)
			if strings.Contains(next, "200 OK") != tc.clean {
				t.Fatal("takeover", next)
			}
			if tc.clean {
				before := starts.Load()
				_ = run("ingress-exec", 1)
				if starts.Load() != before {
					t.Fatal("stale exec replayed")
				}
			}
		})
	}
}
