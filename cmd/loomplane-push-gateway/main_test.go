package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xraph/herald/pushgateway"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func b64Key(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func pkcs8PEM(t *testing.T, key any) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// writeSecrets writes a full set of valid secret files and returns the
// directory and the environment that points at them.
func writeSecrets(t *testing.T) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	sa, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"project_id":   "loomplane-test",
		"client_email": "push@loomplane-test.iam.gserviceaccount.com",
		"private_key":  string(pkcs8PEM(t, rsaKey)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return dir, map[string]string{
		"PUSH_GATEWAY_SEALING_KEY_ID":       "k2026b",
		"PUSH_GATEWAY_SEALING_KEY_FILE":     writeFile(t, dir, "sealing.key", []byte(b64Key(t, 32))),
		"PUSH_GATEWAY_FCM_CREDENTIALS_FILE": writeFile(t, dir, "fcm.json", sa),
		"PUSH_GATEWAY_APNS_KEY_FILE":        writeFile(t, dir, "apns.p8", pkcs8PEM(t, ecKey)),
		"PUSH_GATEWAY_APNS_KEY_ID":          "ABCDE12345",
		"PUSH_GATEWAY_APNS_TEAM_ID":         "TEAM123456",
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	_, e := writeSecrets(t)
	c, err := loadConfig(env(e))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8790" || c.ServerRate != 50 || c.HandleRate != 30 {
		t.Errorf("defaults = %q %v %v", c.Addr, c.ServerRate, c.HandleRate)
	}
	if len(c.SealingKeys) != 1 || c.SealingKeys[0].ID != "k2026b" || len(c.SealingKeys[0].Key) != 32 {
		t.Errorf("sealing keys = %+v", c.SealingKeys)
	}
	if c.FCM == nil || c.FCM.ProjectID != "loomplane-test" || c.FCM.BaseURL != "" || len(c.FCM.ServiceAccount) == 0 {
		t.Errorf("fcm = %+v", c.FCM)
	}
	if c.APNs == nil || c.APNs.BundleID != "com.loomplane.app" || c.APNs.KeyID != "ABCDE12345" ||
		c.APNs.TeamID != "TEAM123456" || len(c.APNs.PrivateKey) == 0 {
		t.Errorf("apns = %+v", c.APNs)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	_, e := writeSecrets(t)
	e["PUSH_GATEWAY_ADDR"] = "127.0.0.1:9000"
	e["PUSH_GATEWAY_SERVER_RATE"] = "5.5"
	e["PUSH_GATEWAY_HANDLE_RATE"] = "2"
	e["PUSH_GATEWAY_APNS_BUNDLE_ID"] = "com.example.app"
	e["PUSH_GATEWAY_APNS_BASE_URL"] = "http://127.0.0.1:1"
	e["PUSH_GATEWAY_FCM_BASE_URL"] = "https://fcm.test"
	c, err := loadConfig(env(e))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "127.0.0.1:9000" || c.ServerRate != 5.5 || c.HandleRate != 2 ||
		c.APNs.BundleID != "com.example.app" || c.APNs.BaseURL != "http://127.0.0.1:1" || c.FCM.BaseURL != "https://fcm.test" {
		t.Errorf("config = %+v %+v %+v", c, c.APNs, c.FCM)
	}
}

func TestLoadConfigPreviousKey(t *testing.T) {
	dir, e := writeSecrets(t)
	e["PUSH_GATEWAY_PREVIOUS_SEALING_KEY_ID"] = "k2026a"
	e["PUSH_GATEWAY_PREVIOUS_SEALING_KEY_FILE"] = writeFile(t, dir, "prev.key", []byte(b64Key(t, 32)))
	c, err := loadConfig(env(e))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.SealingKeys) != 2 || c.SealingKeys[0].ID != "k2026b" || c.SealingKeys[1].ID != "k2026a" {
		t.Errorf("sealing keys = %+v", c.SealingKeys)
	}
}

func TestLoadConfigOnePlatform(t *testing.T) {
	_, e := writeSecrets(t)
	noFCM := maps.Clone(e)
	delete(noFCM, "PUSH_GATEWAY_FCM_CREDENTIALS_FILE")
	c, err := loadConfig(env(noFCM))
	if err != nil || c.FCM != nil || c.APNs == nil {
		t.Errorf("apns only: err=%v fcm=%v apns=%v", err, c.FCM, c.APNs)
	}
	noAPNs := maps.Clone(e)
	delete(noAPNs, "PUSH_GATEWAY_APNS_KEY_FILE")
	delete(noAPNs, "PUSH_GATEWAY_APNS_KEY_ID")
	delete(noAPNs, "PUSH_GATEWAY_APNS_TEAM_ID")
	c, err = loadConfig(env(noAPNs))
	if err != nil || c.APNs != nil || c.FCM == nil {
		t.Errorf("fcm only: err=%v fcm=%v apns=%v", err, c.FCM, c.APNs)
	}
}

func TestLoadConfigRejects(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaPEM := pkcs8PEM(t, rsaKey)
	tests := []struct {
		name   string
		mutate func(t *testing.T, dir string, e map[string]string)
		want   string
	}{
		{"no key ID", func(_ *testing.T, _ string, e map[string]string) { delete(e, "PUSH_GATEWAY_SEALING_KEY_ID") },
			"PUSH_GATEWAY_SEALING_KEY_ID is required"},
		{"no key file", func(_ *testing.T, _ string, e map[string]string) { delete(e, "PUSH_GATEWAY_SEALING_KEY_FILE") },
			"PUSH_GATEWAY_SEALING_KEY_FILE is required"},
		{"dotted ID", func(_ *testing.T, _ string, e map[string]string) { e["PUSH_GATEWAY_SEALING_KEY_ID"] = "a.b" },
			`PUSH_GATEWAY_SEALING_KEY_ID must be 1 to 32 letters, digits, "-" or "_"`},
		{"short key", func(t *testing.T, d string, e map[string]string) {
			e["PUSH_GATEWAY_SEALING_KEY_FILE"] = writeFile(t, d, "short.key", []byte(b64Key(t, 31)))
		}, "PUSH_GATEWAY_SEALING_KEY_FILE must hold 32 random bytes in base64 (openssl rand -base64 32)"},
		{"previous half set", func(t *testing.T, d string, e map[string]string) {
			e["PUSH_GATEWAY_PREVIOUS_SEALING_KEY_FILE"] = writeFile(t, d, "prev.key", []byte(b64Key(t, 32)))
		}, "set PUSH_GATEWAY_PREVIOUS_SEALING_KEY_ID and PUSH_GATEWAY_PREVIOUS_SEALING_KEY_FILE together"},
		{"previous same ID", func(t *testing.T, d string, e map[string]string) {
			e["PUSH_GATEWAY_PREVIOUS_SEALING_KEY_ID"] = "k2026b"
			e["PUSH_GATEWAY_PREVIOUS_SEALING_KEY_FILE"] = writeFile(t, d, "prev.key", []byte(b64Key(t, 32)))
		}, "PUSH_GATEWAY_PREVIOUS_SEALING_KEY_ID must differ from PUSH_GATEWAY_SEALING_KEY_ID"},
		{"no platform", func(_ *testing.T, _ string, e map[string]string) {
			delete(e, "PUSH_GATEWAY_FCM_CREDENTIALS_FILE")
			delete(e, "PUSH_GATEWAY_APNS_KEY_FILE")
		}, "set PUSH_GATEWAY_FCM_CREDENTIALS_FILE, PUSH_GATEWAY_APNS_KEY_FILE or both"},
		{"not a service account", func(t *testing.T, d string, e map[string]string) {
			e["PUSH_GATEWAY_FCM_CREDENTIALS_FILE"] = writeFile(t, d, "bad.json", []byte(`{"type":"authorized_user"}`))
		}, "PUSH_GATEWAY_FCM_CREDENTIALS_FILE isn't a Firebase service-account key"},
		{"not a p8", func(t *testing.T, d string, e map[string]string) {
			e["PUSH_GATEWAY_APNS_KEY_FILE"] = writeFile(t, d, "rsa.p8", rsaPEM)
		}, "PUSH_GATEWAY_APNS_KEY_FILE isn't an APNs .p8 key"},
		{"APNs key ID", func(_ *testing.T, _ string, e map[string]string) { delete(e, "PUSH_GATEWAY_APNS_KEY_ID") },
			"PUSH_GATEWAY_APNS_KEY_ID must be the 10-character key ID from Apple"},
		{"APNs team", func(_ *testing.T, _ string, e map[string]string) { e["PUSH_GATEWAY_APNS_TEAM_ID"] = "team" },
			"PUSH_GATEWAY_APNS_TEAM_ID must be the 10-character team ID from Apple"},
		{"rate zero", func(_ *testing.T, _ string, e map[string]string) { e["PUSH_GATEWAY_SERVER_RATE"] = "0" },
			"PUSH_GATEWAY_SERVER_RATE must be a positive number"},
		{"rate text", func(_ *testing.T, _ string, e map[string]string) { e["PUSH_GATEWAY_HANDLE_RATE"] = "fast" },
			"PUSH_GATEWAY_HANDLE_RATE must be a positive number"},
		{"rate infinite", func(_ *testing.T, _ string, e map[string]string) { e["PUSH_GATEWAY_HANDLE_RATE"] = "Inf" },
			"PUSH_GATEWAY_HANDLE_RATE must be a positive number"},
		{"base URL", func(_ *testing.T, _ string, e map[string]string) { e["PUSH_GATEWAY_APNS_BASE_URL"] = "ftp://x" },
			"PUSH_GATEWAY_APNS_BASE_URL must be an http or https URL"},
		{"missing file", func(_ *testing.T, d string, e map[string]string) {
			e["PUSH_GATEWAY_SEALING_KEY_FILE"] = filepath.Join(d, "nope")
		}, "PUSH_GATEWAY_SEALING_KEY_FILE: open "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, e := writeSecrets(t)
			tc.mutate(t, dir, e)
			_, err := loadConfig(env(e))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if tc.name == "missing file" && !strings.Contains(err.Error(), filepath.Join(dir, "nope")) {
				t.Errorf("err = %v, want the path", err)
			}
		})
	}
}

func TestLoadConfigReportsEveryProblem(t *testing.T) {
	_, err := loadConfig(env(nil))
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{
		"PUSH_GATEWAY_SEALING_KEY_ID is required",
		"set PUSH_GATEWAY_FCM_CREDENTIALS_FILE, PUSH_GATEWAY_APNS_KEY_FILE or both",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want %q", err, want)
		}
	}
}

func TestLoadConfigTrimsSecretFiles(t *testing.T) {
	dir, e := writeSecrets(t)
	for _, tail := range []string{"\n", "\r\n"} {
		e["PUSH_GATEWAY_SEALING_KEY_FILE"] = writeFile(t, dir, "sealing.key", []byte(b64Key(t, 32)+tail))
		if _, err := loadConfig(env(e)); err != nil {
			t.Errorf("sealing key with %q: %v", tail, err)
		}
	}
	p8, err := os.ReadFile(e["PUSH_GATEWAY_APNS_KEY_FILE"])
	if err != nil {
		t.Fatal(err)
	}
	crlf := bytes.ReplaceAll(p8, []byte("\n"), []byte("\r\n"))
	e["PUSH_GATEWAY_APNS_KEY_FILE"] = writeFile(t, dir, "crlf.p8", crlf)
	if _, err := loadConfig(env(e)); err != nil {
		t.Errorf("crlf p8: %v", err)
	}
}

func TestLoadConfigUnreadableSecret(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	_, e := writeSecrets(t)
	if err := os.Chmod(e["PUSH_GATEWAY_SEALING_KEY_FILE"], 0o000); err != nil {
		t.Fatal(err)
	}
	_, err := loadConfig(env(e))
	if err == nil || !strings.Contains(err.Error(), "PUSH_GATEWAY_SEALING_KEY_FILE: open ") ||
		!strings.Contains(err.Error(), "permission denied") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadConfigErrorsHideSecrets(t *testing.T) {
	dir, e := writeSecrets(t)
	e["PUSH_GATEWAY_SEALING_KEY_FILE"] = writeFile(t, dir, "s.key", []byte("SECRETSECRETSECRET"))
	e["PUSH_GATEWAY_FCM_CREDENTIALS_FILE"] = writeFile(t, dir, "s.json",
		[]byte(`{"type":"x","private_key":"SECRETSECRETSECRET"}`))
	e["PUSH_GATEWAY_APNS_KEY_FILE"] = writeFile(t, dir, "s.p8", []byte("SECRETSECRETSECRET"))
	_, err := loadConfig(env(e))
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "SECRETSECRETSECRET") {
		t.Errorf("error leaks a secret: %v", err)
	}
}

func TestRunVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"--version"}, env(nil), &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr %q", code, errb.String())
	}
	if out.String() != "loomplane-push-gateway 0.1.0-dev\n" {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), nil, env(nil), &out, &errb); code != 1 {
		t.Fatalf("code = %d", code)
	}
	if !strings.HasPrefix(errb.String(), "loomplane-push-gateway: ") ||
		!strings.Contains(errb.String(), "PUSH_GATEWAY_SEALING_KEY_ID is required") {
		t.Errorf("stderr = %q", errb.String())
	}
	errb.Reset()
	if code := run(context.Background(), []string{"serve"}, env(nil), &out, &errb); code != 2 {
		t.Errorf("positional code = %d", code)
	}
}

func TestRunAcceptsGoodConfig(t *testing.T) {
	_, e := writeSecrets(t)
	e["PUSH_GATEWAY_ADDR"] = "127.0.0.1:0"
	e["PUSH_GATEWAY_FCM_BASE_URL"] = "http://fcm.test"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errb bytes.Buffer
	if code := run(ctx, nil, env(e), &out, &errb); code != 0 {
		t.Errorf("code = %d, stderr %q", code, errb.String())
	}
}

func TestLoadConfigRejectsOrphanedPlatformSettings(t *testing.T) {
	tests := []struct {
		drop, set, value string
		keep             string
	}{
		{"PUSH_GATEWAY_APNS_KEY_FILE", "PUSH_GATEWAY_APNS_KEY_ID", "ABCDE12345", "fcm"},
		{"PUSH_GATEWAY_APNS_KEY_FILE", "PUSH_GATEWAY_APNS_TEAM_ID", "TEAM123456", "fcm"},
		{"PUSH_GATEWAY_APNS_KEY_FILE", "PUSH_GATEWAY_APNS_BUNDLE_ID", "com.example.app", "fcm"},
		{"PUSH_GATEWAY_APNS_KEY_FILE", "PUSH_GATEWAY_APNS_BASE_URL", "http://127.0.0.1:1", "fcm"},
		{"PUSH_GATEWAY_FCM_CREDENTIALS_FILE", "PUSH_GATEWAY_FCM_BASE_URL", "http://127.0.0.1:1", "apns"},
	}
	for _, tc := range tests {
		t.Run(tc.set, func(t *testing.T) {
			_, e := writeSecrets(t)
			delete(e, tc.drop)
			e[tc.set] = tc.value
			if tc.drop == "PUSH_GATEWAY_APNS_KEY_FILE" {
				delete(e, "PUSH_GATEWAY_APNS_KEY_ID")
				delete(e, "PUSH_GATEWAY_APNS_TEAM_ID")
				e[tc.set] = tc.value
			}
			want := tc.set + " is set but " + tc.drop + " isn't"
			_, err := loadConfig(env(e))
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want %q", err, want)
			}
		})
	}
}

func TestGatewayConfig(t *testing.T) {
	_, e := writeSecrets(t)
	prevDir := t.TempDir()
	e["PUSH_GATEWAY_PREVIOUS_SEALING_KEY_ID"] = "k2026a"
	e["PUSH_GATEWAY_PREVIOUS_SEALING_KEY_FILE"] = writeFile(t, prevDir, "prev.key", []byte(b64Key(t, 32)))
	e["PUSH_GATEWAY_FCM_BASE_URL"] = "http://fcm.test"
	e["PUSH_GATEWAY_APNS_BASE_URL"] = "http://apns.test"
	c, err := loadConfig(env(e))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	g := gatewayConfig(c, logger)

	want := []pushgateway.SealingKey{{ID: "k2026b", Key: c.SealingKeys[0].Key}, {ID: "k2026a", Key: c.SealingKeys[1].Key}}
	if !reflect.DeepEqual(g.SealingKeys, want) {
		t.Errorf("SealingKeys = %v", g.SealingKeys)
	}
	if g.Android.Driver.Name() != "fcm" || g.Android.Credentials["service_account"] != string(c.FCM.ServiceAccount) {
		t.Errorf("Android = %+v", g.Android)
	}
	if want := map[string]string{"project_id": "loomplane-test", "base_url": "http://fcm.test"}; !maps.Equal(g.Android.Settings, want) {
		t.Errorf("Android settings = %v", g.Android.Settings)
	}
	if g.IOS.Driver.Name() != "apns" || g.IOS.Credentials["private_key"] != string(c.APNs.PrivateKey) {
		t.Errorf("IOS = %+v", g.IOS)
	}
	wantIOS := map[string]string{"key_id": "ABCDE12345", "team_id": "TEAM123456", "bundle_id": "com.loomplane.app", "base_url": "http://apns.test"}
	if !maps.Equal(g.IOS.Settings, wantIOS) {
		t.Errorf("IOS settings = %v", g.IOS.Settings)
	}
	if g.ServerPerSecond != 50 || g.HandlePerMinute != 30 || g.Logger != logger {
		t.Errorf("rates/logger = %v %v %v", g.ServerPerSecond, g.HandlePerMinute, g.Logger)
	}
	if g.PlaceholderTitle != "Loomplane" || g.PlaceholderBody != "New notification" {
		t.Errorf("placeholders = %q %q", g.PlaceholderTitle, g.PlaceholderBody)
	}

	c.FCM = nil
	c.APNs.BaseURL = ""
	g = gatewayConfig(c, logger)
	if g.Android != nil {
		t.Errorf("Android = %+v, want nil", g.Android)
	}
	if _, ok := g.IOS.Settings["base_url"]; ok {
		t.Errorf("IOS settings has base_url: %v", g.IOS.Settings)
	}
	c.APNs = nil
	if g = gatewayConfig(c, logger); g.IOS != nil {
		t.Errorf("IOS = %+v, want nil", g.IOS)
	}
}

func TestHandlerHealth(t *testing.T) {
	var gotPath string
	called := 0
	stub := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called++
		gotPath = r.URL.Path
	})
	h := newHandler(stub)
	do := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), method, path, nil))
		return rec
	}
	if rec := do(http.MethodGet, "/_/health"); rec.Code != 200 || rec.Body.String() != "ok\n" {
		t.Errorf("health = %d %q", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodPost, "/_/health"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST health = %d", rec.Code)
	}
	if rec := do(http.MethodGet, "/nope"); rec.Code != http.StatusNotFound || called != 0 {
		t.Errorf("/nope = %d, called %d", rec.Code, called)
	}
	do(http.MethodPost, "/v1/send")
	if called != 1 || gotPath != "/v1/send" {
		t.Errorf("called %d with %q", called, gotPath)
	}
}

func TestHandlerLimitsBody(t *testing.T) {
	var readErr error
	stub := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	})
	h := newHandler(stub)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/send", bytes.NewReader(make([]byte, maxBody+1)))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !errors.As(readErr, new(*http.MaxBytesError)) {
		t.Errorf("readErr = %v", readErr)
	}
}

func TestServeFinishesInFlight(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	h := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	served := make(chan error, 1)
	go func() { served <- serve(ctx, ln, h, logger) }()

	status := make(chan int, 1)
	go func() {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
		if err != nil {
			status <- -1
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			status <- -1
			return
		}
		resp.Body.Close()
		status <- resp.StatusCode
	}()
	<-started
	cancel()
	time.Sleep(100 * time.Millisecond)
	if conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(context.Background(), "tcp", ln.Addr().String()); err == nil {
		conn.Close()
		t.Error("new connection accepted after shutdown began")
	}
	close(release)
	if got := <-status; got != 200 {
		t.Errorf("in-flight status = %d", got)
	}
	if err := <-served; err != nil {
		t.Errorf("serve = %v", err)
	}
}

func TestGatewayImportsNoServerPackages(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "go", "list", "-deps", "./cmd/loomplane-push-gateway")
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	const self = "github.com/xraph/loomplane/"
	for _, pkg := range strings.Fields(string(out)) {
		if !strings.HasPrefix(pkg, self) {
			continue
		}
		if pkg != self+"cmd/loomplane-push-gateway" && pkg != self+"internal/buildinfo" {
			t.Errorf("gateway imports %s", pkg)
		}
	}
}
