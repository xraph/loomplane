package connections

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/keyless"
)

const (
	workloadProvider = "projects/123456789012/locations/global/workloadIdentityPools/loomplane/providers/loomplane"
	keylessAccount   = "loomplane@shop-prod.iam.gserviceaccount.com"
)

// fakeGoogleProject answers for any project: a service account key's
// grant is ya29.key, STS trades a minted token for sts-access, IAM
// Credentials trades that for ya29.keyless, and Resource Manager grants
// either access token every permission. STS is down while down is set,
// and answers as for a deleted provider while refuse is.
type fakeGoogleProject struct {
	mu       sync.Mutex
	tokens   []string
	asked    []string
	audience []string
	down     bool
	refuse   bool
}

func (f *fakeGoogleProject) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/token":
		_, _ = w.Write([]byte(`{"access_token":"ya29.key","expires_in":3599,"token_type":"Bearer"}`))
	case r.URL.Path == "/v1/token":
		_ = r.ParseForm()
		f.tokens = append(f.tokens, r.PostForm.Get("subject_token"))
		f.audience = append(f.audience, r.PostForm.Get("audience"))
		switch {
		case f.down:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"temporarily_unavailable"}`))
		case f.refuse:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_target","error_description":"The target service indicated by the \"audience\" parameters is invalid. This might either be because the pool or provider is disabled or deleted or because it doesn't exist."}`))
		default:
			_, _ = w.Write([]byte(`{"access_token":"sts-access","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":3600}`))
		}
	case r.URL.Path == "/v1/projects/-/serviceAccounts/"+keylessAccount+":generateAccessToken":
		if r.Header.Get("Authorization") != "Bearer sts-access" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprintf(w, `{"accessToken":"ya29.keyless","expireTime":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	case strings.HasPrefix(r.URL.Path, "/v3/projects/") && strings.HasSuffix(r.URL.Path, ":testIamPermissions"):
		bearer := r.Header.Get("Authorization")
		f.asked = append(f.asked, strings.TrimPrefix(bearer, "Bearer "))
		if bearer != "Bearer ya29.key" && bearer != "Bearer ya29.keyless" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"permissions": gcp.Permissions})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// googleKeyFor is a service account key for shop-prod whose grants come
// from tokenURI.
func googleKeyFor(t *testing.T, tokenURI string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "shop-prod", "client_email": "deployer@shop-prod.iam.gserviceaccount.com",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs})), "token_uri": tokenURI,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(cred)
}

func newGoogleKeylessRig(t *testing.T) (*keylessRig, *fakeGoogleProject, string) {
	t.Helper()
	fake := &fakeGoogleProject{}
	srv := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(srv.Close)
	client := gcp.New(srv.URL).WithMinter(ticketMinter{})
	r := newKeylessRig(t)
	r.s.checkers[GCP] = GCPChecker{Client: client}
	r.s.KeylessClouds[GCP] = client
	return r, fake, srv.URL
}

// A service account key and the workload provider's trust reach the same
// project, so a connection moves to keyless and keeps its ID, and every
// setting it had is in the handout a deployment gets.
func TestGoogleKeylessLandsOnTheSameIdentityAsAKey(t *testing.T) {
	r, fake, url := newGoogleKeylessRig(t)
	settings := map[string]string{
		gcp.BillingExportSetting:     "shop-prod.billing.gcp_billing_export_v1_0A1B2C",
		"cloud_sql_recovery_network": "projects/shop-prod/global/networks/recovery",
	}
	c, err := r.s.Create(t.Context(), starter, CreateInput{Cloud: GCP, Name: "Shop", Credential: googleKeyFor(t, url+"/token"), Settings: settings})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.Account != "shop-prod" || c.Details["service_account"] != "deployer@shop-prod.iam.gserviceaccount.com" {
		t.Fatalf("key connection: %+v", c)
	}

	moving := maps.Clone(settings)
	moving["project_id"] = "shop-prod"
	moving["workload_provider"] = workloadProvider
	moving["service_account"] = keylessAccount
	got, err := r.s.ReplaceKeyless(t.Context(), starter, c.ID, moving)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got.ID != c.ID || got.Method != MethodKeyless || got.Status != StatusReady || got.Account != c.Account || len(got.Missing) != 0 ||
		AccountIdentity(GCP, got.Account, got.Details) != AccountIdentity(GCP, c.Account, c.Details) {
		t.Fatalf("keyless connection: %+v", got)
	}
	if !maps.Equal(got.Details, moving) {
		t.Fatalf("details: %v", got.Details)
	}
	subject := keyless.Subject("org", c.ID)
	audience := "//iam.googleapis.com/" + workloadProvider
	if len(fake.tokens) != 1 || fake.tokens[0] != "minted-for "+subject || fake.audience[0] != audience {
		t.Fatalf("exchanged %d tokens, the minted one for the provider: %v", len(fake.tokens), len(fake.tokens) == 1 && fake.tokens[0] == "minted-for "+subject && fake.audience[0] == audience)
	}
	if len(fake.asked) != 2 || fake.asked[0] != "ya29.key" || fake.asked[1] != "ya29.keyless" {
		t.Fatal("Resource Manager wasn't asked as the key, then as the impersonated account")
	}

	// A deployment's handout carries every setting, and parses as a key.
	_, cred, err := r.s.Credential(t.Context(), "org", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	h, _, _ := keyless.Parse(cred)
	if h.Audience != audience || !maps.Equal(h.Settings, moving) {
		t.Fatalf("handout: audience %q, settings %v", h.Audience, h.Settings)
	}
	if k, err := gcp.ParseKey(cred); err != nil || !k.Keyless || k.ProjectID != "shop-prod" || k.ClientEmail != keylessAccount || k.WorkloadProvider != workloadProvider {
		t.Fatalf("handout key: %v", err)
	}
	if again, err := r.s.Recheck(t.Context(), starter, c.ID); err != nil || again.Status != StatusReady || !maps.Equal(again.Details, moving) {
		t.Fatalf("recheck: %+v %v", again, err)
	}
	if setup, err := r.s.KeylessSetupFor(t.Context(), starter, c.ID); err != nil || setup.Audience != audience {
		t.Fatalf("setup: %+v %v", setup, err)
	}

	// Another project isn't the same identity.
	elsewhere := maps.Clone(moving)
	elsewhere["project_id"] = "shop-staging"
	if _, err := r.s.ReplaceKeyless(t.Context(), starter, c.ID, elsewhere); !errors.Is(err, ErrInvalid) {
		t.Fatalf("other project: %v", err)
	}
	if !(GCPChecker{}).IsGrant(cred) {
		t.Fatal("a keyless handout isn't a grant")
	}
}

// Google refusing the trust, here because its provider was deleted, is a
// refusal, which fails the connection, and Google being down is not.
func TestGoogleKeylessRefusalIsRejectedAndAnOutageIsNot(t *testing.T) {
	r, fake, _ := newGoogleKeylessRig(t)
	settings := map[string]string{"project_id": "shop-prod", "workload_provider": workloadProvider, "service_account": keylessAccount}
	handout, err := r.s.keylessHandout(t.Context(), &Connection{ID: "conn_1", OrgID: "org", Cloud: GCP, Details: settings})
	if err != nil {
		t.Fatal(err)
	}
	checker := r.s.checkers[GCP]
	fake.down = true
	_, err = checker.Check(t.Context(), handout, settings)
	if err == nil || rejected(err) {
		t.Fatalf("outage: rejected %v", err != nil && rejected(err))
	}
	fake.down, fake.refuse = false, true
	_, err = checker.Check(t.Context(), handout, settings)
	if err == nil || !rejected(err) || refusedSignIn(err) {
		t.Fatalf("refusal: rejected %v", err != nil && rejected(err))
	}
	if strings.Contains(err.Error(), "minted-for") || strings.Contains(err.Error(), "ticket-for") {
		t.Fatal("the refusal shows a secret")
	}
	// It names the keyless trust, not a key or a sign-in.
	if msg := err.Error(); !strings.HasPrefix(msg, "Google Cloud refused Loomplane's keyless token for this workload identity provider") ||
		strings.Contains(msg, "key or sign-in") {
		t.Fatalf("refusal says %q", msg)
	}
}
