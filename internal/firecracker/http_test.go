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
