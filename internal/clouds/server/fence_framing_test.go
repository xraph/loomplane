package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/node"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// fenceHosts are the two hosts a fenced request runs on: the SSH helper,
// run here with Python, and a node's Docker handler with its native journal.
var fenceHosts = []string{"ssh", "node"}

func TestEffectHelperRequiresCompleteResponseFraming(t *testing.T) {
	python, pythonErr := exec.LookPath("python3")
	for _, host := range fenceHosts {
		t.Run(host, func(t *testing.T) {
			if host == "ssh" && pythonErr != nil {
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
						command := exec.CommandContext(t.Context(), python, "-c", testFenceScript(t))
						command.Stdin = bytes.NewReader(append(input, '\n'))
						out, e := command.Output()
						if e != nil {
							t.Fatal(e)
						}
						return string(out)
					}
					if host == "node" {
						native := newNodeFence(t, daemon, socket)
						journalDir = native.root
						run = func(mode string, generation int) string {
							return native.run(t, mode, EffectFence{Operation: "scope", Generation: int64(generation)}, "/mutation", nil)
						}
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
		})
	}
}

func testFenceScript(t *testing.T) string {
	t.Helper()
	script, err := fenceScript()
	if err != nil {
		t.Fatal(err)
	}
	return script
}

// nodeFence runs fenced requests the way a joined node does: through
// nodeAccess, on a stream served by internal/node's Docker handler, which
// keeps its native journal in a state directory. That journal's state file
// holds what the SSH helper's does, so one set of checks reads either.
type nodeFence struct {
	client  *Client
	daemon  string
	streams *servedStreams
	// root is the journal's directory, where the SSH helper's is
	// /var/tmp/loomplane-fence-<hash>.
	root string
}

func newNodeFence(t *testing.T, daemon, socket string) *nodeFence {
	t.Helper()
	// A short path, as the fence tests keep their sockets in /tmp too.
	state, err := os.MkdirTemp("/tmp", "lp-node-fence-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	journal, err := node.OpenFenceJournal(state, daemon)
	if err != nil {
		t.Fatal(err)
	}
	reg := &node.Registry{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	reg.Register(nodeproto.EndpointDocker, node.DockerHandler(socket, journal))
	streams := &servedStreams{reg: reg, live: node.NewLive(node.Config{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}})}
	cfg, err := ParseConfig(NodeCredential("org_1", testNodeID), map[string]string{"node_id": testNodeID, "identity_generation": "1", "docker_socket": socket})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Details["docker_id"] = daemon
	c, err := Connect(t.Context(), cfg, streams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	sum := sha256.Sum256([]byte(daemon))
	return &nodeFence{client: c, daemon: daemon, streams: streams, root: filepath.Join(state, "fence", hex.EncodeToString(sum[:]))}
}

// run sends one fenced POST and returns what its caller read, put back in
// the SSH helper's framing: the status line, the body, and the last chunk
// only when the whole chunked response arrived. It returns once the node has
// finished with the stream, so the journal is settled.
func (n *nodeFence) run(t *testing.T, mode string, f EffectFence, path string, body []byte) string {
	t.Helper()
	defer n.streams.wait()
	response, err := n.client.fencedRequest(t.Context(), mode, f, http.MethodPost, path, bytes.NewReader(body), http.Header{})
	if err != nil {
		return ""
	}
	data, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	out := response.Proto + " " + response.Status + "\r\n\r\n" + string(data)
	if readErr == nil && closeErr == nil && len(response.TransferEncoding) > 0 && response.TransferEncoding[0] == "chunked" {
		out += "0\r\n\r\n"
	}
	return out
}

// lose sends one fenced POST and closes the stream without reading the
// response, as a caller that went away does.
func (n *nodeFence) lose(t *testing.T, mode string, f EffectFence, path string, body []byte) {
	t.Helper()
	defer n.streams.wait()
	conn, err := n.streams.Open(t.Context(), "org_1", testNodeID, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDocker, Target: nodeproto.DockerTargetFenced,
		Fence: &nodeproto.Fence{Resource: f.Operation, Generation: f.Generation, Mode: mode, Daemon: n.daemon}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://docker"+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = req.Write(conn)
	_ = conn.Close()
}

// servedStreams is a NodeStreams whose streams a node's Registry serves in
// this process, over a pipe, with the hub's verdict handshake.
type servedStreams struct {
	reg  *node.Registry
	live *node.Live
	wg   sync.WaitGroup
}

func (s *servedStreams) Open(ctx context.Context, _, _ string, hdr nodeproto.StreamHeader) (net.Conn, error) {
	caller, served := net.Pipe()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.reg.Serve(context.WithoutCancel(ctx), s.live, served)
	}()
	if err := nodeproto.WriteHeader(caller, hdr); err != nil {
		_ = caller.Close()
		return nil, err
	}
	var verdict [1]byte
	if _, err := io.ReadFull(caller, verdict[:]); err != nil {
		_ = caller.Close()
		return nil, err
	}
	if verdict[0] != 0 {
		reason, _ := bufio.NewReader(caller).ReadString('\n')
		_ = caller.Close()
		return nil, errors.New("the node refused the stream: " + strings.TrimSpace(reason))
	}
	return caller, nil
}

// wait waits for the node to finish every stream opened so far.
func (s *servedStreams) wait() { s.wg.Wait() }
