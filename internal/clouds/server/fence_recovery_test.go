package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
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
	if err := os.MkdirAll(d.root, 0o700); err != nil {
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

// A fence that died while an exec ran leaves the journal dirty with that
// exec recorded. Once Docker shows the exec finished, or no longer knows
// it, the next takeover clears the mark itself. Anything else, a legacy
// bare dirty mark included, stays blocked for an operator. The SSH helper
// and the node's native fence both do this.
func TestEffectHelperTakeoverClearsAFinishedRecordedExec(t *testing.T) {
	for _, host := range fenceHosts {
		t.Run(host, func(t *testing.T) { testTakeoverClearsAFinishedRecordedExec(t, host) })
	}
}

func testTakeoverClearsAFinishedRecordedExec(t *testing.T, host string) {
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
			takeover := func() string {
				var diagnostics bytes.Buffer
				out := runHelper(t, d, "advance", EffectFence{Operation: operation, Generation: 2}, "/", nil, &diagnostics)
				return out + diagnostics.String()
			}
			if host == "node" {
				native := newNodeFence(t, d.id, d.socket)
				d.root = native.root
				takeover = func() string {
					return native.run(t, "advance", EffectFence{Operation: operation, Generation: 2}, "/", nil)
				}
			}
			d.seed(t, fmt.Sprintf(`{"epochs":{%q:1},"dirty":%s}`, fencerules.JournalKey(operation), tc.dirty))
			out := takeover()
			if strings.Contains(out, "200 OK") != tc.clears {
				t.Fatalf("takeover %q, want clears %v", out, tc.clears)
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

// pythonAccess runs the SSH helper with local Python, speaking to it as
// sshAccess does over an SSH session.
type pythonAccess struct {
	HostAccess
	t *testing.T
}

func (a pythonAccess) FencedRequest(ctx context.Context, mode string, f EffectFence, daemon, socket, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	python, err := exec.LookPath("python3")
	if err != nil {
		a.t.Skip("Python 3 is needed for the real host helper")
	}
	request, _ := json.Marshal(struct {
		Mode, Daemon, Socket, Method, Path string
		Fence                              EffectFence
		Headers                            http.Header
	}{mode, daemon, socket, method, path, f, headers})
	input := append(request, '\n')
	if body != nil {
		rest, readErr := io.ReadAll(body)
		if readErr != nil {
			return nil, readErr
		}
		input = append(input, rest...)
	}
	cmd := exec.CommandContext(ctx, python, "-c", testFenceScript(a.t))
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return http.ReadResponse(bufio.NewReader(bytes.NewReader(out)), nil)
}

// The repair path clears a dirty mark nothing else may clear, a legacy bare
// one included, but only for the daemon it names, only when no helper holds
// the lock, only while no exec runs in a Loomplane container, and only for
// the journal the operator reviewed.
func TestFenceRepair(t *testing.T) {
	owned := strings.Repeat("a", 64)
	running, done := strings.Repeat("b", 64), strings.Repeat("d", 64)
	operation := "org/plan/app--postgres"
	epochs := fmt.Sprintf(`{%q:3}`, fencerules.JournalKey(operation))
	for _, tc := range []struct {
		name, journal string
		// runningExec leaves an exec running in the owned container.
		runningExec bool
		// locked holds daemon.lock, as a live helper does.
		locked bool
		// daemon is the docker_id the connection recorded, if not this one.
		daemon string
		node   bool
		// staleReview repairs with a review of another journal.
		staleReview bool
		reviewErr   string
		repairErr   string
		repaired    bool
	}{
		{name: "legacy", journal: `{"epochs":` + epochs + `,"dirty":true}`, repaired: true},
		{name: "exec_mark", journal: fmt.Sprintf(`{"epochs":%s,"dirty":{"mode":"exec","exec":%q,"container":%q}}`, epochs, done, owned), repaired: true},
		{name: "clean", journal: `{"epochs":` + epochs + `,"dirty":false}`},
		{name: "running_exec", journal: `{"epochs":` + epochs + `,"dirty":true}`, runningExec: true, repairErr: "still running"},
		{name: "stale_review", journal: `{"epochs":` + epochs + `,"dirty":true}`, staleReview: true, repairErr: "review again"},
		{name: "helper_alive", journal: `{"epochs":` + epochs + `,"dirty":true}`, locked: true, reviewErr: "holds the fence lock", repairErr: "holds the fence lock"},
		{name: "other_daemon", journal: `{"epochs":` + epochs + `,"dirty":true}`, daemon: "replaced-daemon", reviewErr: "could not review", repairErr: "could not"},
		{name: "node", journal: `{"epochs":` + epochs + `,"dirty":true}`, node: true, reviewErr: "SSH", repairErr: "SSH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newJournalDaemon(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/containers/json":
					var filters map[string][]string
					if err := json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters); err != nil || len(filters) != 1 || len(filters["label"]) != 1 || filters["label"][0] != "loomplane.org" {
						t.Errorf("containers listed with filters %q", r.URL.Query().Get("filters"))
					}
					_, _ = fmt.Fprintf(w, `[{"Id":%q,"Names":["/app--postgres"]}]`, owned)
				case "/containers/" + owned + "/json":
					_, _ = fmt.Fprintf(w, `{"Id":%q,"Name":"/app--postgres","ExecIDs":[%q,%q]}`, owned, running, done)
				case "/exec/" + running + "/json":
					_, _ = fmt.Fprintf(w, `{"ContainerID":%q,"Running":%v,"ExitCode":null}`, owned, tc.runningExec)
				case "/exec/" + done + "/json":
					_, _ = fmt.Fprintf(w, `{"ContainerID":%q,"Running":false,"ExitCode":0}`, owned)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			d.seed(t, tc.journal)
			if tc.locked {
				lock, err := os.OpenFile(filepath.Join(d.root, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = lock.Close() })
				if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &Config{Socket: d.socket, Details: map[string]string{"docker_id": d.id}}
			if tc.daemon != "" {
				cfg.Details["docker_id"] = tc.daemon
			}
			if tc.node {
				cfg.Node = &NodeTarget{OrgID: "org_1", NodeID: testNodeID, IdentityGeneration: 1}
			}
			c := &Client{Config: cfg, access: pythonAccess{t: t}}
			review, err := c.ReviewFenceRepair(t.Context())
			if tc.reviewErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.reviewErr) {
					t.Fatalf("review error %v, want %q", err, tc.reviewErr)
				}
			} else if err != nil {
				t.Fatal(err)
			} else {
				var want struct{ Dirty any }
				_ = json.Unmarshal([]byte(tc.journal), &want)
				if !reflect.DeepEqual(review.Dirty, want.Dirty) {
					t.Fatalf("review shows dirty %v, want %v", review.Dirty, want.Dirty)
				}
				if review.Daemon != d.id || len(review.Review) != 64 {
					t.Fatalf("review %+v", review)
				}
				if tc.runningExec != (len(review.Running) == 1) || tc.runningExec && (review.Running[0].Exec != running || review.Running[0].Container != owned || review.Running[0].Name != "app--postgres") {
					t.Fatalf("running execs %+v", review.Running)
				}
			}
			token := strings.Repeat("0", 64)
			if review != nil && !tc.staleReview {
				token = review.Review
			}
			result, err := c.RepairFence(t.Context(), token)
			if tc.repairErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.repairErr) {
					t.Fatalf("repair error %v, want %q", err, tc.repairErr)
				}
			} else if err != nil {
				t.Fatal(err)
			} else if result.Repaired != tc.repaired {
				t.Fatalf("repaired %v, want %v", result.Repaired, tc.repaired)
			}
			state := d.journal(t)
			if strings.ReplaceAll(string(state["epochs"]), " ", "") != epochs {
				t.Fatalf("epochs %s, want %s", state["epochs"], epochs)
			}
			var original struct{ Dirty json.RawMessage }
			_ = json.Unmarshal([]byte(tc.journal), &original)
			want := string(original.Dirty)
			if tc.repaired {
				want = "false"
			}
			if got := strings.ReplaceAll(string(state["dirty"]), " ", ""); got != want {
				t.Fatalf("dirty %s, want %s", got, want)
			}
		})
	}
}
