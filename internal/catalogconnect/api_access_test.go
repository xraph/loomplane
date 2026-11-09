package catalogconnect

import (
	"strings"
	"testing"
)

func TestAPIAccessPinsProviderResourceAndKeepsTokenPrivate(t *testing.T) {
	access := APIAccess{Provider: "cloudflare", Kind: "cloudflare-d1", AccountID: strings.Repeat("a", 32), ResourceID: "db-id", Token: "application-token"}
	raw, err := EncodeAPIAccess(access)
	if err != nil {
		t.Fatal(err)
	}
	out, err := APIOutputs(raw)
	if err != nil || out["CLOUDFLARE_D1_DATABASE_ID"] != "db-id" || out["CLOUDFLARE_API_TOKEN"] != "application-token" {
		t.Fatal("application access outputs missing")
	}
	e := &Endpoint{Engine: "cloudflare-d1"}
	if err = parseDSN(e, raw); err != nil || e.Host != "api.cloudflare.com" || e.Database != "db-id" || e.TLS != TLSVerifyFull || strings.Contains(e.Host, access.Token) {
		t.Fatal("endpoint lost resource pin or exposed access")
	}
}

func TestAPIAccessNativeBindingsNeverDeliverAccountTokens(t *testing.T) {
	a := APIAccess{Provider: "cloudflare", Kind: "cloudflare-d1", AccountID: strings.Repeat("a", 32), ResourceID: "db-id", Native: true}
	raw, err := EncodeAPIAccess(a)
	if err != nil {
		t.Fatal(err)
	}
	out, err := APIOutputs(raw)
	if err != nil || out["CLOUDFLARE_BINDING"] == "" || out["CLOUDFLARE_API_TOKEN"] != "" {
		t.Fatal("native resource access was not a binding")
	}
	a.Token = "account-admin-token"
	if _, err = EncodeAPIAccess(a); err == nil {
		t.Fatal("native binding accepted account-token delivery")
	}
}

func TestAPIAccessRejectsCredentialContractAmbiguity(t *testing.T) {
	for _, raw := range []string{
		`{"provider":"cloudflare","kind":"cloudflare-d1","account_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","resource_id":"db","token":"secret","host":"attacker.example"}`,
		`{"provider":"cloudflare","kind":"cloudflare-d1","account_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","resource_id":"db","token":"one","token":"two"}`,
		`{"provider":"cloudflare","kind":"cloudflare-hyperdrive","account_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","resource_id":"db","token":"secret"}`,
		`{"provider":"vercel","kind":"vercel-blob","account_id":"team_a","resource_id":"store_one","token":"vercel_blob_rw_two_secret"}`,
	} {
		if _, err := ParseAPIAccess(raw); err == nil {
			t.Fatal("ambiguous or mismatched resource accepted")
		}
	}
}

func TestR2EndpointPinsAccountJurisdictionAndOmitsAWSOwner(t *testing.T) {
	a := BucketAccess{Provider: "cloudflare", AccountID: strings.Repeat("a", 32), Jurisdiction: "eu", Bucket: "assets-production", Region: "auto", AccessKeyID: strings.Repeat("b", 32), SecretAccessKey: strings.Repeat("c", 64)}
	raw, err := EncodeBucketAccess(a)
	if err != nil {
		t.Fatal(err)
	}
	e := &Endpoint{Engine: EngineR2}
	if err = parseDSN(e, raw); err != nil || e.Host != strings.Repeat("a", 32)+".eu.r2.cloudflarestorage.com" || e.TLS != TLSVerifyFull {
		t.Fatal("R2 endpoint was not pinned")
	}
	out, err := BucketOutputs(raw)
	if err != nil || out["AWS_S3_EXPECTED_BUCKET_OWNER"] != "" || out["S3_ENDPOINT"] != "https://"+e.Host {
		t.Fatal("R2 outputs not scoped to its account")
	}
	a.ExpectedOwner = "123456789012"
	if _, err = EncodeBucketAccess(a); err == nil {
		t.Fatal("R2 accepted AWS owner proof")
	}
}

func TestAPIAccessAlsoSupportsWorkerBindingWithoutTokenDelivery(t *testing.T) {
	raw, err := EncodeAPIAccess(APIAccess{Provider: "cloudflare", Kind: "cloudflare-queue", AccountID: strings.Repeat("a", 32), ResourceID: "queue-id", Name: "events", Token: "application-token"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := APIOutputs(raw)
	if err != nil || out["CLOUDFLARE_BINDING"] == "" || out["CLOUDFLARE_API_TOKEN"] != "application-token" {
		t.Fatal("shared access cannot select a native binding")
	}
	native, _ := EncodeAPIAccess(APIAccess{Provider: "cloudflare", Kind: "cloudflare-queue", AccountID: strings.Repeat("a", 32), ResourceID: "queue-id", Name: "events", Native: true})
	endpoint := &Endpoint{Engine: "cloudflare-queue"}
	if err = parseDSN(endpoint, native); err != nil || endpoint.Username != "native-binding" {
		t.Fatal("native access mode not persisted")
	}
}

func TestAPIAccessAllowsDisplayNamesButRequiresNativeBindingIdentity(t *testing.T) {
	if _, err := EncodeAPIAccess(APIAccess{Provider: "vercel", Kind: "vercel-global-config", AccountID: "team_a", ResourceID: "ecfg_a", Token: "token", Name: "Application settings"}); err != nil {
		t.Fatal("display name was treated as an API path")
	}
	for _, kind := range []string{"cloudflare-queue", "cloudflare-vectorize", "cloudflare-r2"} {
		a := APIAccess{Provider: "cloudflare", Kind: kind, AccountID: strings.Repeat("a", 32), ResourceID: "native-id", Native: true}
		if kind == "cloudflare-r2" {
			a.ResourceID = "r2/eu/assets"
			a.Jurisdiction = "eu"
		}
		if _, err := EncodeAPIAccess(a); err == nil {
			t.Fatal("native binding omitted its provider name")
		}
	}
}

func TestAPIEndpointBridgeKeepsOnlySourceAndOutputNamesPublic(t *testing.T) {
	raw, err := EncodeAPIAccess(APIAccess{Provider: "vercel", Kind: "vercel-integration", AccountID: "team_a", ResourceID: "store_a", ProjectID: "prj_application", Environment: "production", Values: map[string]string{"Z_TOKEN": "private-token", "DATABASE_URL": "private-url"}})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := &Endpoint{Engine: "vercel-integration"}
	if err = parseDSN(endpoint, raw); err != nil {
		t.Fatal(err)
	}
	if APIEndpointBridge(endpoint) != "prj_application" || strings.Join(APIEndpointOutputs(endpoint), ",") != "DATABASE_URL,Z_TOKEN" || strings.Contains(endpoint.Username, "private") {
		t.Fatal("bridge metadata lost source identity or exposed values")
	}
}

func TestResolveAPIAccessRejectsSubstitutedPublicBridgeMetadata(t *testing.T) {
	raw, err := EncodeAPIAccess(APIAccess{Provider: "vercel", Kind: "vercel-integration", AccountID: "team_a", ResourceID: "store_a", ProjectID: "prj_original", Environment: "production", Values: map[string]string{"DATABASE_URL": "private-value"}})
	if err != nil {
		t.Fatal(err)
	}
	store := newMemStore()
	service := NewService(store, memSecrets{}, roleAuthz{})
	endpoint, err := service.Create(t.Context(), admin, CreateInput{Name: "integration", Engine: "vercel-integration", DSN: raw})
	if err != nil {
		t.Fatal(err)
	}
	row := store.rows[endpoint.ID]
	row.Username = "bridge:prj_substitute;environment:production;outputs:DATABASE_URL"
	store.rows[endpoint.ID] = row
	if _, dsn, err := service.Resolve(t.Context(), admin.OrgID, endpoint.ID); err == nil || dsn != "" {
		t.Fatal("public bridge substitution released private application access")
	}
}
