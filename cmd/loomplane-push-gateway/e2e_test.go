//go:build !windows

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const (
	collapseID = "a1b2c3d4e5f60718"
	deviceKID  = "0123456789abcdef"
	sealed     = "c2VhbGVkIGJ5dGVz"

	iosToken     = "abcdef0123456789abcdef0123456789"
	iosGoneToken = "deadbeefdeadbeef"
)

var (
	buildOnce sync.Once
	buildPath string
	errBuild  error
	buildSkip bool
)

// buildGateway builds the command once per test run. It builds with -race
// when the suite does, so the two share compiled packages, and skips when
// the build outlasts the test deadline from a cold cache.
func buildGateway(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		limit := 5 * time.Minute
		if deadline, ok := t.Deadline(); ok {
			limit = time.Until(deadline) - time.Minute
		}
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		defer cancel()
		dir, err := os.MkdirTemp("", "push-gateway-e2e")
		if err != nil {
			errBuild = err
			return
		}
		buildPath = filepath.Join(dir, "loomplane-push-gateway")
		args := []string{"build"}
		if raceEnabled {
			args = append(args, "-race")
		}
		args = append(args, "-o", buildPath, ".")
		out, err := exec.CommandContext(ctx, "go", args...).CombinedOutput()
		if err != nil {
			if ctx.Err() != nil {
				buildSkip = true
			}
			errBuild = fmt.Errorf("build gateway: %w\n%s", err, out)
		}
	})
	if buildSkip {
		if os.Getenv("CI") != "" {
			t.Fatalf("gateway did not build in time: %v", errBuild)
		}
		t.Skipf("gateway did not build in time, most likely a cold build cache: %v", errBuild)
	}
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	return buildPath
}

func TestMain(m *testing.M) {
	code := m.Run()
	if buildPath != "" {
		_ = os.RemoveAll(filepath.Dir(buildPath))
	}
	os.Exit(code)
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type gatewayProcess struct {
	URL  string
	cmd  *exec.Cmd
	logs *lockedBuffer
	done chan error // receives once, when the process has exited
	code atomic.Int32
	over atomic.Bool
}

// startGateway runs the binary with exactly env as its environment.
func startGateway(t *testing.T, bin string, environ map[string]string) *gatewayProcess {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), bin)
	cmd.Env = nil
	for k, v := range environ {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	logs := &lockedBuffer{}
	pr, pw := io.Pipe()
	cmd.Stderr = io.MultiWriter(logs, pw)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	p := &gatewayProcess{cmd: cmd, logs: logs, done: make(chan error, 1)}
	go func() {
		err := cmd.Wait()
		p.code.Store(int32(cmd.ProcessState.ExitCode()))
		p.over.Store(true)
		pw.Close()
		p.done <- err
	}()
	t.Cleanup(func() {
		if !p.over.Load() {
			_ = cmd.Process.Kill()
		}
	})
	addr := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			var line struct {
				Msg  string `json:"msg"`
				Addr string `json:"addr"`
			}
			if json.Unmarshal(sc.Bytes(), &line) == nil && line.Msg == "push gateway listening" {
				addr <- line.Addr
				break
			}
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	select {
	case a := <-addr:
		p.URL = "http://" + a
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("gateway did not start listening within 10s; logs:\n%s", logs.String())
	}
	return p
}

// stop sends SIGTERM and returns the exit code.
func (p *gatewayProcess) stop(t *testing.T) int {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("signal gateway: %v", err)
	}
	select {
	case <-p.done:
	case <-time.After(25 * time.Second):
		_ = p.cmd.Process.Kill()
		t.Fatalf("gateway did not stop within 25s")
	}
	return int(p.code.Load())
}

type capturedRequest struct {
	Path   string
	Header http.Header
	Body   []byte
}

type fakeProvider struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []capturedRequest
}

func (f *fakeProvider) record(r *http.Request) []byte {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, capturedRequest{Path: r.URL.EscapedPath(), Header: r.Header.Clone(), Body: body})
	return body
}

func (f *fakeProvider) last(t *testing.T) capturedRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		t.Fatal("fake provider got no request")
	}
	return f.reqs[len(f.reqs)-1]
}

func newFakeFCM(t *testing.T) *fakeProvider {
	f := &fakeProvider{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"fake-access-token","token_type":"Bearer","expires_in":3600}`)
	})
	mux.HandleFunc("POST /v1/projects/loomplane-test/messages:send", func(w http.ResponseWriter, r *http.Request) {
		body := f.record(r)
		w.Header().Set("Content-Type", "application/json")
		if bytes.Contains(body, []byte(`"fcm-gone"`)) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":404,"message":"gone","status":"NOT_FOUND","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"name":"projects/loomplane-test/messages/1"}`)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func newFakeAPNs(t *testing.T) *fakeProvider {
	f := &fakeProvider{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /3/device/{token}", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if r.PathValue("token") == iosGoneToken {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGone)
			_, _ = io.WriteString(w, `{"reason":"Unregistered"}`)
			return
		}
		w.Header().Set("apns-id", "11111111-2222-3333-4444-555555555555")
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// secrets are the files a gateway reads, and the values the logs must not echo.
type secrets struct {
	fcmFile, apnsFile string
	sealing           map[string]string // key ID -> file
	sealingB64        map[string]string // key ID -> base64
	pemMarker         string
}

func newSecrets(t *testing.T, fcmTokenURL string, ids ...string) secrets {
	t.Helper()
	dir := t.TempDir()
	s := secrets{sealing: map[string]string{}, sealingB64: map[string]string{}, pemMarker: "PRIVATE KEY"}

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	sa, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "loomplane-test",
		"private_key_id": "kid1",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "sa@loomplane-test.iam.gserviceaccount.com",
		"token_uri":      fcmTokenURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.fcmFile = writeFile(t, dir, "fcm.json", sa)

	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err = x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	s.apnsFile = writeFile(t, dir, "apns.p8", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	for _, id := range ids {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		s.sealingB64[id] = b64(raw)
		s.sealing[id] = writeFile(t, dir, "sealing-"+id, []byte(s.sealingB64[id]+"\n"))
	}
	return s
}

func newKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func register(t *testing.T, base string, body map[string]string) (int, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(base+"/v1/registrations", "application/json", bytes.NewReader(raw)) //nolint:noctx // test against a local process
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return resp.StatusCode, string(out)
	}
	var got struct {
		Handle string `json:"handle"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("register response %q: %v", out, err)
	}
	return resp.StatusCode, got.Handle
}

// send posts a signed send. The caller closes the response body.
func send(t *testing.T, base string, signer ed25519.PrivateKey, keyHeader ed25519.PublicKey, ts time.Time, body map[string]any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	stamp := strconv.FormatInt(ts.Unix(), 10)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/v1/send", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Contract C spelled out, not taken from Herald, so drift there fails here.
	req.Header.Set("X-Herald-Key", b64(keyHeader))
	req.Header.Set("X-Herald-Timestamp", stamp)
	req.Header.Set("X-Herald-Signature", b64(ed25519.Sign(signer, []byte(stamp+"."+string(raw)))))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	return resp
}

func sendBody(handle, urgency, kind string) map[string]any {
	b := map[string]any{
		"handle":      handle,
		"key_id":      deviceKID,
		"urgency":     urgency,
		"kind":        kind,
		"collapse_id": collapseID,
		"ttl_seconds": 3600,
	}
	if kind == "show" {
		b["sealed"] = sealed
	}
	return b
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return m
}

func sub(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object in %v", key, m)
	}
	return v
}

// sendStatus sends and checks the status code.
func sendStatus(t *testing.T, base string, signer ed25519.PrivateKey, keyHeader ed25519.PublicKey, ts time.Time, body map[string]any, want int) {
	t.Helper()
	resp := send(t, base, signer, keyHeader, ts, body)
	defer resp.Body.Close()
	if resp.StatusCode != want {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want %d: %s", resp.StatusCode, want, out)
	}
}

// sendAccepted sends and checks for 202 with a message id.
func sendAccepted(t *testing.T, base string, signer ed25519.PrivateKey, keyHeader ed25519.PublicKey, ts time.Time, body map[string]any) {
	t.Helper()
	resp := send(t, base, signer, keyHeader, ts, body)
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", resp.StatusCode, out)
	}
	if id, _ := decode(t, out)["id"].(string); id == "" {
		t.Fatalf("no id in %s", out)
	}
}

// assertNoSecrets fails for each needle that appears in logs.
func assertNoSecrets(t *testing.T, logs string, needles []string) {
	t.Helper()
	for _, n := range needles {
		if strings.Contains(logs, n) {
			t.Errorf("logs contain %q", n)
		}
	}
}

func gatewayEnv(s secrets, current, previous string, fcm, apns *fakeProvider) map[string]string {
	vars := map[string]string{
		"PUSH_GATEWAY_ADDR":             "127.0.0.1:0",
		"PUSH_GATEWAY_SEALING_KEY_ID":   current,
		"PUSH_GATEWAY_SEALING_KEY_FILE": s.sealing[current],
	}
	if previous != "" {
		vars["PUSH_GATEWAY_PREVIOUS_SEALING_KEY_ID"] = previous
		vars["PUSH_GATEWAY_PREVIOUS_SEALING_KEY_FILE"] = s.sealing[previous]
	}
	if fcm != nil {
		vars["PUSH_GATEWAY_FCM_CREDENTIALS_FILE"] = s.fcmFile
		vars["PUSH_GATEWAY_FCM_BASE_URL"] = fcm.URL
	}
	if apns != nil {
		vars["PUSH_GATEWAY_APNS_KEY_FILE"] = s.apnsFile
		vars["PUSH_GATEWAY_APNS_KEY_ID"] = "ABCDE12345"
		vars["PUSH_GATEWAY_APNS_TEAM_ID"] = "TEAM123456"
		vars["PUSH_GATEWAY_APNS_BASE_URL"] = apns.URL
	}
	return vars
}

func TestGatewayBinary(t *testing.T) {
	bin := buildGateway(t)
	fcm, apns := newFakeFCM(t), newFakeAPNs(t)
	s := newSecrets(t, fcm.URL+"/token", "k2026b")
	p := startGateway(t, bin, gatewayEnv(s, "k2026b", "", fcm, apns))
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			p.stop(t)
		}
	})

	pubA, keyA := newKeys(t)
	pubB, keyB := newKeys(t)
	now := time.Now()
	var handles []string

	androidHandle := func(t *testing.T) string {
		t.Helper()
		code, handle := register(t, p.URL, map[string]string{"platform": "android", "token": "fcm-token-1", "server_key": b64(pubA)})
		if code != http.StatusCreated {
			t.Fatalf("register android = %d: %s", code, handle)
		}
		handles = append(handles, handle)
		return handle
	}
	iosHandle := func(t *testing.T, token string) string {
		t.Helper()
		code, handle := register(t, p.URL, map[string]string{"platform": "ios", "token": token, "apns_env": "sandbox", "server_key": b64(pubA)})
		if code != http.StatusCreated {
			t.Fatalf("register ios = %d: %s", code, handle)
		}
		handles = append(handles, handle)
		return handle
	}
	var android, ios string
	needles := func() []string {
		n := make([]string, 0, 12+len(handles))
		n = append(n,
			"fcm-token-1", iosToken, "fcm-gone", iosGoneToken, sealed, deviceKID, collapseID,
			s.sealingB64["k2026b"], "PRIVATE KEY", "fake-access-token", b64(pubA), b64(pubB),
		)
		return append(n, handles...)
	}

	t.Run("health", func(t *testing.T) {
		resp, err := http.Get(p.URL + "/_/health") //nolint:noctx // test against a local process
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(out) != "ok\n" {
			t.Fatalf("health = %d %q", resp.StatusCode, out)
		}
	})

	t.Run("android show", func(t *testing.T) {
		android = androidHandle(t)
		if !strings.HasPrefix(android, "h1.k2026b.") {
			t.Fatalf("handle %q lacks the k2026b prefix", android)
		}
		sendAccepted(t, p.URL, keyA, pubA, now, sendBody(android, "critical", "show"))
		r := fcm.last(t)
		if r.Path != "/v1/projects/loomplane-test/messages:send" {
			t.Fatalf("path = %s", r.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fake-access-token" {
			t.Fatalf("Authorization = %q", got)
		}
		msg := sub(t, decode(t, r.Body), "message")
		if msg["token"] != "fcm-token-1" {
			t.Fatalf("token = %v", msg["token"])
		}
		if _, ok := msg["notification"]; ok {
			t.Fatalf("notification present: %v", msg)
		}
		data := sub(t, msg, "data")
		want := map[string]any{"sealed": sealed, "kid": deviceKID, "kind": "show", "collapse_id": collapseID}
		if len(data) != len(want) {
			t.Fatalf("data = %v, want %v", data, want)
		}
		for k, v := range want {
			if data[k] != v {
				t.Fatalf("data[%s] = %v, want %v", k, data[k], v)
			}
		}
		a := sub(t, msg, "android")
		if a["priority"] != "HIGH" || a["collapse_key"] != collapseID || a["ttl"] != "3600s" {
			t.Fatalf("android = %v", a)
		}
	})

	t.Run("android withdraw", func(t *testing.T) {
		sendAccepted(t, p.URL, keyA, pubA, now, sendBody(android, "passive", "withdraw"))
		msg := sub(t, decode(t, fcm.last(t).Body), "message")
		data := sub(t, msg, "data")
		if data["kind"] != "withdraw" || data["collapse_id"] != collapseID {
			t.Fatalf("data = %v", data)
		}
		if _, ok := data["sealed"]; ok {
			t.Fatalf("withdraw carries sealed: %v", data)
		}
		if sub(t, msg, "android")["priority"] != "NORMAL" {
			t.Fatalf("android = %v", msg["android"])
		}
	})

	t.Run("ios show", func(t *testing.T) {
		ios = iosHandle(t, iosToken)
		sendAccepted(t, p.URL, keyA, pubA, now, sendBody(ios, "time_sensitive", "show"))
		r := apns.last(t)
		if r.Path != "/3/device/"+iosToken {
			t.Fatalf("path = %s", r.Path)
		}
		for h, want := range map[string]string{
			"apns-topic": "com.loomplane.app", "apns-push-type": "alert",
			"apns-priority": "10", "apns-collapse-id": collapseID,
		} {
			if got := r.Header.Get(h); got != want {
				t.Fatalf("%s = %q, want %q", h, got, want)
			}
		}
		exp, err := strconv.ParseInt(r.Header.Get("apns-expiration"), 10, 64)
		if err != nil || exp < now.Unix()+3590 || exp > time.Now().Unix()+3610 {
			t.Fatalf("apns-expiration = %q", r.Header.Get("apns-expiration"))
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "bearer ") {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		payload := decode(t, r.Body)
		aps := sub(t, payload, "aps")
		alert := sub(t, aps, "alert")
		if len(alert) != 2 || alert["title"] != "Loomplane" || alert["body"] != "New notification" {
			t.Fatalf("alert = %v", alert)
		}
		if aps["mutable-content"] != float64(1) || aps["interruption-level"] != "time-sensitive" {
			t.Fatalf("aps = %v", aps)
		}
		if payload["sealed"] != sealed || payload["kid"] != deviceKID || payload["collapse_id"] != collapseID {
			t.Fatalf("payload = %v", payload)
		}
	})

	t.Run("ios critical", func(t *testing.T) {
		sendAccepted(t, p.URL, keyA, pubA, now, sendBody(ios, "critical", "show"))
		aps := sub(t, decode(t, apns.last(t).Body), "aps")
		sound := sub(t, aps, "sound")
		if len(sound) != 3 || sound["critical"] != float64(1) || sound["name"] != "default" || sound["volume"] != float64(1) {
			t.Fatalf("sound = %v", sound)
		}
		if aps["interruption-level"] != "critical" {
			t.Fatalf("aps = %v", aps)
		}
	})

	t.Run("ios passive", func(t *testing.T) {
		sendAccepted(t, p.URL, keyA, pubA, now, sendBody(ios, "passive", "show"))
		r := apns.last(t)
		if r.Header.Get("apns-priority") != "5" {
			t.Fatalf("apns-priority = %q", r.Header.Get("apns-priority"))
		}
		if sub(t, decode(t, r.Body), "aps")["interruption-level"] != "passive" {
			t.Fatalf("body = %s", r.Body)
		}
	})

	t.Run("ios withdraw", func(t *testing.T) {
		sendAccepted(t, p.URL, keyA, pubA, now, sendBody(ios, "active", "withdraw"))
		r := apns.last(t)
		if r.Header.Get("apns-push-type") != "background" || r.Header.Get("apns-priority") != "5" {
			t.Fatalf("headers = %v", r.Header)
		}
		payload := decode(t, r.Body)
		aps := sub(t, payload, "aps")
		if aps["content-available"] != float64(1) {
			t.Fatalf("aps = %v", aps)
		}
		if _, ok := aps["alert"]; ok {
			t.Fatalf("aps has alert: %v", aps)
		}
		if payload["withdraw"] != collapseID {
			t.Fatalf("payload = %v", payload)
		}
	})

	t.Run("bad signature", func(t *testing.T) {
		sendStatus(t, p.URL, keyB, pubA, now, sendBody(android, "active", "show"), http.StatusUnauthorized)
	})

	t.Run("key not matching the handle", func(t *testing.T) {
		sendStatus(t, p.URL, keyB, pubB, now, sendBody(android, "active", "show"), http.StatusUnauthorized)
	})

	t.Run("stale timestamp", func(t *testing.T) {
		sendStatus(t, p.URL, keyA, pubA, now.Add(-301*time.Second), sendBody(android, "active", "show"), http.StatusUnauthorized)
	})

	t.Run("address gone", func(t *testing.T) {
		code, gone := register(t, p.URL, map[string]string{"platform": "android", "token": "fcm-gone", "server_key": b64(pubA)})
		if code != http.StatusCreated {
			t.Fatalf("register = %d: %s", code, gone)
		}
		handles = append(handles, gone)
		sendStatus(t, p.URL, keyA, pubA, now, sendBody(gone, "active", "show"), http.StatusGone)
		iosGone := iosHandle(t, iosGoneToken)
		sendStatus(t, p.URL, keyA, pubA, now, sendBody(iosGone, "active", "show"), http.StatusGone)
	})

	t.Run("override warning", func(t *testing.T) {
		if !strings.Contains(p.logs.String(), `"msg":"endpoint override in use"`) {
			t.Fatalf("no override warning in logs:\n%s", p.logs.String())
		}
	})

	t.Run("logs hold no secrets", func(t *testing.T) {
		assertNoSecrets(t, p.logs.String(), needles())
	})

	t.Run("graceful stop", func(t *testing.T) {
		stopped = true
		if code := p.stop(t); code != 0 {
			t.Fatalf("exit code = %d; logs:\n%s", code, p.logs.String())
		}
		if !strings.Contains(p.logs.String(), `"msg":"push gateway stopped"`) {
			t.Fatalf("no stop line in logs:\n%s", p.logs.String())
		}
		assertNoSecrets(t, p.logs.String(), needles())
	})
}

func TestGatewayBinaryKeyRotation(t *testing.T) {
	bin := buildGateway(t)
	fcm := newFakeFCM(t)
	s := newSecrets(t, fcm.URL+"/token", "k2026a", "k2026b")
	pub, key := newKeys(t)
	now := time.Now()

	p1 := startGateway(t, bin, gatewayEnv(s, "k2026a", "", fcm, nil))
	code, handleA := register(t, p1.URL, map[string]string{"platform": "android", "token": "fcm-token-2", "server_key": b64(pub)})
	if code != http.StatusCreated || !strings.HasPrefix(handleA, "h1.k2026a.") {
		t.Fatalf("register = %d %q", code, handleA)
	}
	if got := p1.stop(t); got != 0 {
		t.Fatalf("process 1 exit code = %d", got)
	}
	needles := append(make([]string, 0, 12),
		"fcm-token-2", "fcm-token-3", sealed, deviceKID, collapseID, b64(pub), handleA,
		s.sealingB64["k2026a"], s.sealingB64["k2026b"], "PRIVATE KEY", "fake-access-token",
	)
	assertNoSecrets(t, p1.logs.String(), needles)

	vars := gatewayEnv(s, "k2026b", "k2026a", fcm, nil)
	vars["PUSH_GATEWAY_HANDLE_RATE"] = "1"
	p2 := startGateway(t, bin, vars)
	sendAccepted(t, p2.URL, key, pub, now, sendBody(handleA, "active", "show"))
	if msg := sub(t, decode(t, fcm.last(t).Body), "message"); msg["token"] != "fcm-token-2" {
		t.Fatalf("token = %v", msg["token"])
	}
	limited := send(t, p2.URL, key, pub, now, sendBody(handleA, "active", "show"))
	limited.Body.Close()
	if limited.StatusCode != http.StatusTooManyRequests || limited.Header.Get("Retry-After") == "" {
		t.Fatalf("second send = %d, Retry-After %q; want 429 with a Retry-After", limited.StatusCode, limited.Header.Get("Retry-After"))
	}
	code, handleB := register(t, p2.URL, map[string]string{"platform": "android", "token": "fcm-token-3", "server_key": b64(pub)})
	if code != http.StatusCreated || !strings.HasPrefix(handleB, "h1.k2026b.") {
		t.Fatalf("register = %d %q", code, handleB)
	}
	needles = append(needles, handleB)
	if got := p2.stop(t); got != 0 {
		t.Fatalf("process 2 exit code = %d", got)
	}
	assertNoSecrets(t, p2.logs.String(), needles)

	p3 := startGateway(t, bin, gatewayEnv(s, "k2026b", "", fcm, nil))
	sendStatus(t, p3.URL, key, pub, now, sendBody(handleA, "active", "show"), http.StatusGone)
	if got := p3.stop(t); got != 0 {
		t.Fatalf("process 3 exit code = %d", got)
	}
	assertNoSecrets(t, p3.logs.String(), needles)
}

func TestGatewayBinaryRefusesBadConfig(t *testing.T) {
	bin := buildGateway(t)
	cmd := exec.CommandContext(t.Context(), bin)
	cmd.Env = []string{"PUSH_GATEWAY_ADDR=127.0.0.1:0"}
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("gateway kept running with no configuration")
	}
	if code := cmd.ProcessState.ExitCode(); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "PUSH_GATEWAY_SEALING_KEY_ID is required") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
