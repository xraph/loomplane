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
	"testing"
)

func TestEffectHelperRequiresCompleteResponseFraming(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 is needed for the real host helper")
	}
	for _, tc := range []struct {
		name, response string
		complete       bool
	}{
		{"short_fixed", "HTTP/1.1 200 OK\r\nContent-Length: 12\r\nConnection: close\r\n\r\npartial", false},
		{"short_chunk", "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n8\r\npartial", false},
		{"missing_trailer_end", "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n0\r\n", false},
		{"unframed", "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\npartial", false},
		{"fixed", "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok", true},
		{"chunked", "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n2\r\nok\r\n0\r\nReceipt: yes\r\n\r\n", true},
		{"no_content", "HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := tc.response
			dir, err := os.MkdirTemp("/tmp", "lp-frame-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			daemon := dir + "/" + t.Name()
			journalDir := fmt.Sprintf("/var/tmp/loomplane-fence-%x", sha256.Sum256([]byte(daemon)))
			t.Cleanup(func() { _ = os.RemoveAll(journalDir) })
			socket := filepath.Join(dir, "docker.sock")
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			api := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/info" {
					_ = json.NewEncoder(w).Encode(map[string]string{"ID": daemon})
					return
				}
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					t.Error("no hijacker")
					return
				}
				conn, buf, e := hijacker.Hijack()
				if e != nil {
					t.Error(e)
					return
				}
				defer conn.Close()
				_, _ = buf.WriteString(response)
				_ = buf.Flush()
			})}
			go func() { _ = api.Serve(listener) }()
			t.Cleanup(func() { _ = api.Close() })
			run := func(mode string, generation int) string {
				t.Helper()
				input, _ := json.Marshal(map[string]any{"Mode": mode, "Daemon": daemon, "Socket": socket, "Method": "POST", "Path": "/mutation", "Headers": map[string]string{}, "Fence": map[string]any{"operation": "scope", "generation": generation}})
				command := exec.CommandContext(t.Context(), python, "-c", effectFenceScript)
				command.Stdin = bytes.NewReader(append(input, '\n'))
				out, e := command.Output()
				if e != nil {
					t.Fatal(e)
				}
				return string(out)
			}
			if out := run("advance", 1); !strings.Contains(out, "200 OK") {
				t.Fatal("initial arm", out)
			}
			out := run("request", 1)
			dirty, err := os.ReadFile(filepath.Join(journalDir, "state"))
			if err != nil {
				t.Fatal(err)
			}
			var state struct{ Dirty bool }
			if err = json.Unmarshal(dirty, &state); err != nil {
				t.Fatal(err)
			}
			if state.Dirty == tc.complete {
				t.Fatalf("dirty=%v, complete=%v", state.Dirty, tc.complete)
			}
			next := run("advance", 2)
			if strings.Contains(next, "200 OK") != tc.complete {
				t.Fatalf("takeover completion=%v: %s", tc.complete, next)
			}
			if !tc.complete && strings.HasSuffix(out, "0\r\n\r\n") {
				t.Fatal("incomplete daemon response completed caller framing")
			}
		})
	}
}
