package catalogconnect

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/recipes"
)

func bucketFixture(t *testing.T, handler http.HandlerFunc) (BucketProber, DialFunc, *[]string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(3), DNSNames: []string{"s3.us-east-1.amazonaws.com", "storage.googleapis.com", "oauth2.googleapis.com", "api.cloudflare.com", "vercel.com", "edge-config.vercel.com", "global-config.vercel.com", "application.upstash.io", strings.Repeat("a", 32) + ".eu.r2.cloudflarestorage.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	var mu sync.Mutex
	dials := []string{}
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		mu.Lock()
		dials = append(dials, address)
		mu.Unlock()
		var d net.Dialer
		return d.DialContext(ctx, network, srv.Listener.Addr().String())
	}
	return BucketProber{RootCAs: roots}, dial, &dials
}

func bucketGoogleKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{"type": "service_account", "project_id": "storage-project", "private_key_id": "application-key-id", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "client_email": "workload@storage-project.iam.gserviceaccount.com", "token_uri": "https://oauth2.googleapis.com/token"})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestBucketProberS3UsesExplicitKeysAndOwnerWithoutWrites(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "ADMIN-DO-NOT-USE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ADMIN-DO-NOT-USE")
	t.Setenv("HTTPS_PROXY", "http://administrator-proxy.example:3128")
	methods := []string{}
	prober, dial, dials := bucketFixture(t, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Host != "s3.us-east-1.amazonaws.com" || r.URL.Path != "/application-assets" || r.Header.Get("X-Amz-Expected-Bucket-Owner") != "123456789012" || !strings.Contains(r.Header.Get("Authorization"), "Credential=AKIAEXAMPLEAPPLICATION/") || strings.Contains(r.Header.Get("Authorization"), "ADMIN") {
			t.Errorf("unexpected authenticated request %s %s %s", r.Method, r.Host, r.URL.Path)
		}
		w.Header().Set("X-Amz-Bucket-Region", "us-east-1")
		if r.Method == "GET" {
			if r.URL.Query().Get("max-keys") != "1" || r.URL.Query().Get("list-type") != "2" {
				t.Error("unbounded list")
			}
			fmt.Fprint(w, `<ListBucketResult><Name>application-assets</Name><KeyCount>0</KeyCount></ListBucketResult>`)
		}
	})
	got, err := prober.Probe(t.Context(), dial, testBucketJSON, recipes.Requirement{Engine: EngineS3})
	if err != nil || got == nil || len(got.Problems) != 0 || !slices.Equal(methods, []string{"HEAD", "GET"}) || len(*dials) == 0 || slices.ContainsFunc(*dials, func(address string) bool { return address != "s3.us-east-1.amazonaws.com:443" }) {
		t.Fatalf("report=%+v err=%v methods=%v dials=%v", got, err, methods, *dials)
	}
}
func TestBucketProberS3RefusesRedirectOwnerAndRegionFailures(t *testing.T) {
	for _, scenario := range []string{"redirect", "owner", "region", "malformed", "denied-list"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			prober, dial, _ := bucketFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("X-Amz-Bucket-Region", "us-east-1")
				switch scenario {
				case "redirect":
					w.Header().Set("Location", "https://evil.example/steal")
					w.WriteHeader(307)
				case "owner":
					w.WriteHeader(403)
				case "region":
					w.Header().Set("X-Amz-Bucket-Region", "us-west-2")
				case "malformed":
					if r.Method == "GET" {
						fmt.Fprint(w, "not xml")
					}
				case "denied-list":
					if r.Method == "GET" {
						w.WriteHeader(403)
						fmt.Fprint(w, "application-secret-not-admin")
					}
				}
			})
			got, err := prober.Probe(t.Context(), dial, testBucketJSON, recipes.Requirement{Engine: EngineS3})
			if err == nil && (got == nil || len(got.Problems) == 0) {
				t.Fatalf("accepted %s: %+v", scenario, got)
			}
			if strings.Contains(fmt.Sprint(got, err), "application-secret") {
				t.Fatal("secret leak")
			}
			if scenario == "redirect" && calls != 1 {
				t.Fatalf("followed redirect (%d calls)", calls)
			}
		})
	}
}
func TestBucketProberGoogleUsesFixedTokenAndBoundedList(t *testing.T) {
	access := BucketAccess{Provider: "gcp", Bucket: "application-assets", Region: "us-central1", ServiceAccountJSON: bucketGoogleKey(t)}
	raw, err := EncodeBucketAccess(access)
	if err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	prober, dial, dials := bucketFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.Host+r.URL.Path)
		switch r.Host {
		case "oauth2.googleapis.com":
			if r.Method != "POST" || r.URL.Path != "/token" {
				t.Error("unexpected token request")
			}
			if formErr := r.ParseForm(); formErr != nil {
				t.Error(formErr)
			}
			parts := strings.Split(r.Form.Get("assertion"), ".")
			if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || len(parts) != 3 {
				t.Error("missing signed assertion")
			} else {
				payload, decodeErr := base64.RawURLEncoding.DecodeString(parts[1])
				var claims map[string]any
				if decodeErr != nil || json.Unmarshal(payload, &claims) != nil || claims["iss"] != "workload@storage-project.iam.gserviceaccount.com" || claims["aud"] != "https://oauth2.googleapis.com/token" || claims["scope"] != "https://www.googleapis.com/auth/devstorage.read_only" {
					t.Error("token did not use the reviewed workload key, scope and audience")
				}
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"application-bearer-token","token_type":"Bearer","expires_in":3600}`)
		case "storage.googleapis.com":
			if r.Method != "GET" || r.URL.Path != "/storage/v1/b/application-assets/o" || r.URL.Query().Get("maxResults") != "1" || r.Header.Get("Authorization") != "Bearer application-bearer-token" {
				t.Errorf("unexpected storage request %s", r.URL)
			}
			fmt.Fprint(w, `{"kind":"storage#objects"}`)
		default:
			t.Errorf("credential sent to %s", r.Host)
		}
	})
	got, err := prober.Probe(t.Context(), dial, raw, recipes.Requirement{Engine: EngineGCS})
	if err != nil || got == nil || len(got.Problems) != 0 || len(calls) != 2 || !slices.Contains(*dials, "oauth2.googleapis.com:443") || !slices.Contains(*dials, "storage.googleapis.com:443") {
		t.Fatalf("report=%+v err=%v calls=%v dials=%v", got, err, calls, *dials)
	}
}
func TestBucketProberRejectsInvalidGoogleKeyAndGuardFailure(t *testing.T) {
	access := BucketAccess{Provider: "gcp", Bucket: "application-assets", Region: "us", ServiceAccountJSON: bucketGoogleKey(t)}
	for _, key := range []string{strings.Replace(access.ServiceAccountJSON, "oauth2.googleapis.com", "evil.example", 1), `{"type":"external_account","credential_source":{"file":"/secret"}}`} {
		access.ServiceAccountJSON = key
		raw, _ := json.Marshal(access)
		calls := 0
		_, err := (BucketProber{}).Probe(t.Context(), func(context.Context, string, string) (net.Conn, error) { calls++; return nil, errors.New("secret") }, string(raw), recipes.Requirement{Engine: EngineGCS})
		if !errors.Is(err, ErrInvalid) || calls != 0 {
			t.Fatalf("invalid key made network call: %v (%d)", err, calls)
		}
	}
	_, err := (BucketProber{}).Probe(t.Context(), func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("application-secret-not-admin")
	}, testBucketJSON, recipes.Requirement{Engine: EngineS3})
	if err == nil || strings.Contains(err.Error(), "application-secret") {
		t.Fatalf("guard error leaked credentials: %v", err)
	}
}
func TestBucketContractOutputsAndRedaction(t *testing.T) {
	a, err := ParseBucketAccess(testBucketJSON)
	if err != nil {
		t.Fatal(err)
	}
	out, err := BucketOutputs(testBucketJSON)
	if err != nil {
		t.Fatal(err)
	}
	if out["STORAGE_URL"] != "s3://application-assets?region=us-east-1" || out["AWS_SECRET_ACCESS_KEY"] != a.SecretAccessKey || len(out) != len(BucketEnvironment(EngineS3)) {
		t.Fatalf("bad outputs: keys=%v", BucketEnvironment(EngineS3))
	}
	if _, ok := out["AWS_SESSION_TOKEN"]; !ok {
		t.Fatal("missing stable empty session output")
	}
	message := redact("refused "+a.SecretAccessKey+" "+a.AccessKeyID, testBucketJSON)
	if strings.Contains(message, a.SecretAccessKey) || strings.Contains(message, a.AccessKeyID) {
		t.Fatal("bucket credentials not redacted")
	}
	if _, err := url.Parse(out["STORAGE_URL"]); err != nil {
		t.Fatal(err)
	}
}

func TestBucketProberS3ListDoesNotRequireHeadOnlyRegionHeader(t *testing.T) {
	prober, dial, _ := bucketFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.Header().Set("X-Amz-Bucket-Region", "us-east-1")
		} else {
			fmt.Fprint(w, `<ListBucketResult><Name>application-assets</Name></ListBucketResult>`)
		}
	})
	got, err := prober.Probe(t.Context(), dial, testBucketJSON, recipes.Requirement{Engine: EngineS3})
	if err != nil || got == nil || len(got.Problems) > 0 {
		t.Fatalf("successful standard listing refused: %+v %v", got, err)
	}
}
func TestBucketProberGoogleRefusesTokenRedirectAndStorageDenial(t *testing.T) {
	raw, err := EncodeBucketAccess(BucketAccess{Provider: "gcp", Bucket: "application-assets", Region: "us", ServiceAccountJSON: bucketGoogleKey(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"token-redirect", "token-denied", "storage-redirect", "storage-denied", "malformed-list"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			prober, dial, _ := bucketFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Host == "oauth2.googleapis.com" {
					switch scenario {
					case "token-redirect":
						w.Header().Set("Location", "https://evil.example/token")
						w.WriteHeader(307)
					case "token-denied":
						w.WriteHeader(403)
						fmt.Fprint(w, "application-bearer-token")
					default:
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"access_token":"application-bearer-token","token_type":"Bearer","expires_in":3600}`)
					}
					return
				}
				switch scenario {
				case "storage-redirect":
					w.Header().Set("Location", "https://evil.example/list")
					w.WriteHeader(307)
				case "storage-denied":
					w.WriteHeader(403)
					fmt.Fprint(w, "application-bearer-token")
				case "malformed-list":
					fmt.Fprint(w, "application-bearer-token")
				default:
					t.Fatalf("token failure allowed storage call")
				}
			})
			got, err := prober.Probe(t.Context(), dial, raw, recipes.Requirement{Engine: EngineGCS})
			if err == nil && (got == nil || len(got.Problems) == 0) {
				t.Fatalf("accepted %s", scenario)
			}
			if strings.Contains(fmt.Sprint(got, err), "application-bearer-token") {
				t.Fatal("token leaked")
			}
			expected := 2
			if strings.HasPrefix(scenario, "token-") {
				expected = 1
			}
			if calls != expected {
				t.Fatalf("unexpected redirect or retry: %d", calls)
			}
		})
	}
}
