package hfhub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// testToken looks like a Hugging Face token and is nobody's.
const testToken = "hf_testTOKENvalue0123456789abcdefgh"

const qwenSHA = "a09a35458c702b33eeacc393d103063234e8bc28"

func TestModelReadsMetadata(t *testing.T) {
	// The Hub's answer for Qwen/Qwen2.5-7B-Instruct at the registry's pin,
	// trimmed: the fields Loomplane reads, and enough around them to show
	// the rest is ignored.
	fixture, err := os.ReadFile("testdata/qwen2.5-7b-instruct.json")
	if err != nil {
		t.Fatal(err)
	}
	var gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotAuth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	m, err := New(srv.URL, srv.Client()).Model(context.Background(), "Qwen/Qwen2.5-7B-Instruct", qwenSHA, testToken)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/models/Qwen/Qwen2.5-7B-Instruct/revision/"+qwenSHA || gotQuery != "blobs=true" {
		t.Errorf("request %s?%s", gotPath, gotQuery)
	}
	if gotAuth != "Bearer "+testToken {
		t.Errorf("Authorization %q, want the token", gotAuth)
	}
	if m.ID != "Qwen/Qwen2.5-7B-Instruct" || m.SHA != qwenSHA || m.PipelineTag != "text-generation" || m.LibraryName != "transformers" {
		t.Errorf("model = %+v", m)
	}
	if m.Gated || m.License != "apache-2.0" || m.Params != 7615616512 || m.Downloads != 1843210 || m.Likes != 812 {
		t.Errorf("gated %v, license %q, params %d, downloads %d, likes %d", m.Gated, m.License, m.Params, m.Downloads, m.Likes)
	}
	if !slices.Contains(m.Tags, "conversational") {
		t.Errorf("tags %v", m.Tags)
	}
	sizes := map[string]int64{}
	var weights int64
	for _, f := range m.Files {
		sizes[f.Name] = f.Size
		if strings.HasSuffix(f.Name, ".safetensors") {
			weights += f.Size
		}
	}
	if len(m.Files) != 14 || sizes["config.json"] != 663 || sizes["model-00001-of-00004.safetensors"] != 3945441440 {
		t.Errorf("files %v", m.Files)
	}
	// The last shard has only its LFS size, as some listings do.
	if sizes["model-00004-of-00004.safetensors"] != 3556377672 {
		t.Errorf("LFS-only size %d", sizes["model-00004-of-00004.safetensors"])
	}
	// The registry's bf16 weight_bytes for this entry.
	if weights != 15231271888 {
		t.Errorf("weights total %d, want 15231271888", weights)
	}

	// No revision means main; no token means no Authorization header.
	if _, err := New(srv.URL, nil).Model(context.Background(), "Qwen/Qwen2.5-7B-Instruct", "", ""); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/models/Qwen/Qwen2.5-7B-Instruct/revision/main" || gotAuth != "" {
		t.Errorf("anonymous request %s with Authorization %q", gotPath, gotAuth)
	}
}

func TestModelReadsGated(t *testing.T) {
	for raw, want := range map[string]bool{`false`: false, `"auto"`: true, `"manual"`: true, `true`: true} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"id":"meta-llama/Llama-3.1-8B-Instruct","sha":"` + qwenSHA + `","gated":` + raw + `,"tags":["license:llama3.1"]}`))
		}))
		m, err := New(srv.URL, nil).Model(context.Background(), "meta-llama/Llama-3.1-8B-Instruct", "", "")
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if m.Gated != want || m.License != "llama3.1" {
			t.Errorf("gated %s: Gated %v, License %q", raw, m.Gated, m.License)
		}
	}
}

func TestAccessReportsGated(t *testing.T) {
	var (
		mu     sync.Mutex
		status int
		header string
		method string
		path   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		method, path = r.Method, r.URL.Path
		if header != "" {
			w.Header().Set("X-Error-Code", header)
		}
		if status == http.StatusFound {
			w.Header().Set("Location", "https://cdn-lfs.huggingface.co/config.json")
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	c := New(srv.URL, nil)

	for _, tt := range []struct {
		status int
		header string
		want   error
	}{
		{http.StatusUnauthorized, "", ErrGated},
		{http.StatusForbidden, "", ErrGated},
		{http.StatusUnauthorized, "GatedRepo", ErrGated},
		{http.StatusNotFound, "", ErrNotFound},
		// An anonymous request for a missing repo gets 401 and this header.
		{http.StatusUnauthorized, "RepoNotFound", ErrNotFound},
		{http.StatusOK, "", nil},
		// A redirect to the file's CDN counts as access; it isn't followed.
		{http.StatusFound, "", nil},
		{http.StatusBadGateway, "", ErrUnavailable},
	} {
		mu.Lock()
		status, header = tt.status, tt.header
		mu.Unlock()
		err := c.Access(context.Background(), "meta-llama/Llama-3.1-8B-Instruct", qwenSHA, testToken)
		if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
			t.Errorf("%d %s: err = %v, want %v", tt.status, tt.header, err, tt.want)
		}
	}
	if method != http.MethodHead || path != "/meta-llama/Llama-3.1-8B-Instruct/resolve/"+qwenSHA+"/config.json" {
		t.Errorf("request %s %s", method, path)
	}

	// Access takes a commit SHA, never a branch.
	if err := c.Access(context.Background(), "meta-llama/Llama-3.1-8B-Instruct", "main", testToken); err == nil {
		t.Error("Access at main: no error")
	}
}

// hostRouter is an http.RoundTripper that serves each host from its own
// handler, so a test can redirect between real Hugging Face host names
// without a network. It records the Authorization header each host saw.
type hostRouter struct {
	mu    sync.Mutex
	hosts map[string]http.HandlerFunc
	auth  map[string][]string
}

func (h *hostRouter) RoundTrip(r *http.Request) (*http.Response, error) {
	h.mu.Lock()
	handler, ok := h.hosts[r.URL.Scheme+"://"+r.URL.Host]
	h.auth[r.URL.Host] = append(h.auth[r.URL.Host], r.Header.Get("Authorization"))
	h.mu.Unlock()
	if !ok {
		return nil, errors.New("no route to " + r.URL.Host)
	}
	rec := httptest.NewRecorder()
	handler(rec, r)
	resp := rec.Result()
	resp.Request = r
	return resp, nil
}

func (h *hostRouter) seen(host string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.auth[host])
}

func redirectTo(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target, http.StatusFound) }
}

func TestTokenOnlyGoesToHuggingFace(t *testing.T) {
	ctx := context.Background()

	// Two local servers: the Hub redirects to another host, which must never
	// be asked, let alone see the token.
	var otherHits int
	var otherAuth string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits++
		otherAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"name":"evil"}`))
	}))
	defer other.Close()
	hub := httptest.NewServer(redirectTo(other.URL + "/steal"))
	defer hub.Close()
	c := New(hub.URL, nil)
	if _, err := c.Model(ctx, "Qwen/Qwen2.5-7B-Instruct", "", testToken); err == nil {
		t.Error("Model followed a redirect to another host")
	}
	if _, err := c.RawFile(ctx, "Qwen/Qwen2.5-7B-Instruct", qwenSHA, "config.json", testToken, 1<<20); err == nil {
		t.Error("RawFile followed a redirect to another host")
	}
	if _, err := c.WhoAmI(ctx, testToken); err == nil {
		t.Error("WhoAmI followed a redirect to another host")
	}
	if otherHits != 0 || otherAuth != "" {
		t.Errorf("the other host was asked %d times, with Authorization %q", otherHits, otherAuth)
	}

	// The Hub's own host names, served in memory.
	router := &hostRouter{auth: map[string][]string{}, hosts: map[string]http.HandlerFunc{
		"https://huggingface.co": func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasPrefix(r.URL.Path, "/api/models/old/name"):
				// A renamed repo: a redirect on the same host keeps the token.
				http.Redirect(w, r, "/api/models/new/name/revision/main", http.StatusTemporaryRedirect)
			case strings.HasPrefix(r.URL.Path, "/api/models/new/name"):
				_, _ = w.Write([]byte(`{"id":"new/name","sha":"` + qwenSHA + `"}`))
			case strings.HasPrefix(r.URL.Path, "/api/models/"):
				http.Redirect(w, r, "https://cdn-lfs.huggingface.co/meta", http.StatusFound)
			case strings.Contains(r.URL.Path, "/lfs-file"):
				http.Redirect(w, r, "https://cdn-lfs-us-1.huggingface.co/blob?signed=1", http.StatusFound)
			case strings.Contains(r.URL.Path, "/xet-file"):
				http.Redirect(w, r, "https://cas-bridge.xethub.hf.co/blob?signed=1", http.StatusFound)
			case strings.Contains(r.URL.Path, "/plain-cdn"):
				http.Redirect(w, r, "http://cdn-lfs.huggingface.co/blob", http.StatusFound)
			case strings.Contains(r.URL.Path, "/look-alike"):
				http.Redirect(w, r, "https://cdn-lfs.huggingface.co.evil.example/blob", http.StatusFound)
			default:
				http.Redirect(w, r, "https://evil.example/blob", http.StatusFound)
			}
		},
		"https://cdn-lfs.huggingface.co":              func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("weights")) },
		"https://cdn-lfs-us-1.huggingface.co":         func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("weights")) },
		"https://cas-bridge.xethub.hf.co":             func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("xet")) },
		"http://cdn-lfs.huggingface.co":               func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("plain")) },
		"https://cdn-lfs.huggingface.co.evil.example": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("evil")) },
		"https://evil.example":                        func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("evil")) },
	}}
	c = New(DefaultURL, &http.Client{Transport: router})

	for file, want := range map[string]string{"lfs-file": "weights", "xet-file": "xet"} {
		got, err := c.RawFile(ctx, "acme/model", qwenSHA, file, testToken, 1<<20)
		if err != nil || string(got) != want {
			t.Errorf("RawFile %s = %q, %v; want %q", file, got, err, want)
		}
	}
	for _, file := range []string{"plain-cdn", "look-alike", "elsewhere"} {
		if _, err := c.RawFile(ctx, "acme/model", qwenSHA, file, testToken, 1<<20); err == nil {
			t.Errorf("RawFile %s followed the redirect", file)
		}
	}
	// Only a file download may go to the CDN.
	if _, err := c.Model(ctx, "acme/model", "", testToken); err == nil {
		t.Error("Model followed a redirect to the CDN")
	}
	if m, err := c.Model(ctx, "old/name", "", testToken); err != nil || m.ID != "new/name" {
		t.Errorf("Model through a same-host redirect = %+v, %v", m, err)
	}

	for host, auths := range router.auth {
		for _, a := range auths {
			if host != "huggingface.co" && a != "" {
				t.Errorf("%s got Authorization %q", host, a)
			}
		}
	}
	for _, host := range []string{"cdn-lfs-us-1.huggingface.co", "cas-bridge.xethub.hf.co"} {
		if len(router.seen(host)) == 0 {
			t.Errorf("%s was never asked; the test proves nothing about it", host)
		}
	}
	for _, host := range []string{"cdn-lfs.huggingface.co.evil.example", "evil.example"} {
		if n := len(router.seen(host)); n != 0 {
			t.Errorf("%s was asked %d times", host, n)
		}
	}
	for i, a := range router.seen("huggingface.co") {
		if a != "Bearer "+testToken {
			t.Errorf("Hub request %d had Authorization %q, want the token", i, a)
		}
	}
}

func TestErrorsNeverCarryTheToken(t *testing.T) {
	ctx := context.Background()
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/redirect") {
			// The token in the Location the client refuses to follow.
			http.Redirect(w, r, "https://evil.example/?t="+testToken, http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := New(srv.URL, nil)

	cases := map[string]string{
		"echoed":         "upstream said: Authorization: Bearer " + testToken + " is broken",
		"twice":          testToken + " and " + testToken,
		"across the cut": strings.Repeat("x", 470) + testToken + strings.Repeat("y", 100),
		"past the read":  strings.Repeat("z", 4096-10) + testToken,
		"long and plain": strings.Repeat("é", 2000),
	}
	for name, b := range cases {
		body = b
		_, err := c.Model(ctx, "Qwen/Qwen2.5-7B-Instruct", "", testToken)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s: err = %v, want ErrUnavailable", name, err)
		}
		checkNoToken(t, name, err)
	}

	body = "token " + testToken
	_, err := c.WhoAmI(ctx, testToken)
	checkNoToken(t, "whoami", err)
	_, err = c.RawFile(ctx, "acme/model", qwenSHA, "redirect", testToken, 1<<20)
	if err == nil {
		t.Fatal("RawFile followed a redirect off the Hub")
	}
	checkNoToken(t, "redirect", err)
	if !strings.Contains(err.Error(), redacted) {
		t.Errorf("redirect: %q doesn't show where the token was", err)
	}
}

func checkNoToken(t *testing.T, name string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: no error", name)
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		msg := e.Error()
		if strings.Contains(msg, testToken[:8]) {
			t.Errorf("%s: error carries the token: %q", name, msg)
		}
		if len(msg) > maxErrorBytes {
			t.Errorf("%s: error is %d bytes", name, len(msg))
		}
	}
}

func TestRedactTakesOffAPartialToken(t *testing.T) {
	for _, s := range []string{"abc " + testToken[:10], "abc " + testToken[:len(testToken)-1]} {
		if got := Redact(s, testToken); got != "abc "+redacted {
			t.Errorf("Redact(%q) = %q", s, got)
		}
	}
	if got := Redact("abc hf_", testToken); got != "abc hf_" {
		t.Errorf("Redact of a bare prefix = %q", got)
	}
}

func TestOversizedResponsesAreRefused(t *testing.T) {
	ctx := context.Background()
	var size int
	var declare bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if declare {
			w.Header().Set("Content-Length", strconv.Itoa(size))
		}
		chunk := []byte(strings.Repeat(" ", 64<<10))
		for left := size; left > 0; left -= len(chunk) {
			if _, err := w.Write(chunk[:min(left, len(chunk))]); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	c := New(srv.URL, nil)

	for _, declared := range []bool{true, false} {
		declare = declared
		size = maxModelBytes + 1
		if _, err := c.Model(ctx, "Qwen/Qwen2.5-7B-Instruct", "", ""); !errors.Is(err, ErrTooLarge) || !strings.Contains(err.Error(), "limit") {
			t.Errorf("Model over 4 MiB (Content-Length %v): err = %v", declared, err)
		}
		size = 11
		if _, err := c.RawFile(ctx, "acme/model", qwenSHA, "config.json", "", 10); !errors.Is(err, ErrTooLarge) || !strings.Contains(err.Error(), "limit") {
			t.Errorf("RawFile of 11 bytes with a limit of 10 (Content-Length %v): err = %v", declared, err)
		}
		size = 10
		if got, err := c.RawFile(ctx, "acme/model", qwenSHA, "config.json", "", 10); err != nil || len(got) != 10 {
			t.Errorf("RawFile of 10 bytes with a limit of 10 (Content-Length %v) = %d bytes, %v", declared, len(got), err)
		}
	}
	if _, err := c.RawFile(ctx, "acme/model", qwenSHA, "config.json", "", 0); err == nil {
		t.Error("RawFile with no limit: no error")
	}
}

func TestWhoAmI(t *testing.T) {
	ctx := context.Background()
	var calls int
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/whoami-v2" || r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("request %s with Authorization %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"type":"user","name":"rex","auth":{"accessToken":{"role":"read"}}}`))
	}))
	defer srv.Close()
	c := New(srv.URL, nil)

	if name, err := c.WhoAmI(ctx, testToken); err != nil || name != "rex" {
		t.Errorf("WhoAmI = %q, %v", name, err)
	}
	status = http.StatusUnauthorized
	if _, err := c.WhoAmI(ctx, testToken); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("WhoAmI on 401: err = %v, want ErrUnauthorized", err)
	}
	before := calls
	if _, err := c.WhoAmI(ctx, ""); !errors.Is(err, ErrUnauthorized) || calls != before {
		t.Errorf("WhoAmI with no token: err = %v after %d requests", err, calls-before)
	}
}

func TestPathsStayInTheRepo(t *testing.T) {
	ctx := context.Background()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := New(srv.URL, nil)

	for _, repo := range []string{"", "acme", "acme/model/extra", "../acme", "acme/..", "acme/a..b", "-acme/model", "acme/model?x=1"} {
		if ValidRepo(repo) {
			t.Errorf("ValidRepo(%q) = true", repo)
		}
		if _, err := c.RawFile(ctx, repo, qwenSHA, "config.json", "", 10); err == nil {
			t.Errorf("RawFile in repo %q: no error", repo)
		}
	}
	for _, path := range []string{"", "/config.json", "../config.json", "a/../../b", "a//b", "a\\b"} {
		if _, err := c.RawFile(ctx, "acme/model", qwenSHA, path, "", 10); err == nil {
			t.Errorf("RawFile of %q: no error", path)
		}
	}
	if _, err := c.RawFile(ctx, "acme/model", "main", "config.json", "", 10); err == nil {
		t.Error("RawFile at main: no error")
	}
	if len(paths) != 0 {
		t.Errorf("refused requests reached the Hub: %v", paths)
	}

	if _, err := c.RawFile(ctx, "acme/model", qwenSHA, "onnx/model #1?.json", "", 10); err != nil {
		t.Fatal(err)
	}
	if want := "/acme/model/resolve/" + qwenSHA + "/onnx/model%20%231%3F.json"; len(paths) != 1 || paths[0] != want {
		t.Errorf("paths %v, want %s", paths, want)
	}
}
