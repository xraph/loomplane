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

	"github.com/xraph/loomplane/internal/clouds/server/fencerules"
)

// journalDaemon is a Docker API on a Unix socket in a fresh /tmp directory,
// with the SSH helper's journal directory for that daemon cleaned up after.
type journalDaemon struct {
	id, socket, root string
}

func newJournalDaemon(t *testing.T, handler http.HandlerFunc) *journalDaemon {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "lp-fence-repair-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	d := &journalDaemon{id: dir, socket: filepath.Join(dir, "d.sock")}
	d.root = fmt.Sprintf("/var/tmp/loomplane-fence-%x", sha256.Sum256([]byte(d.id)))
	t.Cleanup(func() { _ = os.RemoveAll(d.root) })
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", d.socket)
	if err != nil {
		t.Fatal(err)
	}
	api := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/info" {
			_ = json.NewEncoder(w).Encode(map[string]string{"ID": d.id})
			return
		}
		handler(w, r)
	})}
	go func() { _ = api.Serve(listener) }()
	t.Cleanup(func() { _ = api.Close() })
	return d
}

// seed writes the daemon's journal as a crashed helper would have left it.
func (d *journalDaemon) seed(t *testing.T, state string) {
	t.Helper()
	if err := os.Mkdir(d.root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.root, "state"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (d *journalDaemon) journal(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(d.root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]json.RawMessage
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// runHelper runs the SSH helper once with Python, printing its traceback to
// diagnostics, and returns what it wrote to its caller.
func runHelper(t *testing.T, d *journalDaemon, mode string, f EffectFence, path string, body []byte, diagnostics *bytes.Buffer) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 is needed for the real host helper")
	}
	q, _ := json.Marshal(map[string]any{"Headers": map[string][]string{}, "Mode": mode, "Daemon": d.id, "Socket": d.socket, "Method": "POST", "Path": path, "Fence": f})
	script := strings.Replace(testFenceScript(t), "except Exception: fail()", "except Exception:\n import traceback;traceback.print_exc();fail()", 1)
	cmd := exec.CommandContext(t.Context(), python, "-c", script)
	cmd.Stderr = diagnostics
	cmd.Stdin = bytes.NewReader(append(append(q, '\n'), body...))
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// A helper that died while an exec ran leaves the journal dirty with that
// exec recorded. Once Docker shows the exec finished, or no longer knows
// it, the next takeover clears the mark itself. Anything else, a legacy
// bare dirty mark included, stays blocked for an operator.
func TestEffectHelperTakeoverClearsAFinishedRecordedExec(t *testing.T) {
	container, execution := strings.Repeat("a", 64), strings.Repeat("b", 64)
	operation := "org/plan/app--postgres"
	record := fmt.Sprintf(`{"mode":"exec","exec":%q,"container":%q}`, execution, container)
	for _, tc := range []struct {
		name    string
		dirty   string
		status  int
		inspect string
		clears  bool
	}{
		{name: "finished", dirty: record, status: 200, inspect: fmt.Sprintf(`{"ContainerID":%q,"Running":false,"ExitCode":0}`, container), clears: true},
		{name: "finished_nonzero", dirty: record, status: 200, inspect: fmt.Sprintf(`{"ContainerID":%q,"Running":false,"ExitCode":137}`, container), clears: true},
		{name: "gone", dirty: record, status: 404, inspect: `{"message":"No such exec instance"}`, clears: true},
		{name: "running", dirty: record, status: 200, inspect: fmt.Sprintf(`{"ContainerID":%q,"Running":true,"ExitCode":null}`, container)},
		{name: "exit_unprocessed", dirty: record, status: 200, inspect: fmt.Sprintf(`{"ContainerID":%q,"Running":false,"ExitCode":null}`, container)},
		{name: "exit_code_bool", dirty: record, status: 200, inspect: fmt.Sprintf(`{"ContainerID":%q,"Running":false,"ExitCode":false}`, container)},
		{name: "other_container", dirty: record, status: 200, inspect: fmt.Sprintf(`{"ContainerID":%q,"Running":false,"ExitCode":0}`, strings.Repeat("c", 64))},
		{name: "daemon_error", dirty: record, status: 500, inspect: `{"message":"boom"}`},
		{name: "legacy", dirty: `true`, status: 200, inspect: fmt.Sprintf(`{"ContainerID":%q,"Running":false,"ExitCode":0}`, container)},
		{name: "unknown_mode", dirty: fmt.Sprintf(`{"mode":"request","exec":%q,"container":%q}`, execution, container), status: 200, inspect: fmt.Sprintf(`{"ContainerID":%q,"Running":false,"ExitCode":0}`, container)},
		{name: "malformed_exec", dirty: fmt.Sprintf(`{"mode":"exec","exec":"../containers","container":%q}`, container), status: 404, inspect: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newJournalDaemon(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/exec/"+execution+"/json" {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.inspect))
			})
			d.seed(t, fmt.Sprintf(`{"epochs":{%q:1},"dirty":%s}`, fencerules.JournalKey(operation), tc.dirty))
			var diagnostics bytes.Buffer
			out := runHelper(t, d, "advance", EffectFence{Operation: operation, Generation: 2}, "/", nil, &diagnostics)
			if strings.Contains(out, "200 OK") != tc.clears {
				t.Fatalf("takeover %q, want clears %v: %s", out, tc.clears, diagnostics.String())
			}
			state := d.journal(t)
			epochs := string(state["epochs"])
			if tc.clears {
				if string(state["dirty"]) != "false" {
					t.Fatalf("dirty %s after a cleared takeover", state["dirty"])
				}
				if want := fmt.Sprintf(`{%q:2}`, fencerules.JournalKey(operation)); strings.ReplaceAll(epochs, " ", "") != want {
					t.Fatalf("epochs %s, want %s", epochs, want)
				}
				return
			}
			if strings.ReplaceAll(string(state["dirty"]), " ", "") != tc.dirty {
				t.Fatalf("dirty %s changed from %s", state["dirty"], tc.dirty)
			}
		})
	}
}
