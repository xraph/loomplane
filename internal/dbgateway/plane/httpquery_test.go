package plane

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// What the pinned driver (@neondatabase/serverless 1.2.0) sends and parses,
// read from testdata/neon/node_modules/@neondatabase/serverless/index.js:
//
// Request (lines 1303-1315, the execute function inside neon()):
//   - POST to fetchEndpoint, by default https://api.<host>/sql (line 428).
//   - Headers Neon-Connection-String, and always Neon-Raw-Text-Output: true
//     and Neon-Array-Mode: true. A transaction adds Neon-Batch-Isolation-Level
//     (ReadUncommitted, ReadCommitted, RepeatableRead or Serializable, per
//     index.d.ts line 489), Neon-Batch-Read-Only and Neon-Batch-Deferrable
//     ("true" or "false"), each only when set. An authToken adds
//     Authorization: Bearer, which the gateway ignores.
//   - Body {query, params} or {queries: [{query, params}, ...]}, params
//     already turned into text by prepareValue (line 1288, hs()).
//
// Success (lines 1316-1325, execute and processQueryResult ds()): on res.ok
// the body is JSON. A batch reads .results, which must be an array. Each
// result reads .fields[].name and .fields[].dataTypeID, then .rows, which it
// maps with rows.map(c => c.map(...)): every row must be an array of text
// values (or null), since the driver always asks for array mode and runs
// its own type parsers. With fullResults it hands the whole object back
// with rows, rowAsArray and _parsers replaced, so command, rowCount and the
// other field keys (tableID, columnID, dataTypeSize, dataTypeModifier,
// format) reach the caller as the gateway sent them.
//
// Errors (lines 1325-1326): only HTTP 400 is parsed as JSON. The driver
// builds a NeonDbError from .message and copies the keys listed in er
// (line 1274): severity, code, detail, hint, position, internalPosition,
// internalQuery, where, schema, table, column, dataType, constraint, file,
// line, routine. Any other status becomes "Server error (HTTP status N):
// <body text>". It reads no response headers, so CORS needs to expose none.

// httpHarness serves HTTP queries over a router with one route, "orders".
type httpHarness struct {
	*routerHarness
	q *httpQuery
}

func newHTTPHarness(t *testing.T, d Dialer) *httpHarness {
	t.Helper()
	h := newRouterHarness(t, d, nil)
	h.apply(h.route("r1", "orders"))
	return &httpHarness{routerHarness: h, q: newHTTPQuery(h.r)}
}

const testDSN = "postgresql://" + testRole + ":" + testPass + "@orders." + string(testSuffix) + "/orders"

// post sends body to /sql with the driver's usual headers plus extra. An
// empty value in extra removes that header.
func (h *httpHarness) post(body string, extra map[string]string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.postCtx(context.Background(), body, extra)
}

// postCtx is post under ctx.
func (h *httpHarness) postCtx(ctx context.Context, body string, extra map[string]string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/sql", strings.NewReader(body))
	req.Header.Set("Neon-Connection-String", testDSN)
	req.Header.Set("Neon-Raw-Text-Output", "true")
	req.Header.Set("Neon-Array-Mode", "true")
	for k, v := range extra {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.q.ServeHTTP(rec, req)
	return rec
}

// jsonEq fails unless got is the JSON value want.
func jsonEq(t *testing.T, want, got string) {
	t.Helper()
	var w, g any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad expected JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, got)
	}
	if !reflect.DeepEqual(w, g) {
		t.Fatalf("body = %s\nwant   %s", got, want)
	}
}

// errorOf checks the status and returns the error body's code and message.
func errorOf(t *testing.T, rec *httptest.ResponseRecorder, status int) (code, message string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d: %s", rec.Code, status, rec.Body)
	}
	var body struct{ Code, Message string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %v: %s", err, rec.Body)
	}
	if body.Message == "" {
		t.Fatalf("error body has no message: %s", rec.Body)
	}
	return body.Code, body.Message
}

func TestHTTPQueryFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "neon", "http_query.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Method string      `json:"method"`
		Path   string      `json:"path"`
		Header http.Header `json:"header"`
		Body   string      `json:"body"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	h := newRouterHarness(t, nil, nil)
	rt := h.route("r1", "orders")
	// The capture's connection string carries app:pw.
	rt.Verifiers = []Verifier{NewVerifier("app", "pw", testRouteSalt, 4096)}
	h.apply(rt)
	q := newHTTPQuery(h.r)

	serve := func(header http.Header) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), fx.Method, fx.Path, strings.NewReader(fx.Body))
		req.Header = header
		rec := httptest.NewRecorder()
		q.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
		}
		return rec
	}

	// The captured request asks for array mode and raw text, as the driver
	// always does: the row is an array of text, and the driver parses "7" as
	// an int4 into [{n: 7}] for its caller.
	rec := serve(fx.Header.Clone())
	jsonEq(t, `{"command":"SELECT","rowCount":1,"rowAsArray":true,"rows":[["7"]],
		"fields":[{"name":"n","tableID":0,"columnID":0,"dataTypeID":23,"dataTypeSize":4,"dataTypeModifier":-1,"format":"text"}]}`,
		rec.Body.String())
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	// Without array mode the rows are objects keyed by field name.
	header := fx.Header.Clone()
	header.Del("Neon-Array-Mode")
	jsonEq(t, `{"command":"SELECT","rowCount":1,"rowAsArray":false,"rows":[{"n":"7"}],
		"fields":[{"name":"n","tableID":0,"columnID":0,"dataTypeID":23,"dataTypeSize":4,"dataTypeModifier":-1,"format":"text"}]}`,
		serve(header).Body.String())

	// One extended-protocol round trip per request, with text parameters and
	// text results.
	got := h.dialer.server(0).received()
	wantTail := []string{"Parse  SELECT $1::int AS n", "Bind ", "Describe P ", "Execute", "Sync"}
	if len(got) < 2*len(wantTail) || !slices.Equal(got[len(got)-len(wantTail):], wantTail) {
		t.Fatalf("backend received %q, want each request to end %q", got, wantTail)
	}
	for _, b := range h.dialer.server(0).bound() {
		if len(b.paramFormats) != 0 || len(b.resultFormats) != 0 || len(b.params) != 1 || string(b.params[0]) != "7" {
			t.Fatalf("bind = %+v, want one text parameter \"7\" and text results", b)
		}
	}
	if s := h.handle("r1").pool.Stats(); s.Open != 1 || s.Idle != 1 {
		t.Fatalf("pool = %+v, want the connection back, clean", s)
	}
}

func TestHTTPQueryParams(t *testing.T) {
	h := newHTTPHarness(t, nil)
	rec := h.post(`{"query":"SELECT $1 AS a","params":[null]}`, nil)
	jsonEq(t, `{"command":"SELECT","rowCount":1,"rowAsArray":true,"rows":[[null]],
		"fields":[{"name":"a","tableID":0,"columnID":0,"dataTypeID":25,"dataTypeSize":-1,"dataTypeModifier":-1,"format":"text"}]}`,
		rec.Body.String())
	for _, tc := range []struct{ param, want string }{
		{`12.50`, "12.50"}, {`true`, "true"}, {`"it's"`, "it's"}, {`{"k":[1]}`, `{"k":[1]}`},
	} {
		rec := h.post(`{"query":"SELECT $1 AS a","params":[`+tc.param+`]}`, nil)
		var body struct{ Rows [][]*string }
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Rows) != 1 || body.Rows[0][0] == nil {
			t.Fatalf("param %s: body %s", tc.param, rec.Body)
		}
		if got := *body.Rows[0][0]; got != tc.want {
			t.Errorf("param %s reached the backend as %q, want %q", tc.param, got, tc.want)
		}
	}
	if b := h.dialer.server(0).bound()[0]; b.params[0] != nil {
		t.Fatalf("JSON null bound as %q, want SQL NULL", b.params[0])
	}
}

func TestHTTPQueryBatchTxn(t *testing.T) {
	h := newHTTPHarness(t, nil)
	rec := h.post(`{"queries":[{"query":"SELECT $1 AS a","params":["x"]},{"query":"UPDATE t SET a = 1","params":[]}]}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	jsonEq(t, `{"results":[
		{"command":"SELECT","rowCount":1,"rowAsArray":true,"rows":[["x"]],
		 "fields":[{"name":"a","tableID":0,"columnID":0,"dataTypeID":25,"dataTypeSize":-1,"dataTypeModifier":-1,"format":"text"}]},
		{"command":"UPDATE","rowCount":null,"rowAsArray":true,"rows":[],"fields":[]}]}`, rec.Body.String())
	srv := h.dialer.server(0)
	var parses []string
	for _, m := range srv.received() {
		if p, ok := strings.CutPrefix(m, "Parse  "); ok {
			parses = append(parses, p)
		}
	}
	want(t, "statements", parses, []string{"BEGIN", "SELECT $1 AS a", "UPDATE t SET a = 1", "COMMIT"})

	// A failure skips the rest, rolls back and comes back as a 400 carrying
	// the backend's SQLSTATE.
	rec = h.post(`{"queries":[{"query":"SELECT 1","params":[]},{"query":"SELECT fail","params":[]},{"query":"SELECT 3","params":[]}]}`, nil)
	code, msg := errorOf(t, rec, http.StatusBadRequest)
	if code != "42000" || msg != "scripted failure" {
		t.Fatalf("error = %s %q, want the backend's 42000", code, msg)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["severity"] != "ERROR" {
		t.Errorf("error body %s has no severity", rec.Body)
	}
	if q := srv.queries(); q[len(q)-1] != "ROLLBACK" {
		t.Fatalf("simple queries = %q, want a ROLLBACK after the failure", q)
	}
	if s := h.handle("r1").pool.Stats(); s.Open != 1 || s.Idle != 1 {
		t.Fatalf("pool = %+v, want the connection back after the rollback", s)
	}
	// The connection is clean: the next request works on it.
	if again := h.post(`{"query":"SELECT 4","params":[]}`, nil); again.Code != http.StatusOK {
		t.Fatalf("request after the rollback: %d %s", again.Code, again.Body)
	}
	if h.dialer.dials() != 1 {
		t.Fatalf("dials = %d, want the one connection reused", h.dialer.dials())
	}

	// A single query that fails is a 400 too, and needs no rollback.
	rec = h.post(`{"query":"SELECT fail","params":[]}`, nil)
	if code, _ := errorOf(t, rec, http.StatusBadRequest); code != "42000" {
		t.Fatalf("code = %s, want 42000", code)
	}
}

func TestHTTPQueryBatchIsolation(t *testing.T) {
	h := newHTTPHarness(t, nil)
	rec := h.post(`{"queries":[{"query":"SELECT 1","params":[]}]}`, map[string]string{
		"Neon-Batch-Isolation-Level": "Serializable",
		"Neon-Batch-Read-Only":       "true",
		"Neon-Batch-Deferrable":      "true",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	rec = h.post(`{"queries":[{"query":"SELECT 1","params":[]}]}`, map[string]string{
		"Neon-Batch-Isolation-Level": "ReadCommitted",
		"Neon-Batch-Read-Only":       "false",
		"Neon-Batch-Deferrable":      "false",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var begins []string
	for _, m := range h.dialer.server(0).received() {
		if p, ok := strings.CutPrefix(m, "Parse  BEGIN"); ok {
			begins = append(begins, "BEGIN"+p)
		}
	}
	want(t, "BEGIN statements", begins, []string{
		"BEGIN ISOLATION LEVEL SERIALIZABLE, READ ONLY, DEFERRABLE",
		"BEGIN ISOLATION LEVEL READ COMMITTED, READ WRITE, NOT DEFERRABLE",
	})

	for _, hd := range []map[string]string{
		{"Neon-Batch-Isolation-Level": "Snapshot"},
		{"Neon-Batch-Read-Only": "yes"},
		{"Neon-Batch-Deferrable": "1"},
	} {
		rec := h.post(`{"queries":[{"query":"SELECT 1","params":[]}]}`, hd)
		errorOf(t, rec, http.StatusBadRequest)
	}
}

func TestHTTPQueryBadAuth(t *testing.T) {
	h := newHTTPHarness(t, nil)
	for _, dsn := range []string{
		"postgres://" + testRole + ":wrong@orders." + string(testSuffix) + "/orders",
		"postgres://ghost:" + testPass + "@orders." + string(testSuffix) + "/orders",
		"postgres://" + testRole + "@orders." + string(testSuffix) + "/orders",
	} {
		rec := h.post(`{"query":"SELECT 1","params":[]}`, map[string]string{"Neon-Connection-String": dsn})
		code, msg := errorOf(t, rec, http.StatusBadRequest)
		if code != "28P01" || !strings.HasPrefix(msg, "password authentication failed") {
			t.Fatalf("dsn %s: error = %s %q, want 28P01", dsn, code, msg)
		}
		if strings.Contains(rec.Body.String(), "wrong") || strings.Contains(rec.Body.String(), testPass) {
			t.Fatalf("error body %s echoes the password", rec.Body)
		}
	}
	if h.dialer.dials() != 0 {
		t.Fatal("a failed login dialed the backend")
	}
}

// driverErrorKeys are the keys the driver copies from an error body onto its
// NeonDbError (index.js:1274), besides message.
var driverErrorKeys = []string{"severity", "code", "detail", "hint", "position", "internalPosition",
	"internalQuery", "where", "schema", "table", "column", "dataType", "constraint", "file", "line", "routine"}

// The driver parses an error's JSON only on a 400, so a failed login has to
// be one for the caller's err.code to say 28P01.
func TestHTTPQueryAuthErrorParsesLikeTheDriver(t *testing.T) {
	h := newHTTPHarness(t, nil)
	rec := h.post(`{"query":"SELECT 1"}`, map[string]string{
		"Neon-Connection-String": "postgresql://" + testRole + ":wrong@orders." + string(testSuffix) + "/orders",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, the only status the driver parses", rec.Code)
	}
	// What the driver does with it: new NeonDbError(body.message), then
	// err[key] = body[key] for each key it knows.
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %v: %s", err, rec.Body)
	}
	if msg, _ := body["message"].(string); msg != `password authentication failed for user "`+testRole+`"` {
		t.Fatalf("message = %v", body["message"])
	}
	if code, _ := body["code"].(string); code != "28P01" {
		t.Fatalf("code = %v, want the string 28P01", body["code"])
	}
	for k, v := range body {
		if _, ok := v.(string); !ok {
			t.Errorf("%s = %v, want a string as node-postgres has it", k, v)
		}
		if k != "message" && !slices.Contains(driverErrorKeys, k) {
			t.Errorf("error body has %q, which the driver ignores", k)
		}
	}
}

// countingReader counts what is read from it.
type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func TestHTTPQueryBodyReadOnlyAfterAuth(t *testing.T) {
	h := newHTTPHarness(t, nil)
	send := func(dsn string, body *countingReader) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/sql", body)
		req.Header.Set("Neon-Connection-String", dsn)
		rec := httptest.NewRecorder()
		h.q.ServeHTTP(rec, req)
		return rec
	}
	for _, dsn := range []string{
		"postgres://" + testRole + ":wrong@orders." + string(testSuffix) + "/orders",
		"postgres://" + testRole + ":" + testPass + "@billing." + string(testSuffix) + "/billing",
	} {
		big := &countingReader{r: strings.NewReader(`{"query":"SELECT '` + strings.Repeat("x", 8<<20) + `'"}`)}
		rec := send(dsn, big)
		if code, _ := errorOf(t, rec, http.StatusBadRequest); code != "28P01" && code != "08004" {
			t.Fatalf("dsn %s: code = %s, want a refusal", dsn, code)
		}
		if n := big.n.Load(); n != 0 {
			t.Fatalf("dsn %s: a refused request read %d bytes of its body", dsn, n)
		}
	}
	small := &countingReader{r: strings.NewReader(`{"query":"SELECT 1"}`)}
	if rec := send(testDSN, small); rec.Code != http.StatusOK {
		t.Fatalf("logged-in request: %d %s", rec.Code, rec.Body)
	}
	if small.n.Load() == 0 {
		t.Fatal("a logged-in request did not read its body")
	}
}

func TestHTTPQueryBadRequests(t *testing.T) {
	h := newHTTPHarness(t, nil)
	for name, tc := range map[string]struct {
		body   string
		header map[string]string
		status int
	}{
		"no connection string":   {`{"query":"SELECT 1"}`, map[string]string{"Neon-Connection-String": ""}, 400},
		"bad scheme":             {`{"query":"SELECT 1"}`, map[string]string{"Neon-Connection-String": "mysql://a:b@c/d"}, 400},
		"unparseable":            {`{"query":"SELECT 1"}`, map[string]string{"Neon-Connection-String": "postgres://a:b@%zz/d"}, 400},
		"no user":                {`{"query":"SELECT 1"}`, map[string]string{"Neon-Connection-String": "postgres://orders." + string(testSuffix) + "/orders"}, 400},
		"not JSON":               {`SELECT 1`, nil, 400},
		"no query":               {`{"params":[]}`, nil, 400},
		"NUL in query":           {`{"query":"SELECT 1\u0000"}`, nil, 400},
		"trailing data":          {`{"query":"SELECT 1"} {}`, nil, 400},
		"body over 10 MiB":       {`{"query":"` + strings.Repeat("x", 10<<20) + `"}`, nil, 400},
		"query and queries both": {`{"query":"SELECT 1","queries":[]}`, nil, 400},
	} {
		rec := h.post(tc.body, tc.header)
		errorOf(t, rec, tc.status)
		if strings.Contains(rec.Body.String(), testPass) {
			t.Errorf("%s: error body echoes the connection string", name)
		}
	}
	// application_name and other query parameters are ignored, and
	// postgres:// works as well as postgresql://.
	dsn := "postgres://" + testRole + ":" + testPass + "@orders." + string(testSuffix) + ":443/orders?application_name=x&sslmode=require"
	if rec := h.post(`{"query":"SELECT 1"}`, map[string]string{"Neon-Connection-String": dsn}); rec.Code != 200 {
		t.Fatalf("postgres:// with query parameters: %d %s", rec.Code, rec.Body)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/sql", nil)
	rec := httptest.NewRecorder()
	h.q.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") == "" {
		t.Fatalf("GET = %d, Allow %q, want 405 with Allow", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestHTTPQueryCORSPreflight(t *testing.T) {
	h := newHTTPHarness(t, nil)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/sql", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "neon-connection-string,neon-array-mode,neon-raw-text-output,content-type")
	rec := httptest.NewRecorder()
	h.q.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	hd := rec.Header()
	if hd.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("Allow-Origin = %q", hd.Get("Access-Control-Allow-Origin"))
	}
	if !strings.Contains(hd.Get("Access-Control-Allow-Methods"), "POST") {
		t.Fatalf("Allow-Methods = %q", hd.Get("Access-Control-Allow-Methods"))
	}
	allowed := strings.ToLower(hd.Get("Access-Control-Allow-Headers"))
	for _, name := range []string{"neon-connection-string", "neon-raw-text-output", "neon-array-mode",
		"neon-batch-isolation-level", "neon-batch-read-only", "neon-batch-deferrable", "content-type", "authorization"} {
		if !strings.Contains(allowed, name) {
			t.Errorf("Allow-Headers %q lacks %s", allowed, name)
		}
	}
	if h.dialer.dials() != 0 {
		t.Fatal("a preflight dialed the backend")
	}
	// Errors carry CORS too, or a browser can't read them.
	rec = h.post(`{"query":"SELECT 1"}`, map[string]string{"Neon-Connection-String": ""})
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" || rec.Header().Get("Access-Control-Allow-Headers") == "" {
		t.Fatalf("error response headers = %v, want CORS", rec.Header())
	}
}

func TestHTTPQuerySessionStateDiscarded(t *testing.T) {
	h := newHTTPHarness(t, nil)
	if rec := h.post(`{"query":"SELECT 1","params":[]}`, nil); rec.Code != 200 {
		t.Fatalf("SELECT: %d %s", rec.Code, rec.Body)
	}
	srv := h.dialer.server(0)
	if slices.Contains(srv.queries(), "DISCARD ALL") {
		t.Fatal("a plain SELECT released LeaseSession")
	}
	rec := h.post(`{"query":"SET search_path TO other","params":[]}`, nil)
	if rec.Code != 200 {
		t.Fatalf("SET: %d %s", rec.Code, rec.Body)
	}
	jsonEq(t, `{"command":"SET","rowCount":null,"rowAsArray":true,"rows":[],"fields":[]}`, rec.Body.String())
	if q := srv.queries(); q[len(q)-1] != "DISCARD ALL" {
		t.Fatalf("simple queries = %q, want DISCARD ALL after the SET", q)
	}
	if s := h.handle("r1").pool.Stats(); s.Open != 1 || s.Idle != 1 {
		t.Fatalf("pool = %+v, want the connection back after DISCARD ALL", s)
	}

	// A batch with a session-changing statement anywhere in it is discarded
	// too.
	rec = h.post(`{"queries":[{"query":"SELECT 1","params":[]},{"query":"SELECT pg_advisory_lock(1)","params":[]}]}`, nil)
	if rec.Code != 200 {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body)
	}
	if q := srv.queries(); q[len(q)-1] != "DISCARD ALL" {
		t.Fatalf("simple queries = %q, want DISCARD ALL after the advisory lock", q)
	}
}

func TestHTTPQueryOpenTransactionRolledBack(t *testing.T) {
	h := newHTTPHarness(t, nil)
	rec := h.post(`{"query":"BEGIN","params":[]}`, nil)
	if rec.Code != 200 {
		t.Fatalf("BEGIN: %d %s", rec.Code, rec.Body)
	}
	if q := h.dialer.server(0).queries(); q[len(q)-1] != "ROLLBACK" {
		t.Fatalf("simple queries = %q, want the open transaction rolled back", q)
	}
	if s := h.handle("r1").pool.Stats(); s.Idle != 1 {
		t.Fatalf("pool = %+v, want the connection back", s)
	}
}

func TestHTTPQueryResultCap(t *testing.T) {
	h := newHTTPHarness(t, nil)
	h.q.maxResult = 1000
	rec := h.post(`{"queries":[{"query":"SELECT `+strings.Repeat("x", 600)+`","params":[]},{"query":"SELECT `+strings.Repeat("y", 600)+`","params":[]}]}`, nil)
	code, msg := errorOf(t, rec, http.StatusBadRequest)
	if code != "54000" || !strings.Contains(msg, "WebSocket") {
		t.Fatalf("error = %s %q, want 54000 pointing at the WebSocket driver", code, msg)
	}
	if s := h.handle("r1").pool.Stats(); s.Open != 0 {
		t.Fatalf("pool = %+v, want the connection closed", s)
	}
	// A row far over the cap is refused before its body is read.
	rec = h.post(`{"query":"SELECT `+strings.Repeat("z", resultSlack+5000)+`","params":[]}`, nil)
	if code, _ := errorOf(t, rec, http.StatusBadRequest); code != "54000" {
		t.Fatalf("code = %s, want 54000", code)
	}
	if s := h.handle("r1").pool.Stats(); s.Open != 0 {
		t.Fatalf("pool = %+v, want the connection closed", s)
	}
}

// wantCapped checks that rec is the result-cap refusal and the lease was
// broken.
func (h *httpHarness) wantCapped(what string, rec *httptest.ResponseRecorder) {
	h.t.Helper()
	if code, _ := errorOf(h.t, rec, http.StatusBadRequest); code != "54000" {
		h.t.Fatalf("%s: code = %s, want 54000", what, code)
	}
	if s := h.handle("r1").pool.Stats(); s.Open != 0 {
		h.t.Fatalf("%s: pool = %+v, want the connection closed", what, s)
	}
}

// Rows with no value bytes still cost memory and output: zero-column rows
// and all-NULL rows count against the cap.
func TestHTTPQueryResultCapCountsEmptyRows(t *testing.T) {
	h := newHTTPHarness(t, nil)
	h.q.maxResult = 10000

	rec := h.post(`{"query":"SELECT FROM generate_series(1,3)"}`, nil)
	jsonEq(t, `{"command":"SELECT","rowCount":3,"rowAsArray":true,"rows":[[],[],[]],"fields":[]}`, rec.Body.String())
	h.wantCapped("zero-column rows", h.post(`{"query":"SELECT FROM generate_series(1,5000)"}`, nil))

	rec = h.post(`{"query":"SELECT 2 NULLS AS c FROM generate_series(1,2)"}`, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"rows":[[null,null],[null,null]]`) {
		t.Fatalf("NULL rows: %d %s", rec.Code, rec.Body)
	}
	h.wantCapped("all-NULL rows", h.post(`{"query":"SELECT 20 NULLS AS c FROM generate_series(1,500)"}`, nil))
}

// In object mode every value repeats its field's name, and that counts too.
func TestHTTPQueryResultCapCountsFieldNames(t *testing.T) {
	h := newHTTPHarness(t, nil)
	h.q.maxResult = 10000
	q := `{"query":"SELECT 2 NULLS AS ` + strings.Repeat("n", 200) + ` FROM generate_series(1,20)"}`
	if rec := h.post(q, nil); rec.Code != http.StatusOK {
		t.Fatalf("array mode: %d %s", rec.Code, rec.Body)
	}
	h.wantCapped("object mode", h.post(q, map[string]string{"Neon-Array-Mode": ""}))
}

// A value is charged what it costs once escaped, not its raw length.
func TestHTTPQueryResultCapCountsEscaping(t *testing.T) {
	h := newHTTPHarness(t, nil)
	h.q.maxResult = 1000
	if rec := h.post(`{"query":"SELECT $1 AS a","params":["`+strings.Repeat("x", 300)+`"]}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("plain value: %d %s", rec.Code, rec.Body)
	}
	h.wantCapped("escaped value", h.post(`{"query":"SELECT $1 AS a","params":["`+strings.Repeat(`\u0001`, 300)+`"]}`, nil))
}

func TestJSONStringLenBoundsEncoding(t *testing.T) {
	for _, s := range []string{"", "plain", `quote " and \ slash`, "\n\r\t\b\f\x00\x1f", "<a&b>", "naïve 日本 🙂",
		"line sep ", "bad \xff\xfe utf8", strings.Repeat("\x01", 50)} {
		var buf strings.Builder
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(s); err != nil {
			t.Fatal(err)
		}
		encoded := len(buf.String()) - 1 // the newline
		if got := jsonStringLen([]byte(s)); got < encoded || got < len(s)+2 {
			t.Errorf("jsonStringLen(%q) = %d, encoded %d bytes", s, got, encoded)
		}
	}
}

func TestHTTPQueryDoesNotEscapeHTML(t *testing.T) {
	h := newHTTPHarness(t, nil)
	rec := h.post(`{"query":"SELECT $1 AS a","params":["<a&b>"]}`, nil)
	if !strings.Contains(rec.Body.String(), `["<a&b>"]`) {
		t.Fatalf("body = %s, want the value unescaped", rec.Body)
	}
}

func TestHTTPQueryRequestLimits(t *testing.T) {
	h := newHTTPHarness(t, nil)
	batch := func(n int) string {
		qs := make([]string, n)
		for i := range qs {
			qs[i] = `{"query":"SELECT 1"}`
		}
		return `{"queries":[` + strings.Join(qs, ",") + `]}`
	}
	params := func(n int) string { return strings.TrimSuffix(strings.Repeat(`"1",`, n), ",") }

	if rec := h.post(batch(maxBatchQueries), nil); rec.Code != http.StatusOK {
		t.Fatalf("a batch of 1000: %d %s", rec.Code, rec.Body)
	}
	if rec := h.post(`{"query":"SELECT 1","params":[`+params(maxRequestParams)+`]}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("65535 parameters: %d %s", rec.Code, rec.Body)
	}
	dials := h.dialer.dials()
	for name, body := range map[string]string{
		"1001 queries":                  batch(maxBatchQueries + 1),
		"65536 parameters":              `{"query":"SELECT 1","params":[` + params(maxRequestParams+1) + `]}`,
		"65536 parameters over a batch": `{"queries":[{"query":"SELECT 1","params":[` + params(40000) + `]},{"query":"SELECT 2","params":[` + params(25536) + `]}]}`,
	} {
		code, msg := errorOf(t, h.post(body, nil), http.StatusBadRequest)
		if code != "54000" || !strings.Contains(msg, "at most") {
			t.Fatalf("%s: error = %s %q, want 54000 naming the limit", name, code, msg)
		}
	}
	if h.dialer.dials() != dials || h.dialer.server(0).saw("Parse  SELECT 2") {
		t.Fatal("a request over a limit reached the backend")
	}
}

func TestHTTPQueryUnknownRoute(t *testing.T) {
	h := newHTTPHarness(t, nil)
	rec := h.post(`{"query":"SELECT 1"}`, map[string]string{
		"Neon-Connection-String": "postgres://" + testRole + ":" + testPass + "@billing." + string(testSuffix) + "/billing",
	})
	code, msg := errorOf(t, rec, http.StatusBadRequest)
	if code != "08004" || !strings.Contains(msg, "billing") {
		t.Fatalf("error = %s %q, want 08004 naming the label", code, msg)
	}
}

func TestHTTPQueryDraining(t *testing.T) {
	h := newHTTPHarness(t, nil)
	h.r.Drain(t.Context())
	code, _ := errorOf(t, h.post(`{"query":"SELECT 1"}`, nil), http.StatusBadRequest)
	if code != "57P01" {
		t.Fatalf("code = %s, want 57P01", code)
	}
}

func TestHTTPQueryPoolBusy(t *testing.T) {
	// max_connections 6 leaves the pool one connection.
	h := newHTTPHarness(t, &fakeDialer{maxConns: "6", hold: make(chan struct{})})
	held := make(chan *httptest.ResponseRecorder, 1)
	go func() { held <- h.post(`{"query":"SELECT hold"}`, nil) }()
	waitFor(t, "the first request to hold the connection", func() bool {
		return h.dialer.dials() == 1 && h.dialer.server(0).saw("Execute")
	})
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	rec := h.postCtx(ctx, `{"query":"SELECT 1"}`, nil)
	close(h.dialer.hold)
	if code, _ := errorOf(t, rec, http.StatusBadRequest); code != "53300" {
		t.Fatalf("code = %s, want 53300", code)
	}
	if rec := <-held; rec.Code != http.StatusOK {
		t.Fatalf("held request: %d %s", rec.Code, rec.Body)
	}
}

func TestHTTPQueryDeadlineBreaksLease(t *testing.T) {
	h := newHTTPHarness(t, &fakeDialer{hold: make(chan struct{})})
	t.Cleanup(func() { close(h.dialer.hold) })
	h.q.timeout = 200 * time.Millisecond
	start := time.Now()
	rec := h.post(`{"query":"SELECT hold","params":[]}`, nil)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("request took %v, want it cut off at its deadline", d)
	}
	if code, _ := errorOf(t, rec, http.StatusBadRequest); code != "57014" {
		t.Fatalf("code = %s, want 57014", code)
	}
	if s := h.handle("r1").pool.Stats(); s.Open != 0 {
		t.Fatalf("pool = %+v, want the connection closed", s)
	}
}

func TestHTTPQueryBackendLost(t *testing.T) {
	h := newHTTPHarness(t, nil)
	code, msg := errorOf(t, h.post(`{"query":"SELECT die","params":[]}`, nil), http.StatusBadRequest)
	if code != "08006" || strings.Contains(msg, "10.0.0.1") {
		t.Fatalf("error = %s %q, want 08006 without the backend address", code, msg)
	}
	if s := h.handle("r1").pool.Stats(); s.Open != 0 {
		t.Fatalf("pool = %+v, want the connection closed", s)
	}
}

func TestHTTPQueryCopyFromStdinRefused(t *testing.T) {
	h := newHTTPHarness(t, nil)
	code, _ := errorOf(t, h.post(`{"query":"COPY t FROM STDIN","params":[]}`, nil), http.StatusBadRequest)
	if code != "0A000" {
		t.Fatalf("code = %s, want 0A000", code)
	}
	if s := h.handle("r1").pool.Stats(); s.Open != 0 {
		t.Fatalf("pool = %+v, want the connection closed", s)
	}
}

// A DEALLOCATE that ran drops the statement from the connection's record, so
// a WebSocket session leased the connection later prepares it again.
func TestHTTPQueryDeallocateUpdatesStatements(t *testing.T) {
	h := newHTTPHarness(t, nil)
	if rec := h.post(`{"query":"SELECT 1"}`, nil); rec.Code != 200 {
		t.Fatalf("SELECT: %d %s", rec.Code, rec.Body)
	}
	pool := h.handle("r1").pool
	l := mustAcquire(t, pool, nil)
	l.Conn().Statements["lp_a"], l.Conn().Statements["lp_b"] = true, true
	l.Release(LeaseClean)

	// The fake backend has no lp_a, so DEALLOCATE lp_a fails and changes
	// nothing.
	if rec := h.post(`{"query":"DEALLOCATE lp_a"}`, nil); rec.Code != 400 {
		t.Fatalf("DEALLOCATE of a missing statement: %d %s", rec.Code, rec.Body)
	}
	l = mustAcquire(t, pool, nil)
	if !l.Conn().Statements["lp_a"] {
		t.Fatal("a failed DEALLOCATE dropped the statement record")
	}
	l.Release(LeaseClean)

	if rec := h.post(`{"query":"DEALLOCATE ALL"}`, nil); rec.Code != 200 {
		t.Fatalf("DEALLOCATE ALL: %d %s", rec.Code, rec.Body)
	}
	l = mustAcquire(t, pool, nil)
	if n := len(l.Conn().Statements); n != 0 {
		t.Fatalf("statements after DEALLOCATE ALL = %v, want none", l.Conn().Statements)
	}
	l.Release(LeaseClean)
	if slices.Contains(h.dialer.server(0).queries(), "DISCARD ALL") {
		t.Fatal("DEALLOCATE released LeaseSession")
	}
}
