package firecracker

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPRejectsForeignScopeAndUnknownFields(t *testing.T) {
	h, d, q := testHost(t)
	cert := &x509.Certificate{Raw: []byte("client")}
	sum := sha256.Sum256(cert.Raw)
	api := Handler{Host: h, Peers: []Peer{{Fingerprint: hex.EncodeToString(sum[:]), Org: q.Scope.Org, Connection: q.Scope.Connection}}}
	for _, mode := range []string{"anonymous", "foreign", "unknown", "valid"} {
		t.Run(mode, func(t *testing.T) {
			input := q
			if mode == "foreign" {
				input.Scope.Org = "foreign"
			}
			body, _ := json.Marshal(input)
			if mode == "unknown" {
				body = append(body[:len(body)-1], []byte(`,"AllowUnsafe":true}`)...)
			}
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/apply", bytes.NewReader(body))
			if mode != "anonymous" {
				r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert}}}
			}
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			if mode == "valid" && w.Code != http.StatusOK {
				t.Fatalf("valid: %d %s", w.Code, w.Body.String())
			}
			if mode != "valid" && w.Code == http.StatusOK {
				t.Fatal("unsafe accepted")
			}
		})
	}
	if d.starts != 1 {
		t.Fatal("unsafe requests launched a VM")
	}
}
func TestDecodeRejectsOversizedJSON(t *testing.T) {
	var out map[string]string
	if err := Decode(bytes.NewBufferString(`{"x":"12345678901234567890"}`), &out, 8); err == nil {
		t.Fatal("oversized accepted")
	}
}

func TestEnrollmentCertificateCannotRunWorkloads(t *testing.T) {
	h, d, q := testHost(t)
	cert := &x509.Certificate{Raw: []byte("enrollment")}
	sum := sha256.Sum256(cert.Raw)
	api := Handler{Host: h, Peers: []Peer{{Fingerprint: hex.EncodeToString(sum[:]), Org: q.Scope.Org}}}
	for _, path := range []string{"/v1/capabilities", "/v1/apply"} {
		raw, err := json.Marshal(q)
		if err != nil {
			t.Fatal(err)
		}
		method := http.MethodPost
		if path == "/v1/capabilities" {
			method = http.MethodGet
		}
		req := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewReader(raw))
		req.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert}}}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, req)
		if path == "/v1/capabilities" && response.Code != http.StatusOK {
			t.Fatal(response.Code)
		}
		if path == "/v1/apply" && response.Code != http.StatusForbidden {
			t.Fatal(response.Code)
		}
	}
	if d.starts != 0 {
		t.Fatal("enrollment started VM")
	}
}

func platformRequest(t *testing.T, api Handler, cert *x509.Certificate, path string, q Request) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, bytes.NewReader(body))
	if cert != nil {
		r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert}}}
	}
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	return w
}

func TestPlatformPeerAcceptsAnyOrgOnPlatformConnection(t *testing.T) {
	h, d, q := testHost(t)
	cert := &x509.Certificate{Raw: []byte("platform")}
	sum := sha256.Sum256(cert.Raw)
	api := Handler{Host: h, Platform: hex.EncodeToString(sum[:])}
	q.Scope.Connection = PlatformConnection
	for _, org := range []string{"org-a", "org-b"} {
		in := q
		in.Scope.Org = org
		if w := platformRequest(t, api, cert, "/v1/apply", in); w.Code != http.StatusOK {
			t.Fatal(org, w.Code, w.Body.String())
		}
	}
	records, err := h.Store.List(t.Context())
	if err != nil || d.starts != 2 || len(records) != 2 {
		t.Fatalf("starts %d records %d %v", d.starts, len(records), err)
	}
	in := q
	in.Scope.Connection = "conn"
	if w := platformRequest(t, api, cert, "/v1/apply", in); w.Code != http.StatusForbidden {
		t.Fatal("platform peer used a non-platform connection", w.Code)
	}
	other := &x509.Certificate{Raw: []byte("other")}
	if w := platformRequest(t, api, other, "/v1/apply", q); w.Code != http.StatusForbidden {
		t.Fatal("unknown certificate accepted", w.Code)
	}
	if d.starts != 2 {
		t.Fatal("rejected requests launched a VM")
	}
}

func TestPlatformPeerCannotTouchAnotherOrgsRecord(t *testing.T) {
	h, d, q := testHost(t)
	cert := &x509.Certificate{Raw: []byte("platform")}
	sum := sha256.Sum256(cert.Raw)
	api := Handler{Host: h, Platform: hex.EncodeToString(sum[:])}
	q.Scope.Connection = PlatformConnection
	q.Scope.Org = "org-a"
	if w := platformRequest(t, api, cert, "/v1/apply", q); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	stop := q
	stop.Scope.Org = "org-b"
	stop.Authority.Operation = "stop-1"
	if w := platformRequest(t, api, cert, "/v1/stop", stop); w.Code != http.StatusNotFound {
		t.Fatal("cross-org stop", w.Code, w.Body.String())
	}
	if len(d.live) != 1 {
		t.Fatal("org-a VM was stopped")
	}
}

func TestUsageIsScopedToPeerOrg(t *testing.T) {
	h, _, q := testHost(t)
	orgCert := &x509.Certificate{Raw: []byte("org")}
	orgSum := sha256.Sum256(orgCert.Raw)
	platformCert := &x509.Certificate{Raw: []byte("platform")}
	platformSum := sha256.Sum256(platformCert.Raw)
	api := Handler{Host: h, Platform: hex.EncodeToString(platformSum[:]), Peers: []Peer{{Fingerprint: hex.EncodeToString(orgSum[:]), Org: q.Scope.Org, Connection: q.Scope.Connection}}}
	if w := platformRequest(t, api, orgCert, "/v1/apply", q); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	other := q
	other.Scope.Org = "other"
	other.Authority.Operation = "deploy-other"
	if _, err := h.Apply(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	get := func(cert *x509.Certificate) []UsageSample {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/usage", nil)
		r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert}}}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		var out []UsageSample
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := get(orgCert); len(got) != 1 || got[0].Scope.Org != q.Scope.Org {
		t.Fatalf("per-org peer saw %+v", got)
	}
	if got := get(platformCert); len(got) != 2 {
		t.Fatalf("platform peer saw %+v", got)
	}
}
