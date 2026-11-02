package catalogconnect

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const testBucketJSON = `{"provider":"aws","bucket":"application-assets","region":"us-east-1","expected_owner":"123456789012","access_key_id":"AKIAEXAMPLEAPPLICATION","secret_access_key":"application-secret-not-admin"}`

func TestBucketEndpointUsesTypedAccess(t *testing.T) {
	e := &Endpoint{Engine: "s3"}
	if err := parseDSN(e, testBucketJSON); err != nil {
		t.Fatal(err)
	}
	if e.Host != "s3.us-east-1.amazonaws.com" || e.Port != 443 || e.Database != "application-assets" || e.TLS != TLSVerifyFull || e.Username != "" {
		t.Fatalf("unsafe bucket endpoint metadata: %+v", e)
	}
}
func TestBucketEndpointRejectsRedirectedCredentials(t *testing.T) {
	for _, raw := range []string{
		strings.Replace(testBucketJSON, `"provider":"aws"`, `"provider":"aws","endpoint":"https://evil.example"`, 1),
		strings.Replace(testBucketJSON, `"bucket":"application-assets"`, `"bucket":"../../metadata"`, 1),
		strings.Replace(testBucketJSON, `"expected_owner":"123456789012"`, `"expected_owner":""`, 1),
		strings.Replace(testBucketJSON, `"region":"us-east-1"`, `"region":"us-east-1","region":"evil.example"`, 1),
	} {
		err := parseDSN(&Endpoint{Engine: "s3"}, raw)
		if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "application-secret") {
			t.Fatalf("unsafe parse result: %v", err)
		}
	}
}

func TestGoogleBucketAccessAcceptsProviderBucketNames(t *testing.T) {
	raw, err := EncodeBucketAccess(BucketAccess{Provider: "gcp", Bucket: "application_assets", Region: "us", ServiceAccountJSON: bucketGoogleKey(t)})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := &Endpoint{Engine: EngineGCS}
	if err := parseDSN(endpoint, raw); err != nil || endpoint.Host != "storage.googleapis.com" || endpoint.Database != "application_assets" {
		t.Fatalf("metadata=%+v err=%v", endpoint, err)
	}
}
func TestBucketEndpointNeverStoresCredentialFields(t *testing.T) {
	store := newMemStore()
	secrets := memSecrets{}
	service := NewService(store, secrets, roleAuthz{})
	endpoint, err := service.Create(t.Context(), admin, CreateInput{Name: "bucket", Engine: EngineS3, DSN: testBucketJSON})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(endpoint.Username, "AKIA") || strings.Contains(endpoint.Database, "secret") {
		t.Fatal("metadata carries credentials")
	}
	resolved, raw, err := service.Resolve(t.Context(), admin.OrgID, endpoint.ID)
	if err != nil || raw != testBucketJSON || resolved.Host != "s3.us-east-1.amazonaws.com" {
		t.Fatalf("resolve metadata=%+v err=%v", resolved, err)
	}
}

func TestGoogleBucketOutputsKeepJSONExplicitAndRedacted(t *testing.T) {
	access := BucketAccess{Provider: "gcp", Bucket: "application-assets", Region: "us", ServiceAccountJSON: bucketGoogleKey(t)}
	raw, err := EncodeBucketAccess(access)
	if err != nil {
		t.Fatal(err)
	}
	out, err := BucketOutputs(raw)
	if err != nil {
		t.Fatal(err)
	}
	if out["STORAGE_URL"] != "gs://application-assets" || out["GOOGLE_SERVICE_ACCOUNT_JSON"] != access.ServiceAccountJSON || len(out) != len(BucketEnvironment(EngineGCS)) {
		t.Fatal("incorrect Google bucket outputs")
	}
	if _, ok := out["GOOGLE_APPLICATION_CREDENTIALS"]; ok {
		t.Fatal("inline JSON masquerades as credential file path")
	}
	key, err := parseBucketServiceAccount(access.ServiceAccountJSON)
	if err != nil {
		t.Fatal(err)
	}
	escaped, err := json.Marshal(key.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	message := redact("refused "+key.PrivateKey+" "+string(escaped[1:len(escaped)-1])+" "+key.PrivateKeyID, raw)
	if strings.Contains(message, key.PrivateKey) || strings.Contains(message, string(escaped[1:len(escaped)-1])) || strings.Contains(message, key.PrivateKeyID) {
		t.Fatal("Google private key fragments not redacted")
	}
}

func TestValidBucketNameUsesProviderRules(t *testing.T) {
	for _, tc := range []struct {
		provider, name string
		valid          bool
	}{
		{"gcp", "application_assets", true}, {"gcp", "assets._archive.example", true}, {"aws", "application_assets", false}, {"aws", "application-assets", true}, {"gcp", "first." + strings.Repeat("a", 63) + ".last", true}, {"gcp", "first." + strings.Repeat("a", 64) + ".last", false}, {"aws", "192.0.2.1", false}, {"gcp", "192.0.2.1", false}, {"unknown", "application-assets", false},
	} {
		if got := ValidBucketName(tc.provider, tc.name); got != tc.valid {
			t.Errorf("name acceptance %s/%s=%v want %v", tc.provider, tc.name, got, tc.valid)
		}
	}
}

func TestGoogleBucketKeyRejectsUnusedCredentialURLs(t *testing.T) {
	key := bucketGoogleKey(t)
	for _, field := range []string{"auth_uri", "auth_provider_x509_cert_url", "client_x509_cert_url"} {
		var account map[string]any
		if err := json.Unmarshal([]byte(key), &account); err != nil {
			t.Fatal(err)
		}
		account[field] = "https://evil.example/credentials"
		encoded, err := json.Marshal(account)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = EncodeBucketAccess(BucketAccess{Provider: "gcp", Bucket: "application-assets", Region: "us", ServiceAccountJSON: string(encoded)}); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted foreign credential URL %s", field)
		}
	}
}
