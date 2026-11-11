package catalogconnect

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/recipes"
)

const elasticRoot = `{"version":{"number":"9.4.1","build_flavor":"default"},"tagline":"You Know, for Search"}`
const openSearchRoot = `{"version":{"number":"3.9.0","distribution":"opensearch"},"tagline":"The OpenSearch Project: https://opensearch.org/"}`

func searchNativeHandler(engine string, requests *[]string, mu *sync.Mutex) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*requests = append(*requests, r.URL.RequestURI())
		mu.Unlock()
		if r.Method != http.MethodGet || r.Host != "search.example:9200" {
			w.WriteHeader(400)
			return
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "app" || password != searchAccess(engine).Password {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/":
			if engine == EngineElasticsearch {
				w.Header().Set("X-Elastic-Product", "Elasticsearch")
				_, _ = fmt.Fprint(w, elasticRoot)
			} else {
				_, _ = fmt.Fprint(w, openSearchRoot)
			}
		case "/_security/_authenticate":
			_, _ = fmt.Fprint(w, `{"username":"app","authentication_type":"realm"}`)
		case "/_plugins/_security/authinfo":
			_, _ = fmt.Fprint(w, `{"user_name":"app","roles":["read-health"]}`)
		case "/_cluster/health":
			_, _ = fmt.Fprint(w, `{"status":"yellow","timed_out":false,"number_of_nodes":1}`)
		default:
			w.WriteHeader(404)
		}
	}
}

func searchTestCA(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "search test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	root, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"search.example"}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	cert, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{cert, root}, PrivateKey: leafKey}, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root}))
}

func searchTestServer(t *testing.T, engine string, handler http.Handler) (*httptest.Server, string, DialFunc, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	var server *httptest.Server
	var ca string
	if engine == EngineOpenSearch {
		cert, root := searchTestCA(t)
		ca = root
		server = httptest.NewUnstartedServer(handler)
		server.Config.ErrorLog = log.New(io.Discard, "", 0)
		server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
		server.StartTLS()
	} else {
		server = httptest.NewServer(handler)
	}
	t.Cleanup(server.Close)
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		calls.Add(1)
		if network != "tcp" || address != "search.example:9200" {
			return nil, errors.New("different target")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}
	return server, ca, dial, calls
}

func TestSearchProbeUsesOnlyGuardedNativeRequests(t *testing.T) {
	for _, engine := range []string{EngineElasticsearch, EngineOpenSearch} {
		t.Run(engine, func(t *testing.T) {
			var requests []string
			var mu sync.Mutex
			_, ca, dial, calls := searchTestServer(t, engine, searchNativeHandler(engine, &requests, &mu))
			a := searchAccess(engine)
			a.CACert = ca
			raw, err := EncodeEngineAccess(a)
			if err != nil {
				t.Fatal(err)
			}
			report, err := (SearchProber{}).Probe(t.Context(), dial, raw, recipes.Requirement{Engine: engine})
			if err != nil || !report.Result().OK {
				t.Fatalf("native probe failed: %v", err)
			}
			auth := "/_security/_authenticate"
			if engine == EngineOpenSearch {
				auth = "/_plugins/_security/authinfo"
			}
			want := []string{"/", auth, "/_cluster/health?local=true&timeout=1s&filter_path=status,timed_out,number_of_nodes"}
			if !slices.Equal(requests, want) || calls.Load() != 3 {
				t.Fatalf("unexpected request sequence: %v, dials %d", requests, calls.Load())
			}
		})
	}
}

func TestSearchProbeRejectsWrongIdentityAuthVersionAndResponses(t *testing.T) {
	cases := []struct {
		name, path, body string
		status           int
		header           string
	}{
		{name: "wrong product", path: "/", body: openSearchRoot, header: "Elasticsearch"},
		{name: "missing identity header", path: "/", body: elasticRoot},
		{name: "wrong version", path: "/", body: strings.Replace(elasticRoot, "9.4.1", "8.19.0", 1), header: "Elasticsearch"},
		{name: "malformed version", path: "/", body: strings.Replace(elasticRoot, "9.4.1", "9.4.1-secret", 1), header: "Elasticsearch"},
		{name: "duplicate root", path: "/", body: strings.Replace(elasticRoot, `"tagline":`, `"tagline":"spoof","tagline":`, 1), header: "Elasticsearch"},
		{name: "body limit", path: "/", body: strings.Repeat("x", maxSearchBody+1), header: "Elasticsearch"},
		{name: "trailing object", path: "/", body: elasticRoot + "{}", header: "Elasticsearch"},
		{name: "wrong auth identity", path: "/_security/_authenticate", body: `{"username":"anonymous","authentication_type":"realm"}`},
		{name: "wrong auth type", path: "/_security/_authenticate", body: `{"username":"app","authentication_type":"anonymous"}`},
		{name: "auth refused", path: "/_security/_authenticate", status: 401, body: "remote echoed s3cret:short/x?"},
		{name: "read refused", path: "/_cluster/health", status: 403, body: "remote echoed s3cret:short/x?"},
		{name: "red cluster", path: "/_cluster/health", body: `{"status":"red","timed_out":false,"number_of_nodes":1}`},
		{name: "no timeout field", path: "/_cluster/health", body: `{"status":"green","number_of_nodes":1}`},
		{name: "redirect", path: "/", status: 302, body: "remote echoed s3cret:short/x?"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests []string
			var mu sync.Mutex
			native := searchNativeHandler(EngineElasticsearch, &requests, &mu)
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					native.ServeHTTP(w, r)
					return
				}
				if tc.header != "" {
					w.Header().Set("X-Elastic-Product", tc.header)
				}
				w.Header().Set("Location", "http://other.example/leak")
				status := tc.status
				if status == 0 {
					status = 200
				}
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, tc.body)
			})
			_, _, dial, calls := searchTestServer(t, EngineElasticsearch, handler)
			raw, _ := EncodeEngineAccess(searchAccess(EngineElasticsearch))
			report, err := (SearchProber{}).Probe(t.Context(), dial, raw, recipes.Requirement{Engine: EngineElasticsearch})
			if err == nil && report.Result().OK {
				t.Fatal("accepted unverified response")
			}
			if strings.Contains(fmt.Sprint(err), "s3cret") || strings.Contains(fmt.Sprint(report), "s3cret") {
				t.Fatal("remote echo leaked")
			}
			if tc.name == "redirect" && calls.Load() != 1 {
				t.Fatal("redirect was followed")
			}
		})
	}
}

func TestSearchTLSAndPolicyBeforeCredentials(t *testing.T) {
	var requests []string
	var mu sync.Mutex
	_, ca, dial, calls := searchTestServer(t, EngineOpenSearch, searchNativeHandler(EngineOpenSearch, &requests, &mu))
	a := searchAccess(EngineOpenSearch)
	raw, _ := EncodeEngineAccess(a)
	if _, err := (SearchProber{}).Probe(t.Context(), dial, raw, recipes.Requirement{}); err == nil {
		t.Fatal("trusted an unknown CA")
	}
	if len(requests) != 0 {
		t.Fatal("credentials sent before TLS verification")
	}
	a.CACert = ca
	a.Host = "wrong.example"
	raw, _ = EncodeEngineAccess(a)
	wrongDial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dial(ctx, network, "search.example:9200")
	}
	if _, err := (SearchProber{}).Probe(t.Context(), wrongDial, raw, recipes.Requirement{}); err == nil {
		t.Fatal("accepted hostname mismatch")
	}
	a.Host = "search.example"
	raw, _ = EncodeEngineAccess(a)
	before := calls.Load()
	report, err := (SearchProber{}).Probe(t.Context(), dial, raw, recipes.Requirement{TLS: []string{TLSDisable}})
	if err != nil || report.Result().OK || calls.Load() != before {
		t.Fatal("disallowed TLS policy dialed")
	}
	for _, invalid := range []string{"/host/ca.pem", " \n\t ", "garbage" + ca, ca + "private key", strings.Replace(ca, "CERTIFICATE", "PRIVATE KEY", 1), strings.Repeat(ca, 9)} {
		a.CACert = invalid
		if _, err := EncodeEngineAccess(a); err == nil {
			t.Fatal("accepted non-certificate CA contract")
		}
	}
}

func TestSearchCancellationBoundsAndDialErrorsStayFixed(t *testing.T) {
	raw, _ := EncodeEngineAccess(searchAccess(EngineElasticsearch))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	var calls, closes atomic.Int32
	peerExited := make(chan struct{})
	dial := func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		left, right := net.Pipe()
		t.Cleanup(func() { _ = right.Close() })
		go func() {
			defer close(peerExited)
			defer right.Close()
			_, _ = io.Copy(io.Discard, right)
		}()
		return &searchCountingConn{left, &closes}, nil
	}
	started := time.Now()
	_, err := (SearchProber{}).Probe(ctx, dial, raw, recipes.Requirement{})
	if !errors.Is(err, errTimedOut) || time.Since(started) > time.Second {
		t.Fatal("cancellation did not bound the request")
	}
	// The HTTP transport detaches its dial context. Observe the active
	// connection and peer exit rather than waiting for that context.
	remaining := time.NewTimer(time.Second - time.Since(started))
	defer remaining.Stop()
	select {
	case <-peerExited:
	case <-remaining.C:
		t.Fatal("canceled connection did not release its peer")
	}
	if calls.Load() != 1 || closes.Load() != 1 || time.Since(started) > time.Second {
		t.Fatal("cancellation leaked a connection or retried the request")
	}
	leakDial := func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("s3cret:short/x? dial echo")
	}
	if _, err = (SearchProber{}).Probe(t.Context(), leakDial, raw, recipes.Requirement{}); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatal("dial error leaked secret")
	}
}

func FuzzSearchJSONBounds(f *testing.F) {
	f.Add([]byte(elasticRoot))
	f.Add([]byte(`{"a":{"a":1}}`))
	f.Add([]byte(`{"a":1,"a":2}`))
	f.Fuzz(func(_ *testing.T, data []byte) { var value any; _ = searchJSON(data, &value) })
}

func TestSearchOpenSearchProductIdentityAndPublicRoot(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		badPassword      bool
	}{
		{name: "elastic product", path: "/", body: elasticRoot},
		{name: "old minor", path: "/", body: strings.Replace(openSearchRoot, "3.9.0", "3.8.9", 1)},
		{name: "future minor", path: "/", body: strings.Replace(openSearchRoot, "3.9.0", "3.10.0", 1)},
		{name: "wrong principal", path: "/_plugins/_security/authinfo", body: `{"user_name":"other"}`},
		{name: "anonymous matching requested principal", path: "/_plugins/_security/authinfo", body: `{"user_name":"opendistro_security_anonymous"}`},
		{name: "bad password public root", badPassword: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests []string
			var mu sync.Mutex
			native := searchNativeHandler(EngineOpenSearch, &requests, &mu)
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.badPassword && r.URL.Path == "/" {
					_, _ = fmt.Fprint(w, openSearchRoot)
					return
				}
				if tc.path != "" && r.URL.Path == tc.path {
					_, _ = fmt.Fprint(w, tc.body)
					return
				}
				native.ServeHTTP(w, r)
			})
			_, ca, dial, _ := searchTestServer(t, EngineOpenSearch, handler)
			a := searchAccess(EngineOpenSearch)
			a.CACert = ca
			if tc.badPassword {
				a.Password = "wrong-password"
			}
			if strings.HasPrefix(tc.name, "anonymous") {
				a.Username = "opendistro_security_anonymous"
			}
			raw, _ := EncodeEngineAccess(a)
			report, err := (SearchProber{}).Probe(t.Context(), dial, raw, recipes.Requirement{})
			if err == nil && report.Result().OK {
				t.Fatal("accepted unverified identity")
			}
		})
	}
}

func TestSearchElasticPublicRootDoesNotAuthenticate(t *testing.T) {
	var requests []string
	var mu sync.Mutex
	native := searchNativeHandler(EngineElasticsearch, &requests, &mu)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			_, _ = fmt.Fprint(w, elasticRoot)
			return
		}
		native.ServeHTTP(w, r)
	})
	_, _, dial, calls := searchTestServer(t, EngineElasticsearch, handler)
	a := searchAccess(EngineElasticsearch)
	a.Password = "wrong-password"
	raw, _ := EncodeEngineAccess(a)
	report, err := (SearchProber{}).Probe(t.Context(), dial, raw, recipes.Requirement{})
	if err == nil && report.Result().OK || calls.Load() != 2 {
		t.Fatal("public root bypassed authenticated identity")
	}
}

func TestSearchRejectsOtherElasticMinorTrains(t *testing.T) {
	for _, version := range []string{"9.3.9", "9.5.0", "10.0.0"} {
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			_, _ = fmt.Fprint(w, strings.Replace(elasticRoot, "9.4.1", version, 1))
		})
		_, _, dial, calls := searchTestServer(t, EngineElasticsearch, handler)
		raw, _ := EncodeEngineAccess(searchAccess(EngineElasticsearch))
		report, err := (SearchProber{}).Probe(t.Context(), dial, raw, recipes.Requirement{})
		if err == nil && report.Result().OK || calls.Load() != 1 {
			t.Fatal("accepted unqualified minor train")
		}
	}
}

type searchCountingConn struct {
	net.Conn
	closes *atomic.Int32
}

func (c *searchCountingConn) Close() error { c.closes.Add(1); return c.Conn.Close() }

func TestSearchMalformedWireClosesWithoutRetry(t *testing.T) {
	for _, response := range []string{
		"HTTP/1.1 200 OK\r\nBad Header: bad\r\n\r\n{}",
		"HTTP/1.1 200 OK\r\nX-Huge: " + strings.Repeat("h", 20*1024) + "\r\n\r\n{}",
		"HTTP/1.1 200 OK\r\nX-Truncated: bad",
		"HTTP/1.1 200 OK\r\nContent-Length: 800\r\n\r\n{}",
		"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\nff\r\n{}",
	} {
		var calls, closes atomic.Int32
		done := make(chan struct{})
		dial := func(context.Context, string, string) (net.Conn, error) {
			calls.Add(1)
			l, r := net.Pipe()
			go func() {
				defer close(done)
				defer r.Close()
				_, _ = http.ReadRequest(bufio.NewReader(r))
				_, _ = io.WriteString(r, response)
			}()
			return &searchCountingConn{l, &closes}, nil
		}
		raw, _ := EncodeEngineAccess(searchAccess(EngineElasticsearch))
		_, err := (SearchProber{}).Probe(t.Context(), dial, raw, recipes.Requirement{})
		<-done
		if err == nil || calls.Load() != 1 || closes.Load() != 1 {
			t.Fatal("malformed response accepted, retried or leaked a connection")
		}
	}
}

func TestSearchCancellationDuringTLSAndBody(t *testing.T) {
	for _, phase := range []string{"tls", "body"} {
		t.Run(phase, func(t *testing.T) {
			a := searchAccess(EngineElasticsearch)
			if phase == "tls" {
				a.TLS = TLSVerifyFull
			}
			raw, _ := EncodeEngineAccess(a)
			var calls, closes atomic.Int32
			done := make(chan struct{})
			dial := func(context.Context, string, string) (net.Conn, error) {
				calls.Add(1)
				l, r := net.Pipe()
				t.Cleanup(func() {
					_ = l.Close()
					_ = r.Close()
				})
				go func() {
					defer close(done)
					defer r.Close()
					if phase == "body" {
						_, _ = http.ReadRequest(bufio.NewReader(r))
						_, _ = io.WriteString(r, "HTTP/1.1 200 OK\r\nContent-Length: 800\r\n\r\n{")
					}
					_, _ = io.Copy(io.Discard, r)
				}()
				return &searchCountingConn{l, &closes}, nil
			}
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, err := (SearchProber{}).Probe(ctx, dial, raw, recipes.Requirement{})
			if !errors.Is(err, errTimedOut) {
				t.Fatal("cancellation did not return the timeout error")
			}
			remaining := time.NewTimer(time.Second - time.Since(start))
			defer remaining.Stop()
			select {
			case <-done:
			case <-remaining.C:
				t.Fatal("canceled connection did not release its peer")
			}
			// TLS cancellation and HTTP handshake cleanup can both close the connection.
			if calls.Load() != 1 || closes.Load() == 0 || time.Since(start) > time.Second {
				t.Fatal("cancellation did not close the bounded request")
			}
		})
	}
}

func TestSearchParserWorkBoundsAndCaseConfusion(t *testing.T) {
	for _, body := range []string{
		strings.Repeat("[", 14) + "0" + strings.Repeat("]", 14),
		"[" + strings.Repeat("0,", 4100) + "0]",
		`{"username":"wrong","Username":"app"}`,
		`{"Version":{"number":"9.4.1"}}`,
	} {
		var out any
		if searchJSON([]byte(body), &out) == nil {
			t.Fatal("accepted ambiguous or excessive parser work")
		}
	}
}

func TestSearchIgnoresEnvironmentProxy(t *testing.T) {
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxyCalls.Add(1) }))
	defer proxy.Close()
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(key, proxy.URL)
	}
	t.Setenv("NO_PROXY", "")
	for _, engine := range []string{EngineElasticsearch, EngineOpenSearch} {
		var requests []string
		var mu sync.Mutex
		_, ca, dial, calls := searchTestServer(t, engine, searchNativeHandler(engine, &requests, &mu))
		a := searchAccess(engine)
		a.CACert = ca
		raw, _ := EncodeEngineAccess(a)
		report, err := (SearchProber{}).Probe(t.Context(), dial, raw, recipes.Requirement{})
		if err != nil || !report.Result().OK || calls.Load() != 3 || proxyCalls.Load() != 0 {
			t.Fatal("proxy bypassed guarded dialer")
		}
	}
}

func TestSearchCARejectsLeafPrivateKeyAndBounds(t *testing.T) {
	cert, ca := searchTestCA(t)
	second, secondCA := searchTestCA(t)
	a := searchAccess(EngineOpenSearch)
	leaf := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}))
	key, keyErr := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	const begin = "-----BEGIN CERTIFICATE-----\n"
	const end = "-----END CERTIFICATE-----\n"
	for _, tc := range []struct {
		name string
		ca   string
	}{
		{"incomplete prefix", begin + "not-a-certificate\n" + ca},
		{"malformed base64 before a valid certificate", begin + "@@@\n" + end + ca},
		{"repeated BEGIN", begin + ca},
		{"nested BEGIN", begin + "QUJD\n" + ca + end},
		{"non-certificate text inside a block", begin + "QUJD\n# non-certificate text\n" + end + ca},
		{"non-certificate text between blocks", ca + "non-certificate text\n" + secondCA},
		{"invalid BEGIN line", strings.TrimSuffix(begin, "\n") + " extra\n" + ca},
		{"leaf certificate", leaf},
		{"private key", ca + string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))},
		{"size limit", ca + strings.Repeat(" ", 64*1024-len(ca)+1)},
		{"count limit", strings.Repeat(ca, 9)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			access := a
			access.CACert = tc.ca
			if _, err := EncodeEngineAccess(access); !errors.Is(err, ErrInvalid) {
				t.Error("encoder accepted invalid CA material")
			}
			stored, err := json.Marshal(access)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseEngineAccess(string(stored)); !errors.Is(err, ErrInvalid) {
				t.Error("parser accepted invalid stored CA material")
			}
		})
	}

	bundle := " \n" + ca + "\n\t\n" + strings.ReplaceAll(secondCA, "\n", "\r\n")
	a.CACert = bundle
	raw, err := EncodeEngineAccess(a)
	if err != nil {
		t.Fatal(err)
	}
	value, err := EngineAccessField(raw, "ca_cert")
	if err != nil || value != bundle {
		t.Fatal("inline CA bundle changed during field resolution")
	}
	roots, err := engineRootCAs(bundle)
	if err != nil {
		t.Fatal(err)
	}
	want := x509.NewCertPool()
	for _, der := range [][]byte{cert.Certificate[1], second.Certificate[1]} {
		root, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		want.AddCert(root)
	}
	if !roots.Equal(want) {
		t.Fatal("CA bundle did not trust both certificates")
	}

	for _, boundary := range []string{strings.Repeat(ca, 8), ca + strings.Repeat(" ", 64*1024-len(ca))} {
		a.CACert = boundary
		if _, err := EncodeEngineAccess(a); err != nil {
			t.Fatal("valid CA material at a limit was refused")
		}
	}
}

func TestSearchVerifiedElasticHTTPS(t *testing.T) {
	var requests []string
	var mu sync.Mutex
	_, ca, dial, calls := searchTestServer(t, EngineOpenSearch, searchNativeHandler(EngineElasticsearch, &requests, &mu))
	a := searchAccess(EngineElasticsearch)
	a.TLS, a.CACert = TLSVerifyFull, ca
	raw, _ := EncodeEngineAccess(a)
	report, err := (SearchProber{}).Probe(t.Context(), dial, raw, recipes.Requirement{TLS: []string{TLSVerifyFull}})
	if err != nil || !report.Result().OK || calls.Load() != 3 {
		t.Fatal("verified Elasticsearch HTTPS failed")
	}
}

func TestSearchSystemTrustWithoutInlineCA(t *testing.T) {
	const child = "LOOMPLANE_SEARCH_TRUST_CHILD"
	if os.Getenv(child) == "1" {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM([]byte(os.Getenv("LOOMPLANE_SEARCH_TRUST_CA"))) {
			t.Fatal("child trust missing")
		}
		x509.SetFallbackRoots(roots)
		a := searchAccess(EngineOpenSearch)
		raw, _ := EncodeEngineAccess(a)
		dial := func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" || address != "search.example:9200" {
				return nil, errors.New("unexpected authority")
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp", os.Getenv("LOOMPLANE_SEARCH_TRUST_ADDRESS"))
		}
		report, err := (SearchProber{}).Probe(t.Context(), dial, raw, recipes.Requirement{})
		if err != nil || !report.Result().OK {
			t.Fatal("system trust without inline CA failed")
		}
		return
	}
	var requests []string
	var mu sync.Mutex
	server, ca, _, _ := searchTestServer(t, EngineOpenSearch, searchNativeHandler(EngineOpenSearch, &requests, &mu))
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSearchSystemTrustWithoutInlineCA$")
	cmd.Env = append(os.Environ(), child+"=1", "GODEBUG=x509usefallbackroots=1", "LOOMPLANE_SEARCH_TRUST_CA="+ca, "LOOMPLANE_SEARCH_TRUST_ADDRESS="+server.Listener.Addr().String())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated system-trust check failed: %s", output)
	}
}

func TestSearchExplicitCancellationClosesConnection(t *testing.T) {
	raw, _ := EncodeEngineAccess(searchAccess(EngineElasticsearch))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	connected := make(chan struct{})
	done := make(chan struct{})
	var closes atomic.Int32
	dial := func(context.Context, string, string) (net.Conn, error) {
		l, r := net.Pipe()
		go func() { defer close(done); defer r.Close(); close(connected); _, _ = io.Copy(io.Discard, r) }()
		return &searchCountingConn{l, &closes}, nil
	}
	go func() { <-connected; cancel() }()
	start := time.Now()
	_, err := (SearchProber{}).Probe(ctx, dial, raw, recipes.Requirement{})
	<-done
	if err == nil || time.Since(start) > time.Second || closes.Load() != 1 || strings.Contains(err.Error(), "s3cret") {
		t.Fatal("explicit cancellation did not close request")
	}
}
