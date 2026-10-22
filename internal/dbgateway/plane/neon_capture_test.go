//go:build neon

package plane

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgproto3"
)

var update = flag.Bool("update", false, "rewrite the Neon driver capture fixtures in testdata/neon")

const (
	captureDSN = "postgres://app:pw@orders.us-east-1.db.example.com/orders"

	// wantConnectionString is the DSN as driver 1.x puts it in the header:
	// scheme rewritten to postgresql:// and its own application_name added.
	wantConnectionString = "postgresql://app:pw@orders.us-east-1.db.example.com/orders" +
		"?application_name=pkg%3Anpm%2F%2540neondatabase%2Fserverless%401.2.0"

	// maxCapturedBytes caps how much of the WebSocket stream goes into a
	// fixture. The startup packet and anything pipelined behind it are well
	// inside this.
	maxCapturedBytes = 2048
)

// wsCapture is what the recorder saw when the driver opened its WebSocket.
// Frames are the binary messages in arrival order, truncated to
// maxCapturedBytes in total.
type wsCapture struct {
	Path   string     `json:"path"`
	Query  url.Values `json:"query"`
	Frames [][]byte   `json:"frames"`
}

// httpCapture is the one-shot HTTP query the driver sent to fetchEndpoint.
type httpCapture struct {
	Method string      `json:"method"`
	Path   string      `json:"path"`
	Header http.Header `json:"header"`
	Body   []byte      `json:"body"`
}

// recorder is a stand-in for the gateway. It accepts the driver's WebSocket
// and HTTP calls, stores what arrives, and never answers in Postgres.
type recorder struct {
	mu   sync.Mutex
	ws   *wsCapture
	http *httpCapture
}

func (r *recorder) handler() http.Handler {
	mux := http.NewServeMux()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux.HandleFunc("/v2", func(w http.ResponseWriter, req *http.Request) {
		conn, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		capture := &wsCapture{Path: req.URL.Path, Query: req.URL.Query()}
		r.mu.Lock()
		if r.ws == nil {
			r.ws = capture
		}
		r.mu.Unlock()
		total := 0
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if mt != websocket.BinaryMessage || total >= maxCapturedBytes {
				continue
			}
			if room := maxCapturedBytes - total; len(data) > room {
				data = data[:room]
			}
			total += len(data)
			r.mu.Lock()
			capture.Frames = append(capture.Frames, append([]byte(nil), data...))
			r.mu.Unlock()
		}
	})
	mux.HandleFunc("/sql", func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.http = &httpCapture{
			Method: req.Method,
			Path:   req.URL.Path,
			Header: req.Header.Clone(),
			Body:   body,
		}
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"capture only"}`))
	})
	return mux
}

func (r *recorder) snapshot() (wsCapture, httpCapture, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ws == nil || r.http == nil {
		return wsCapture{}, httpCapture{}, false
	}
	w := *r.ws
	w.Frames = append([][]byte(nil), r.ws.Frames...)
	return w, *r.http, true
}

// decodePipelined decodes the whole captured stream: the startup-phase
// message first, then every regular frontend message behind it.
func decodePipelined(t *testing.T, frames [][]byte) (pgproto3.FrontendMessage, []pgproto3.FrontendMessage) {
	t.Helper()
	be := pgproto3.NewBackend(bytes.NewReader(bytes.Join(frames, nil)), io.Discard)
	first, err := be.ReceiveStartupMessage()
	if err != nil {
		t.Fatalf("decode startup: %v", err)
	}
	var rest []pgproto3.FrontendMessage
	for {
		msg, err := be.Receive()
		// pgproto3 reports the end of a stream that stops on a message
		// boundary as either EOF or unexpected EOF, depending on read-ahead.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return first, rest
		}
		if err != nil {
			t.Fatalf("decode message %d after startup: %v", len(rest)+1, err)
		}
		// Receive reuses its buffers, so keep a copy of what we hold on to.
		rest = append(rest, copyFrontend(t, msg))
	}
}

func copyFrontend(t *testing.T, msg pgproto3.FrontendMessage) pgproto3.FrontendMessage {
	t.Helper()
	switch m := msg.(type) {
	case *pgproto3.PasswordMessage:
		return &pgproto3.PasswordMessage{Password: m.Password}
	case *pgproto3.Query:
		return &pgproto3.Query{String: m.String}
	default:
		t.Fatalf("unexpected pipelined message %T", msg)
		return nil
	}
}

// TestNeonDriverCapture runs @neondatabase/serverless against a recorder
// and asserts the two request shapes the gateway has to accept: the
// WebSocket open (Pool) and the one-shot HTTP query (neon()).
//
// Observed with @neondatabase/serverless 1.2.0 and the defaults in
// capture.mjs: the driver's pipelineConnect option defaults to "password",
// so its first WebSocket frame holds a StartupMessage, then a PasswordMessage
// ("pw") and the first Query, all written without waiting for the server's
// AuthenticationCleartextPassword request. The gateway has to cope with that
// ordering. forceDisablePgSSL defaults to true, so no SSLRequest is sent.
// Run with -update to rewrite ws_open.json and http_query.json.
func TestNeonDriverCapture(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	dir := filepath.Join("testdata", "neon")
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "@neondatabase", "serverless")); err != nil {
		t.Skip("run `npm ci --prefix internal/dbgateway/plane/testdata/neon` first")
	}

	rec := &recorder{}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "capture.mjs")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"CAPTURE_URL=ws"+strings.TrimPrefix(srv.URL, "http"),
		"DSN="+captureDSN,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node capture.mjs: %v\n%s", err, out)
	}

	ws, httpq, ok := rec.snapshot()
	if !ok {
		t.Fatalf("recorder missed a request: ws=%v http=%v", ws.Path != "", httpq.Path != "")
	}
	t.Logf("ws: path=%s query=%v", ws.Path, ws.Query)
	t.Logf("http: %s %s headers=%v body=%s", httpq.Method, httpq.Path, httpq.Header, httpq.Body)

	if ws.Path != "/v2" {
		t.Errorf("ws path = %q, want /v2", ws.Path)
	}
	if got, want := ws.Query.Get("address"), "orders.us-east-1.db.example.com:5432"; got != want {
		t.Errorf("ws address = %q, want %q", got, want)
	}
	first, rest := decodePipelined(t, ws.Frames)
	startup, isStartup := first.(*pgproto3.StartupMessage)
	if !isStartup {
		t.Fatalf("first frontend message = %T, want *pgproto3.StartupMessage (no SSLRequest)", first)
	}
	if got := startup.Parameters["user"]; got != "app" {
		t.Errorf("startup user = %q, want app", got)
	}
	if got := startup.Parameters["database"]; got != "orders" {
		t.Errorf("startup database = %q, want orders", got)
	}
	if len(ws.Frames) != 1 {
		t.Errorf("ws binary frames = %d, want 1 (startup, password and query pipelined together)", len(ws.Frames))
	}
	if len(rest) != 2 {
		t.Fatalf("messages after startup = %d, want 2 (PasswordMessage, Query): %+v", len(rest), rest)
	}
	if pw, ok := rest[0].(*pgproto3.PasswordMessage); !ok || pw.Password != "pw" {
		t.Errorf("second frontend message = %+v, want PasswordMessage{pw}", rest[0])
	}
	if q, ok := rest[1].(*pgproto3.Query); !ok || q.String != "SELECT 1" {
		t.Errorf("third frontend message = %+v, want Query{SELECT 1}", rest[1])
	}

	if httpq.Method != "POST" {
		t.Errorf("http method = %q, want POST", httpq.Method)
	}
	if httpq.Path != "/sql" {
		t.Errorf("http path = %q, want /sql", httpq.Path)
	}
	if got := httpq.Header.Get("Neon-Connection-String"); got != wantConnectionString {
		t.Errorf("Neon-Connection-String = %q, want %q", got, wantConnectionString)
	}
	assertJSONEq(t, `{"query":"SELECT $1::int AS n","params":["7"]}`, string(httpq.Body))

	if *update {
		writeFixture(t, filepath.Join(dir, "ws_open.json"), struct {
			Path   string     `json:"path"`
			Query  url.Values `json:"query"`
			Frames []string   `json:"frames_base64"`
		}{ws.Path, ws.Query, encodeFrames(ws.Frames)})
		writeFixture(t, filepath.Join(dir, "http_query.json"), struct {
			Method string      `json:"method"`
			Path   string      `json:"path"`
			Header http.Header `json:"header"`
			Body   string      `json:"body"`
		}{httpq.Method, httpq.Path, httpq.Header, string(httpq.Body)})
	}
}

func encodeFrames(frames [][]byte) []string {
	out := make([]string, len(frames))
	for i, f := range frames {
		out[i] = base64.StdEncoding.EncodeToString(f)
	}
	return out
}

func assertJSONEq(t *testing.T, want, got string) {
	t.Helper()
	var w, g any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad expected JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Errorf("body is not JSON: %v: %s", err, got)
		return
	}
	wb, _ := json.Marshal(w)
	gb, _ := json.Marshal(g)
	if !bytes.Equal(wb, gb) {
		t.Errorf("body = %s, want %s", gb, wb)
	}
}

func writeFixture(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
