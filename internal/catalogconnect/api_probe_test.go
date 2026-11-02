package catalogconnect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
)

func TestAPIProberUsesGuardedTLSReadOnlyRequestsAndPinsIdentity(t *testing.T) {
	for _, tc := range []struct{ kind, id, name, host, path, body, token string }{
		{"cloudflare-d1", "db-id", "orders", "api.cloudflare.com", "/client/v4/accounts/" + strings.Repeat("a", 32) + "/d1/database/db-id", `{"success":true,"result":{"uuid":"db-id"}}`, "application-token"},
		{"cloudflare-kv", "kv-id", "cache", "api.cloudflare.com", "/client/v4/accounts/" + strings.Repeat("a", 32) + "/storage/kv/namespaces/kv-id", `{"success":true,"result":{"id":"kv-id"}}`, "application-token"},
		{"cloudflare-queue", "queue-id", "events", "api.cloudflare.com", "/client/v4/accounts/" + strings.Repeat("a", 32) + "/queues/queue-id", `{"success":true,"result":{"queue_id":"queue-id"}}`, "application-token"},
		{"cloudflare-vectorize", "index-name", "index-name", "api.cloudflare.com", "/client/v4/accounts/" + strings.Repeat("a", 32) + "/vectorize/v2/indexes/index-name", `{"success":true,"result":{"name":"index-name"}}`, "application-token"},
		{"vercel-blob", "store_abc", "assets", "vercel.com", "/api/blob", `{"blobs":[],"hasMore":false}`, "vercel_blob_rw_abc_secret"},
		{"vercel-global-config", "ecfg_abc", "settings", "global-config.vercel.com", "/ecfg_abc/digest", `"0123456789abcdef"`, "read-token"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			a := APIAccess{Provider: "cloudflare", Kind: tc.kind, AccountID: strings.Repeat("a", 32), ResourceID: tc.id, Name: tc.name, Token: tc.token}
			if strings.HasPrefix(tc.kind, "vercel-") {
				a.Provider = "vercel"
				a.AccountID = "team_abc"
			}
			raw, err := EncodeAPIAccess(a)
			if err != nil {
				t.Fatal(err)
			}
			bp, dial, dials := bucketFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.Host != tc.host || r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer "+tc.token {
					t.Error("unscoped API check")
				}
				if tc.kind == "vercel-blob" && (r.URL.Query().Get("limit") != "1" || r.Header.Get("X-Vercel-Blob-Store-Id") != "abc" || r.Header.Get("X-Api-Version") != "13") {
					t.Error("Blob check did not match SDK store-pinned list")
				}
				fmt.Fprint(w, tc.body)
			})
			report, err := APIProber(bp).Probe(t.Context(), dial, raw, recipes.Requirement{Engine: tc.kind})
			if err != nil || report == nil || !report.Result().OK || len(*dials) != 1 || (*dials)[0] != tc.host+":443" {
				t.Fatalf("report=%+v err=%v dials=%v", report, err, *dials)
			}
		})
	}
}
func TestAPIProberRejectsRedirectMalformedAndWrongResourceWithoutLeakingToken(t *testing.T) {
	for _, scenario := range []string{"redirect", "identity", "malformed", "denied", "too-large"} {
		t.Run(scenario, func(t *testing.T) {
			raw, _ := EncodeAPIAccess(APIAccess{Provider: "cloudflare", Kind: "cloudflare-d1", AccountID: strings.Repeat("a", 32), ResourceID: "db-id", Token: "application-secret"})
			calls := 0
			bp, dial, _ := bucketFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				switch scenario {
				case "redirect":
					w.Header().Set("Location", "https://attacker.example")
					w.WriteHeader(307)
				case "identity":
					fmt.Fprint(w, `{"success":true,"result":{"uuid":"other-db"}}`)
				case "malformed":
					fmt.Fprint(w, `{`)
				case "denied":
					w.WriteHeader(403)
					fmt.Fprint(w, `application-secret`)
				case "too-large":
					fmt.Fprint(w, strings.Repeat("x", 130*1024))
				}
			})
			_, err := APIProber(bp).Probe(t.Context(), dial, raw, recipes.Requirement{Engine: "cloudflare-d1"})
			if err == nil || strings.Contains(err.Error(), "application-secret") || calls != 1 {
				t.Fatal("failed API proof was accepted or leaked a credential")
			}
		})
	}
}
func TestNativeBindingProbeDoesNotClaimNetworkReachability(t *testing.T) {
	raw, _ := EncodeAPIAccess(APIAccess{Provider: "cloudflare", Kind: "cloudflare-hyperdrive", AccountID: strings.Repeat("a", 32), ResourceID: "hyperdrive-id", Native: true})
	calls := 0
	dial := func(context.Context, string, string) (net.Conn, error) {
		calls++
		return nil, errors.New("must not dial")
	}
	report, err := (APIProber{}).Probe(t.Context(), dial, raw, recipes.Requirement{Engine: "cloudflare-hyperdrive"})
	if err != nil || report == nil || report.Version != "native-binding" || calls != 0 {
		t.Fatal("native binding validation made a network claim")
	}
}

func TestMarketplaceAPIProbeUsesBackingConnectionAndRejectsUnknownOutputs(t *testing.T) {
	calls := 0
	dial := func(_ context.Context, _, address string) (net.Conn, error) {
		calls++
		if address != "database.example:5432" {
			t.Error("backing connection was not pinned")
		}
		return nil, errors.New("guarded network denied")
	}
	a := APIAccess{Provider: "vercel", Kind: "vercel-integration", ProjectID: "prj_application", Environment: "production", AccountID: "team_a", ResourceID: "store_a", Values: map[string]string{"DATABASE_URL": "postgres://app:application-secret@database.example/app?sslmode=require"}}
	raw, _ := EncodeAPIAccess(a)
	report, err := (APIProber{}).Probe(t.Context(), dial, raw, recipes.Requirement{Engine: "vercel-integration"})
	if calls != 1 || err == nil && (report == nil || report.Result().OK) {
		t.Fatal("Marketplace access skipped the actual backing connection")
	}
	a.Values = map[string]string{"UNKNOWN_SECRET": "application-secret"}
	raw, _ = EncodeAPIAccess(a)
	calls = 0
	report, err = (APIProber{}).Probe(t.Context(), dial, raw, recipes.Requirement{Engine: "vercel-integration"})
	if err == nil && (report == nil || report.Result().OK) || calls != 0 {
		t.Fatal("unknown integration outputs claimed verification")
	}
}
func TestMarketplaceUpstashProbeUsesScopedReadOnlyPing(t *testing.T) {
	a := APIAccess{Provider: "vercel", Kind: "vercel-integration", ProjectID: "prj_application", Environment: "production", AccountID: "team_a", ResourceID: "store_a", Values: map[string]string{"KV_REST_API_URL": "https://application.upstash.io", "KV_REST_API_TOKEN": "application-token"}}
	raw, _ := EncodeAPIAccess(a)
	bp, dial, _ := bucketFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Host != "application.upstash.io" || r.URL.Path != "/ping" || r.Header.Get("Authorization") != "Bearer application-token" {
			t.Error("Upstash check was not scoped read-only ping")
		}
		fmt.Fprint(w, `{"result":"PONG"}`)
	})
	report, err := APIProber(bp).Probe(t.Context(), dial, raw, recipes.Requirement{Engine: "vercel-integration"})
	if err != nil || report == nil || !report.Result().OK {
		t.Fatal("valid Upstash connection not verified")
	}
}

func TestBucketProberR2UsesAccountScopedS3AccessWithoutAWSOwnerClaim(t *testing.T) {
	raw, err := EncodeBucketAccess(BucketAccess{Provider: "cloudflare", AccountID: strings.Repeat("a", 32), Jurisdiction: "eu", Bucket: "application-assets", Region: "auto", AccessKeyID: strings.Repeat("b", 32), SecretAccessKey: strings.Repeat("c", 64)})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	prober, dial, dials := bucketFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Host != strings.Repeat("a", 32)+".eu.r2.cloudflarestorage.com" || r.Header.Get("X-Amz-Expected-Bucket-Owner") != "" || !strings.Contains(r.Header.Get("Authorization"), "/auto/s3/aws4_request") || r.Method != "HEAD" && r.Method != "GET" {
			t.Error("R2 did not use its scoped S3 contract")
		}
		if r.Method == "GET" {
			fmt.Fprint(w, `<ListBucketResult><Name>application-assets</Name></ListBucketResult>`)
		}
	})
	report, err := prober.Probe(t.Context(), dial, raw, recipes.Requirement{Engine: EngineR2})
	if err != nil || report == nil || !report.Result().OK || calls != 2 || len(*dials) != 2 {
		t.Fatal("R2 signed listing check did not complete")
	}
}
